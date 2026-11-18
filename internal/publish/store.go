package publish

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Record is one publication the daemon made.
type Record struct {
	ID          string    `json:"id"`
	Project     string    `json:"project"`
	Path        string    `json:"path"` // root-relative, "/"-separated
	URL         string    `json:"url"`
	SHA256      string    `json:"sha256"`
	Size        int64     `json:"size"`
	Server      string    `json:"server"` // share server base URL it lives on
	PublishedAt time.Time `json:"published_at"`
	ExpiresAt   time.Time `json:"expires_at,omitzero"` // zero: never
}

// Expired reports whether the share server no longer serves r at now.
func (r Record) Expired(now time.Time) bool {
	return !r.ExpiresAt.IsZero() && !now.Before(r.ExpiresAt)
}

type storeDoc struct {
	Publications []Record `json:"publications"`
}

// Store holds the daemon's publication records: in an owner-only JSON file
// when the daemon persists state, otherwise in memory. Expired records are
// never returned and are dropped on the next write.
type Store struct {
	mu   sync.Mutex
	path string
	mem  []Record
}

// NewStore returns a store backed by path, or memory-only when path is empty.
func NewStore(path string) *Store { return &Store{path: path} }

// List returns project's unexpired records, newest first; a non-empty relPath
// restricts it to that file.
func (s *Store) List(project, relPath string, now time.Time) ([]Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	all, err := s.load()
	if err != nil {
		return nil, err
	}
	var out []Record
	for _, r := range all {
		if r.Project == project && (relPath == "" || r.Path == relPath) && !r.Expired(now) {
			out = append(out, r)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].PublishedAt.After(out[j].PublishedAt) })
	return out, nil
}

// Get returns project's unexpired record with id.
func (s *Store) Get(project, id string, now time.Time) (Record, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	all, err := s.load()
	if err != nil {
		return Record{}, false, err
	}
	for _, r := range all {
		if r.Project == project && r.ID == id && !r.Expired(now) {
			return r, true, nil
		}
	}
	return Record{}, false, nil
}

// Add stores r.
func (s *Store) Add(r Record, now time.Time) error {
	return s.mutate(now, func(all []Record) []Record { return append(all, r) })
}

// Remove forgets the record with id (a no-op when absent).
func (s *Store) Remove(id string, now time.Time) error {
	return s.mutate(now, func(all []Record) []Record {
		out := all[:0]
		for _, r := range all {
			if r.ID != id {
				out = append(out, r)
			}
		}
		return out
	})
}

// RenameProject relabels records of project oldName.
func (s *Store) RenameProject(oldName, newName string, now time.Time) error {
	return s.mutate(now, func(all []Record) []Record {
		for i := range all {
			if all[i].Project == oldName {
				all[i].Project = newName
			}
		}
		return all
	})
}

func (s *Store) mutate(now time.Time, fn func([]Record) []Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	all, err := s.load()
	if err != nil {
		return err
	}
	all = fn(all)
	kept := make([]Record, 0, len(all))
	for _, r := range all {
		if !r.Expired(now) {
			kept = append(kept, r)
		}
	}
	if s.path == "" {
		s.mem = kept
		return nil
	}
	return s.save(kept)
}

func (s *Store) load() ([]Record, error) {
	if s.path == "" {
		return append([]Record(nil), s.mem...), nil
	}
	b, err := os.ReadFile(s.path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read publications: %w", err)
	}
	var doc storeDoc
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("parse %s: %w", s.path, err)
	}
	return doc.Publications, nil
}

// save writes the file atomically and owner-only: it holds secret links.
func (s *Store) save(all []Record) error {
	b, err := json.MarshalIndent(storeDoc{Publications: all}, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("write publications: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".publications-*.json")
	if err != nil {
		return fmt.Errorf("write publications: %w", err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return fmt.Errorf("write publications: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("write publications: %w", err)
	}
	if err := os.Rename(tmp.Name(), s.path); err != nil {
		return fmt.Errorf("write publications: %w", err)
	}
	return nil
}
