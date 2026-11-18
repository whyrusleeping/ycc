package shareserver

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// meta is the <id>.json record. An item exists exactly when its metadata file does.
type meta struct {
	Name      string    `json:"name"`
	MediaType string    `json:"media_type"`
	Size      int64     `json:"size"`
	SHA256    string    `json:"sha256"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at,omitzero"`
}

func (m *meta) expired(now time.Time) bool {
	return !m.ExpiresAt.IsZero() && !now.Before(m.ExpiresAt)
}

const (
	idLen     = 22 // 16 random bytes, base64url without padding
	tmpPrefix = ".tmp-"
	// staleAge is how old a temporary file or metadata-less blob must be before the sweep
	// treats it as debris from an interrupted upload rather than one in progress.
	staleAge = time.Hour
)

func newID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b[:])
}

// validID reports whether s has the shape of an id. Anything else in an id position is 404,
// which also keeps request input out of file paths.
func validID(s string) bool {
	if len(s) != idLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

func (s *Server) blobPath(id string) string { return filepath.Join(s.dir, id+".blob") }
func (s *Server) metaPath(id string) string { return filepath.Join(s.dir, id+".json") }

// store streams body into a new item. On any error nothing is left behind.
func (s *Server) store(body io.Reader, name string) (string, *meta, error) {
	tmp, err := os.CreateTemp(s.dir, tmpPrefix+"*")
	if err != nil {
		return "", nil, err
	}
	tmpName := tmp.Name()
	committed := false
	defer func() {
		if !committed {
			tmp.Close()
			os.Remove(tmpName)
		}
	}()

	h := sha256.New()
	head := &prefixWriter{max: sniffLen}
	n, err := io.Copy(io.MultiWriter(tmp, h, head), body)
	if err != nil {
		return "", nil, err
	}
	if err := tmp.Sync(); err != nil {
		return "", nil, err
	}
	if err := tmp.Close(); err != nil {
		return "", nil, err
	}

	now := s.now()
	m := &meta{
		Name:      name,
		MediaType: detectMediaType(name, head.buf),
		Size:      n,
		SHA256:    hex.EncodeToString(h.Sum(nil)),
		CreatedAt: now.UTC(),
	}
	if s.ttl > 0 {
		m.ExpiresAt = now.Add(s.ttl).UTC()
	}
	id := newID()
	if err := os.Rename(tmpName, s.blobPath(id)); err != nil {
		return "", nil, err
	}
	committed = true
	if err := s.writeMeta(id, m); err != nil {
		os.Remove(s.blobPath(id))
		return "", nil, err
	}
	return id, m, nil
}

func (s *Server) writeMeta(id string, m *meta) error {
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.dir, tmpPrefix+"*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	_, err = tmp.Write(data)
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmpName, s.metaPath(id))
	}
	if err != nil {
		os.Remove(tmpName)
	}
	return err
}

// lookup returns the metadata of a live item, or nil when the id is malformed, unknown, or
// expired.
func (s *Server) lookup(id string) *meta {
	if !validID(id) {
		return nil
	}
	m, err := s.readMeta(id)
	if err != nil || m.expired(s.now()) {
		return nil
	}
	return m
}

func (s *Server) readMeta(id string) (*meta, error) {
	data, err := os.ReadFile(s.metaPath(id))
	if err != nil {
		return nil, err
	}
	var m meta
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// remove deletes an item, metadata first so it disappears atomically.
func (s *Server) remove(id string) error {
	if err := os.Remove(s.metaPath(id)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := os.Remove(s.blobPath(id)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// Sweep deletes expired items, plus temporary files and metadata-less blobs left by
// interrupted uploads.
func (s *Server) Sweep() error {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return err
	}
	now := s.now()
	var firstErr error
	note := func(err error) {
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	for _, e := range entries {
		name := e.Name()
		switch {
		case strings.HasPrefix(name, tmpPrefix):
			if stale(e, now) {
				note(os.Remove(filepath.Join(s.dir, name)))
			}
		case strings.HasSuffix(name, ".json"):
			id := strings.TrimSuffix(name, ".json")
			if !validID(id) {
				continue
			}
			m, err := s.readMeta(id)
			if err != nil {
				if !errors.Is(err, fs.ErrNotExist) {
					log.Printf("ycc-share: sweep: %s: %v", name, err)
				}
				continue
			}
			if m.expired(now) {
				note(s.remove(id))
			}
		case strings.HasSuffix(name, ".blob"):
			id := strings.TrimSuffix(name, ".blob")
			if !validID(id) || !stale(e, now) {
				continue
			}
			if _, err := os.Stat(s.metaPath(id)); errors.Is(err, fs.ErrNotExist) {
				note(os.Remove(filepath.Join(s.dir, name)))
			}
		}
	}
	return firstErr
}

func stale(e fs.DirEntry, now time.Time) bool {
	info, err := e.Info()
	return err == nil && now.Sub(info.ModTime()) > staleAge
}

// prefixWriter keeps the first max bytes written to it.
type prefixWriter struct {
	buf []byte
	max int
}

func (p *prefixWriter) Write(b []byte) (int, error) {
	if room := p.max - len(p.buf); room > 0 {
		p.buf = append(p.buf, b[:min(room, len(b))]...)
	}
	return len(b), nil
}
