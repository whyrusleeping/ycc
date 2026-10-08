// Package uianalytics stores client usage analytics reported through the
// daemon's RecordUiEvents RPC and summarises them for `ycc analytics`.
//
// Events carry only identifiers from a fixed vocabulary (screen names, action
// ids, input methods, error codes); see docs/design/usage-analytics.md. The
// store enforces a no-spaces identifier charset and length bounds, dropping
// events that fail, so accidental prose is unlikely to be persisted.
//
// Layout under the store directory (owner-only):
//
//	events-YYYY-MM.jsonl  one Record per line, partitioned by receive month
//	catalog.json          latest catalog per client
package uianalytics

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
)

const (
	// RetentionMonths is how many monthly files (including the current one)
	// are kept; older files are deleted.
	RetentionMonths = 12

	maxEventsPerBatch  = 500
	maxCatalogEntries  = 1000
	maxAttrs           = 8
	maxNameLen         = 80
	maxViaLen          = 24
	maxClientLen       = 16
	maxVersionLen      = 64
	maxVisitLen        = 64
	maxAttrKeyLen      = 32
	maxAttrValueLen    = 64
	maxShortcutLen     = 32
	maxDuration        = 24 * time.Hour
	clientSkewPast     = 7 * 24 * time.Hour
	clientSkewFuture   = 5 * time.Minute
	catalogFile        = "catalog.json"
	eventFilePrefix    = "events-"
	eventFileSuffix    = ".jsonl"
	eventFileMonthForm = "2006-01"
)

// Kinds a client may record.
var validKinds = map[string]bool{"visit": true, "view": true, "action": true, "error": true}

// Record is one stored event. Time is the client's event time (unix ms),
// replaced by the receive time when the client clock is implausible.
type Record struct {
	Time     int64             `json:"t"`
	Client   string            `json:"client"`
	Version  string            `json:"ver,omitempty"`
	Visit    string            `json:"visit,omitempty"`
	Kind     string            `json:"kind"`
	Name     string            `json:"name"`
	View     string            `json:"view,omitempty"`
	Via      string            `json:"via,omitempty"`
	Duration int64             `json:"dur,omitempty"`
	Attrs    map[string]string `json:"attrs,omitempty"`
}

// CatalogEntry is something a client could record.
type CatalogEntry struct {
	Kind     string `json:"kind"`
	Name     string `json:"name"`
	Shortcut string `json:"shortcut,omitempty"`
}

// Catalog is a client's latest reported catalog.
type Catalog struct {
	Updated int64          `json:"updated"`
	Entries []CatalogEntry `json:"entries"`
}

// ErrInvalidClient reports a missing or malformed client name; the whole batch
// is rejected because it could not be attributed.
var ErrInvalidClient = errors.New("uianalytics: invalid client name")

// Store is an append-only, owner-only analytics store. It is safe for
// concurrent use.
type Store struct {
	dir string
	now func() time.Time

	mu        sync.Mutex
	lastPrune time.Time
}

// DefaultDir is <XDG_STATE_HOME or ~/.local/state>/ycc/analytics, alongside
// the daemon's project and workstream registries.
func DefaultDir() string {
	dir := os.Getenv("XDG_STATE_HOME")
	if dir == "" {
		if home, err := os.UserHomeDir(); err == nil {
			dir = filepath.Join(home, ".local", "state")
		}
	}
	if dir == "" {
		dir = os.TempDir()
	}
	return filepath.Join(dir, "ycc", "analytics")
}

// Open creates (owner-only) and returns the store at dir, pruning expired
// monthly files.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create analytics dir: %w", err)
	}
	// MkdirAll leaves an existing directory's mode alone; tighten it.
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, fmt.Errorf("chmod analytics dir: %w", err)
	}
	s := &Store{dir: dir, now: time.Now}
	s.mu.Lock()
	s.pruneLocked()
	s.mu.Unlock()
	return s, nil
}

// OpenReadOnly returns a store for reading an existing directory without
// creating or pruning anything (the CLI's daemon-less fallback).
func OpenReadOnly(dir string) *Store { return &Store{dir: dir, now: time.Now} }

// Dir is the store directory.
func (s *Store) Dir() string { return s.dir }

// validToken reports whether v is 1..max characters of [A-Za-z0-9_.:/-].
func validToken(v string, max int) bool {
	if len(v) == 0 || len(v) > max {
		return false
	}
	for i := 0; i < len(v); i++ {
		c := v[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
			c == '_' || c == '.' || c == ':' || c == '/' || c == '-' {
			continue
		}
		return false
	}
	return true
}

func optionalToken(v string, max int) bool { return v == "" || validToken(v, max) }

// validShortcut accepts a short single-line display label such as "⌘K" or
// "Ctrl+Shift+P".
func validShortcut(v string) bool {
	if len(v) > maxShortcutLen {
		return false
	}
	for _, r := range v {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

// Validate converts a request into records, returning the accepted records
// and the number of dropped events. now is the receive time.
func Validate(req *v1.RecordUiEventsRequest, now time.Time) ([]Record, int, error) {
	if !validToken(req.GetClient(), maxClientLen) {
		return nil, 0, ErrInvalidClient
	}
	version := req.GetClientVersion()
	if !optionalToken(version, maxVersionLen) {
		version = ""
	}
	visit := req.GetVisitId()
	if !optionalToken(visit, maxVisitLen) {
		visit = ""
	}
	events := req.GetEvents()
	dropped := 0
	if len(events) > maxEventsPerBatch {
		dropped += len(events) - maxEventsPerBatch
		events = events[:maxEventsPerBatch]
	}
	nowMS := now.UnixMilli()
	minMS, maxMS := now.Add(-clientSkewPast).UnixMilli(), now.Add(clientSkewFuture).UnixMilli()
	out := make([]Record, 0, len(events))
	for _, e := range events {
		rec, ok := validateEvent(e)
		if !ok {
			dropped++
			continue
		}
		rec.Time = e.GetTimeMs()
		if rec.Time < minMS || rec.Time > maxMS {
			rec.Time = nowMS
		}
		rec.Client, rec.Version, rec.Visit = req.GetClient(), version, visit
		out = append(out, rec)
	}
	return out, dropped, nil
}

func validateEvent(e *v1.UiEvent) (Record, bool) {
	if e == nil || !validKinds[e.GetKind()] || !validToken(e.GetName(), maxNameLen) ||
		!optionalToken(e.GetView(), maxNameLen) || !optionalToken(e.GetVia(), maxViaLen) ||
		len(e.GetAttrs()) > maxAttrs {
		return Record{}, false
	}
	var attrs map[string]string
	if len(e.GetAttrs()) > 0 {
		attrs = make(map[string]string, len(e.GetAttrs()))
		for k, v := range e.GetAttrs() {
			if !validToken(k, maxAttrKeyLen) || !validToken(v, maxAttrValueLen) {
				return Record{}, false
			}
			attrs[k] = v
		}
	}
	dur := e.GetDurationMs()
	if dur < 0 {
		dur = 0
	}
	if max := maxDuration.Milliseconds(); dur > max {
		dur = max
	}
	return Record{Kind: e.GetKind(), Name: e.GetName(), View: e.GetView(), Via: e.GetVia(), Duration: dur, Attrs: attrs}, true
}

// Record validates and appends a batch, and replaces the client's catalog when
// one is supplied. It returns the accepted and dropped event counts.
func (s *Store) Record(req *v1.RecordUiEventsRequest) (accepted, dropped int, err error) {
	now := s.now()
	recs, dropped, err := Validate(req, now)
	if err != nil {
		return 0, 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if now.Sub(s.lastPrune) > 24*time.Hour {
		s.pruneLocked()
	}
	if len(recs) > 0 {
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		for i := range recs {
			if err := enc.Encode(&recs[i]); err != nil {
				return 0, dropped, err
			}
		}
		path := filepath.Join(s.dir, eventFilePrefix+now.UTC().Format(eventFileMonthForm)+eventFileSuffix)
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
		if err != nil {
			return 0, dropped, fmt.Errorf("open analytics log: %w", err)
		}
		_, werr := f.Write(buf.Bytes())
		cerr := f.Close()
		if werr != nil {
			return 0, dropped, fmt.Errorf("append analytics log: %w", werr)
		}
		if cerr != nil {
			return 0, dropped, fmt.Errorf("close analytics log: %w", cerr)
		}
	}
	if len(req.GetCatalog()) > 0 {
		if err := s.writeCatalogLocked(req.GetClient(), req.GetCatalog(), now); err != nil {
			return len(recs), dropped, err
		}
	}
	return len(recs), dropped, nil
}

func (s *Store) writeCatalogLocked(client string, entries []*v1.UiCatalogEntry, now time.Time) error {
	all, err := s.readCatalogs()
	if err != nil {
		return err
	}
	seen := make(map[string]bool)
	var cat Catalog
	cat.Updated = now.UnixMilli()
	for _, e := range entries {
		if len(cat.Entries) >= maxCatalogEntries {
			break
		}
		if (e.GetKind() != "view" && e.GetKind() != "action") || !validToken(e.GetName(), maxNameLen) {
			continue
		}
		key := e.GetKind() + " " + e.GetName()
		if seen[key] {
			continue
		}
		seen[key] = true
		sc := e.GetShortcut()
		if !validShortcut(sc) {
			sc = ""
		}
		cat.Entries = append(cat.Entries, CatalogEntry{Kind: e.GetKind(), Name: e.GetName(), Shortcut: sc})
	}
	sort.Slice(cat.Entries, func(i, j int) bool {
		if cat.Entries[i].Kind != cat.Entries[j].Kind {
			return cat.Entries[i].Kind < cat.Entries[j].Kind
		}
		return cat.Entries[i].Name < cat.Entries[j].Name
	})
	all[client] = cat
	b, err := json.MarshalIndent(all, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.dir, catalogFile+".*")
	if err != nil {
		return fmt.Errorf("write analytics catalog: %w", err)
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return fmt.Errorf("write analytics catalog: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return fmt.Errorf("write analytics catalog: %w", err)
	}
	if err := os.Rename(tmp.Name(), filepath.Join(s.dir, catalogFile)); err != nil {
		os.Remove(tmp.Name())
		return fmt.Errorf("write analytics catalog: %w", err)
	}
	return nil
}

func (s *Store) readCatalogs() (map[string]Catalog, error) {
	all := make(map[string]Catalog)
	b, err := os.ReadFile(filepath.Join(s.dir, catalogFile))
	if errors.Is(err, os.ErrNotExist) {
		return all, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read analytics catalog: %w", err)
	}
	if err := json.Unmarshal(b, &all); err != nil {
		// A corrupt catalog is replaceable state; start over rather than wedge.
		return make(map[string]Catalog), nil
	}
	return all, nil
}

// Catalogs returns the latest catalog per client.
func (s *Store) Catalogs() (map[string]Catalog, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.readCatalogs()
}

// monthFiles returns the event files with their month start, oldest first.
func (s *Store) monthFiles() ([]string, []time.Time, error) {
	ents, err := os.ReadDir(s.dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	var names []string
	var months []time.Time
	for _, e := range ents {
		n := e.Name()
		if e.IsDir() || !strings.HasPrefix(n, eventFilePrefix) || !strings.HasSuffix(n, eventFileSuffix) {
			continue
		}
		m, err := time.Parse(eventFileMonthForm, strings.TrimSuffix(strings.TrimPrefix(n, eventFilePrefix), eventFileSuffix))
		if err != nil {
			continue
		}
		names = append(names, filepath.Join(s.dir, n))
		months = append(months, m)
	}
	return names, months, nil
}

func (s *Store) pruneLocked() {
	now := s.now().UTC()
	s.lastPrune = s.now()
	cutoff := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, -(RetentionMonths - 1), 0)
	names, months, err := s.monthFiles()
	if err != nil {
		return
	}
	for i, m := range months {
		if m.Before(cutoff) {
			os.Remove(names[i])
		}
	}
}

// Load returns records with Time >= since (unix ms), optionally for one client.
// Malformed lines (e.g. a torn final write) are skipped.
func (s *Store) Load(since time.Time, client string) ([]Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	names, months, err := s.monthFiles()
	if err != nil {
		return nil, err
	}
	sinceMS := since.UnixMilli()
	// Files are partitioned by receive month while records carry client time
	// (at most clientSkewPast earlier), so include one extra month of margin.
	first := since.Add(-clientSkewPast).UTC()
	firstMonth := time.Date(first.Year(), first.Month(), 1, 0, 0, 0, 0, time.UTC)
	var out []Record
	for i, name := range names {
		if months[i].Before(firstMonth) {
			continue
		}
		if err := readRecords(name, func(r Record) {
			if r.Time >= sinceMS && (client == "" || r.Client == client) {
				out = append(out, r)
			}
		}); err != nil {
			return nil, err
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Time < out[j].Time })
	return out, nil
}

func readRecords(path string, fn func(Record)) error {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		var r Record
		if json.Unmarshal(sc.Bytes(), &r) != nil || r.Client == "" || r.Kind == "" {
			continue
		}
		fn(r)
	}
	return sc.Err()
}

// Report loads the window and summarises it.
func (s *Store) Report(days int, client string) (*v1.GetUiAnalyticsResponse, error) {
	if days <= 0 {
		days = 30
	}
	now := s.now()
	recs, err := s.Load(now.AddDate(0, 0, -days), client)
	if err != nil {
		return nil, err
	}
	cats, err := s.Catalogs()
	if err != nil {
		return nil, err
	}
	if client != "" {
		for c := range cats {
			if c != client {
				delete(cats, c)
			}
		}
	}
	resp := Summarize(recs, cats)
	resp.Enabled = true
	resp.StorageDir = s.dir
	resp.Days = int32(days)
	return resp, nil
}
