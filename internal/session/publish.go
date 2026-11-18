package session

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"path"
	"path/filepath"
	"time"

	"github.com/whyrusleeping/ycc/internal/config"
	"github.com/whyrusleeping/ycc/internal/projectfs"
	"github.com/whyrusleeping/ycc/internal/publish"
)

// PublishMaxBytes caps a file PublishFile reads and uploads.
const PublishMaxBytes = 64 << 20

// Publishing errors, mapped to RPC codes by the server.
var (
	ErrPublishNotConfigured = errors.New("publishing is not configured: set [publish] url and token_env in ycc.toml")
	ErrPublishNoToken       = errors.New("the publish token is not set")
	ErrUnknownPublication   = errors.New("no such publication")
)

// PublishTarget is the configured share server as clients see it.
type PublishTarget struct {
	Name string
	URL  string
}

// Publication is a record plus whether it matches the file's present bytes.
type Publication struct {
	publish.Record
	Current bool
}

// SetPublicationsFile persists publication records at path instead of memory.
func (m *Manager) SetPublicationsFile(path string) {
	m.publishMu.Lock()
	m.publications = publish.NewStore(path)
	m.publishMu.Unlock()
}

func (m *Manager) publicationStore() *publish.Store {
	m.publishMu.Lock()
	defer m.publishMu.Unlock()
	if m.publications == nil {
		m.publications = publish.NewStore("")
	}
	return m.publications
}

// PublishTarget returns the configured share server, if any.
func (m *Manager) PublishTarget() (PublishTarget, bool) {
	cfg := m.publishConfig()
	if cfg.URL == "" {
		return PublishTarget{}, false
	}
	name := cfg.Name
	if name == "" {
		if u, err := url.Parse(cfg.URL); err == nil {
			name = u.Host
		}
	}
	return PublishTarget{Name: name, URL: cfg.URL}, true
}

func (m *Manager) publishConfig() config.Publish {
	if m.reg == nil {
		return config.Publish{}
	}
	return m.reg.Publish()
}

// projectName canonicalizes a request's project (empty: the sole project).
func (m *Manager) projectName(name string) (string, error) {
	if name != "" {
		if _, ok := m.projects.Resolve(name); !ok {
			return "", fmt.Errorf("%w %q", ErrUnknownProject, name)
		}
		return name, nil
	}
	if _, err := m.resolveProjectWorkspace(""); err != nil {
		return "", err
	}
	return m.projects.List()[0].Name, nil
}

// Publications lists the project's unexpired publications, newest first.
// With a path, it lists that file's and marks those matching its present
// contents; a file that can no longer be read marks none current.
func (m *Manager) Publications(project, sessionID, relPath string) ([]Publication, error) {
	name, err := m.projectName(project)
	if err != nil {
		return nil, err
	}
	var sum string
	if relPath != "" {
		root, _, err := m.FileRoot(project, sessionID)
		if err != nil {
			return nil, err
		}
		clean, err := projectfs.CleanRel(relPath)
		if err != nil {
			return nil, err
		}
		if clean == "" {
			return nil, fmt.Errorf("%w: the project root is a directory", projectfs.ErrNotFile)
		}
		relPath = filepath.ToSlash(clean)
		if _, data, err := projectfs.ReadAll(root, relPath, PublishMaxBytes); err == nil {
			sum = digest(data)
		}
	}
	recs, err := m.publicationStore().List(name, relPath, time.Now())
	if err != nil {
		return nil, err
	}
	out := make([]Publication, len(recs))
	for i, r := range recs {
		out[i] = Publication{Record: r, Current: sum != "" && r.SHA256 == sum}
	}
	return out, nil
}

// PublishFile uploads a project (or session worktree) file to the share
// server. An unexpired publication of identical bytes on the same server is
// returned instead, with reused set.
func (m *Manager) PublishFile(ctx context.Context, project, sessionID, relPath string) (rec publish.Record, reused bool, err error) {
	target, ok := m.PublishTarget()
	if !ok {
		return publish.Record{}, false, ErrPublishNotConfigured
	}
	cfg := m.publishConfig()
	token := cfg.Token()
	if token == "" {
		return publish.Record{}, false, fmt.Errorf("%w: export %s or run `ycc token set %s`", ErrPublishNoToken, cfg.TokenEnv, cfg.TokenEnv)
	}
	name, err := m.projectName(project)
	if err != nil {
		return publish.Record{}, false, err
	}
	root, _, err := m.FileRoot(project, sessionID)
	if err != nil {
		return publish.Record{}, false, err
	}
	rel, data, err := projectfs.ReadAll(root, relPath, PublishMaxBytes)
	if err != nil {
		return publish.Record{}, false, err
	}
	sum := digest(data)

	// Serialize publishes so two clicks on the same file reuse one upload.
	m.publishOpMu.Lock()
	defer m.publishOpMu.Unlock()
	store := m.publicationStore()
	existing, err := store.List(name, rel, time.Now())
	if err != nil {
		return publish.Record{}, false, err
	}
	for _, r := range existing {
		if r.SHA256 == sum && r.Server == target.URL {
			return r, true, nil
		}
	}
	client := publish.NewClient(target.URL, token)
	item, err := client.Upload(ctx, path.Base(rel), data)
	if err != nil {
		return publish.Record{}, false, err
	}
	rec = publish.Record{
		ID:          item.ID,
		Project:     name,
		Path:        rel,
		URL:         item.URL,
		SHA256:      sum,
		Size:        int64(len(data)),
		Server:      target.URL,
		PublishedAt: time.Now().UTC(),
		ExpiresAt:   item.ExpiresAt,
	}
	if err := store.Add(rec, time.Now()); err != nil {
		// An unrecorded link could not be shown or revoked from ycc; take it down.
		if derr := client.Delete(context.WithoutCancel(ctx), item.ID); derr != nil {
			return publish.Record{}, false, fmt.Errorf("published %s but could not record it (%w), and removing it failed: %v", item.URL, err, derr)
		}
		return publish.Record{}, false, fmt.Errorf("could not record the publication, so it was removed: %w", err)
	}
	return rec, false, nil
}

// UnpublishFile deletes a publication from its share server and forgets it.
// An item the server no longer has is forgotten too.
func (m *Manager) UnpublishFile(ctx context.Context, project, id string) error {
	name, err := m.projectName(project)
	if err != nil {
		return err
	}
	store := m.publicationStore()
	rec, ok, err := store.Get(name, id, time.Now())
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%w %q", ErrUnknownPublication, id)
	}
	cfg := m.publishConfig()
	token := cfg.Token()
	if cfg.URL != rec.Server {
		// The record lives on a server that is no longer configured; its token
		// may not be ours to send there.
		return fmt.Errorf("%w: this link is on %s, not the configured share server", ErrPublishNotConfigured, rec.Server)
	}
	if token == "" {
		return fmt.Errorf("%w: export %s or run `ycc token set %s`", ErrPublishNoToken, cfg.TokenEnv, cfg.TokenEnv)
	}
	if err := publish.NewClient(rec.Server, token).Delete(ctx, rec.ID); err != nil && !errors.Is(err, publish.ErrNotFound) {
		return err
	}
	return store.Remove(rec.ID, time.Now())
}

func digest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
