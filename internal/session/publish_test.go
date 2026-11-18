package session

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/whyrusleeping/ycc/internal/config"
	"github.com/whyrusleeping/ycc/internal/project"
	"github.com/whyrusleeping/ycc/internal/projectfs"
)

// fakeShare is a minimal ycc-share API: it checks the bearer token and records
// uploads and deletes.
type fakeShare struct {
	mu      sync.Mutex
	srv     *httptest.Server
	uploads map[string][]byte // id -> bytes
	next    int
	deletes []string
}

func newFakeShare(t *testing.T, token string) *fakeShare {
	f := &fakeShare{uploads: map[string][]byte{}}
	mux := http.NewServeMux()
	auth := func(w http.ResponseWriter, r *http.Request) bool {
		if r.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(http.StatusUnauthorized)
			return false
		}
		return true
	}
	mux.HandleFunc("POST /api/items", func(w http.ResponseWriter, r *http.Request) {
		if !auth(w, r) {
			return
		}
		b, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.next++
		id := fmt.Sprintf("id%d", f.next)
		f.uploads[id] = b
		f.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
		fmt.Fprintf(w, `{"id":%q,"url":%q,"size":%d,"expires_at":"2099-01-01T00:00:00Z"}`, id, f.srv.URL+"/"+id+"/"+r.Header.Get("X-Ycc-Filename"), len(b))
	})
	mux.HandleFunc("DELETE /api/items/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !auth(w, r) {
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		id := r.PathValue("id")
		f.deletes = append(f.deletes, id)
		if _, ok := f.uploads[id]; !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		delete(f.uploads, id)
		w.WriteHeader(http.StatusNoContent)
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func publishManager(t *testing.T, pub config.Publish) (*Manager, string) {
	t.Helper()
	m := NewManager(config.NewRegistry(&config.Config{Publish: pub}), t.TempDir())
	m.SetProjects(project.NewMemory())
	dir := t.TempDir()
	if _, err := m.AddProject(dir, "demo"); err != nil {
		t.Fatal(err)
	}
	return m, dir
}

// TestPublishFileLifecycle covers the publish contract a client relies on:
// the token reaches the server, identical bytes reuse one link, changed bytes
// get a new one, `current` follows the file, and unpublish deletes remotely.
func TestPublishFileLifecycle(t *testing.T) {
	t.Setenv("TEST_PUBLISH_TOKEN", "tok-123456")
	share := newFakeShare(t, "tok-123456")
	m, dir := publishManager(t, config.Publish{URL: share.srv.URL, TokenEnv: "TEST_PUBLISH_TOKEN"})
	m.SetPublicationsFile(filepath.Join(t.TempDir(), "state", "publications.json"))
	ctx := context.Background()
	file := filepath.Join(dir, "docs", "report.md")
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("# v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	first, reused, err := m.PublishFile(ctx, "", "", "docs/report.md")
	if err != nil || reused {
		t.Fatalf("first publish = %+v reused=%v err=%v", first, reused, err)
	}
	if string(share.uploads[first.ID]) != "# v1\n" || first.Path != "docs/report.md" || first.Project != "demo" {
		t.Fatalf("upload/record mismatch: %+v uploads=%v", first, share.uploads)
	}
	again, reused, err := m.PublishFile(ctx, "demo", "", "docs/report.md")
	if err != nil || !reused || again.ID != first.ID || len(share.uploads) != 1 {
		t.Fatalf("republish of unchanged bytes = %+v reused=%v err=%v uploads=%d", again, reused, err, len(share.uploads))
	}

	if err := os.WriteFile(file, []byte("# v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	pubs, err := m.Publications("demo", "", "docs/report.md")
	if err != nil || len(pubs) != 1 || pubs[0].Current {
		t.Fatalf("after edit, publications = %+v err=%v; want one, not current", pubs, err)
	}
	second, reused, err := m.PublishFile(ctx, "demo", "", "docs/report.md")
	if err != nil || reused || second.ID == first.ID {
		t.Fatalf("publish of changed bytes = %+v reused=%v err=%v; want a new link", second, reused, err)
	}
	pubs, _ = m.Publications("demo", "", "docs/report.md")
	if len(pubs) != 2 || pubs[0].ID != second.ID || !pubs[0].Current || pubs[1].Current {
		t.Fatalf("publications = %+v; want newest first, only it current", pubs)
	}

	// Unpublish deletes on the server; an item the server already lost is
	// forgotten without error.
	if err := m.UnpublishFile(ctx, "demo", second.ID); err != nil {
		t.Fatal(err)
	}
	delete(share.uploads, first.ID)
	if err := m.UnpublishFile(ctx, "demo", first.ID); err != nil {
		t.Fatalf("unpublish of a server-side missing item: %v", err)
	}
	if pubs, _ := m.Publications("demo", "", ""); len(pubs) != 0 {
		t.Fatalf("publications after unpublish = %+v", pubs)
	}
	if len(share.deletes) != 2 {
		t.Fatalf("server deletes = %v", share.deletes)
	}
	if err := m.UnpublishFile(ctx, "demo", first.ID); !errors.Is(err, ErrUnknownPublication) {
		t.Fatalf("unpublish of forgotten id err = %v", err)
	}
}

// TestPublishRecordsPersistPrivately: the records hold secret links, so the
// state file must be owner-only and survive a new manager.
func TestPublishRecordsPersistPrivately(t *testing.T) {
	t.Setenv("TEST_PUBLISH_TOKEN", "tok-123456")
	share := newFakeShare(t, "tok-123456")
	m, dir := publishManager(t, config.Publish{URL: share.srv.URL, TokenEnv: "TEST_PUBLISH_TOKEN"})
	state := filepath.Join(t.TempDir(), "publications.json")
	m.SetPublicationsFile(state)
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec, _, err := m.PublishFile(context.Background(), "demo", "", "a.txt")
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(state)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("publications.json mode = %v, want 0600", info.Mode().Perm())
	}
	m2, _ := publishManager(t, config.Publish{URL: share.srv.URL, TokenEnv: "TEST_PUBLISH_TOKEN"})
	m2.SetPublicationsFile(state)
	pubs, err := m2.Publications("demo", "", "")
	if err != nil || len(pubs) != 1 || pubs[0].URL != rec.URL {
		t.Fatalf("reloaded publications = %+v err=%v", pubs, err)
	}
}

// TestPublishFileRefusals: nothing leaves the host without config, a token,
// or a path inside the project.
func TestPublishFileRefusals(t *testing.T) {
	ctx := context.Background()
	m, dir := publishManager(t, config.Publish{})
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.PublishFile(ctx, "demo", "", "a.txt"); !errors.Is(err, ErrPublishNotConfigured) {
		t.Fatalf("unconfigured err = %v", err)
	}

	share := newFakeShare(t, "right-token")
	t.Setenv("TEST_PUBLISH_TOKEN", "")
	m, dir = publishManager(t, config.Publish{URL: share.srv.URL, TokenEnv: "TEST_PUBLISH_TOKEN"})
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.PublishFile(ctx, "demo", "", "a.txt"); !errors.Is(err, ErrPublishNoToken) {
		t.Fatalf("missing token err = %v", err)
	}

	t.Setenv("TEST_PUBLISH_TOKEN", "wrong-token")
	if _, _, err := m.PublishFile(ctx, "demo", "", "a.txt"); err == nil || len(share.uploads) != 0 {
		t.Fatalf("wrong token err = %v uploads=%d", err, len(share.uploads))
	}

	t.Setenv("TEST_PUBLISH_TOKEN", "right-token")
	outside := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(outside, []byte("s"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "escape")); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"../secret", "escape", outside, ".git/config"} {
		if _, _, err := m.PublishFile(ctx, "demo", "", p); err == nil {
			t.Errorf("PublishFile(%q) succeeded; want confinement refusal", p)
		}
	}
	if len(share.uploads) != 0 {
		t.Fatalf("uploads after refusals = %d", len(share.uploads))
	}
	big := filepath.Join(dir, "big.bin")
	if err := os.WriteFile(big, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(big, PublishMaxBytes+1); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.PublishFile(ctx, "demo", "", "big.bin"); !errors.Is(err, projectfs.ErrTooLarge) {
		t.Fatalf("oversized err = %v", err)
	}
}
