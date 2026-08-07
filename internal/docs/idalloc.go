package docs

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// maxTaskID scans dir without taking its docs directory lock and returns the
// largest numeric task id. It must remain lock-free with respect to dirLocks:
// a Store for the primary tree already holds that lock when its configured id
// source calls IDAllocator.NextID.
func maxTaskID(dir string) (int, error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	max := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		task, err := parseFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return 0, fmt.Errorf("%s: %w", entry.Name(), err)
		}
		if task == nil {
			continue
		}
		id, err := strconv.Atoi(normalizeID(task.ID))
		if err == nil && id > max {
			max = id
		}
	}
	return max, nil
}

// IDAllocator serializes monotonically increasing backlog ids by primary
// project tree. A non-empty path persists the per-project counters as JSON.
type IDAllocator struct {
	mu       sync.Mutex
	path     string
	counters map[string]int
}

var (
	allocatorsMu sync.Mutex
	allocators   = map[string]*IDAllocator{}
)

// AllocatorFor returns the process-wide allocator backed by path. Stores using
// the same path share one lock. An empty path selects the shared in-memory
// allocator used by one-shot daemon managers.
func AllocatorFor(path string) *IDAllocator {
	if path != "" {
		path = filepath.Clean(path)
	}
	allocatorsMu.Lock()
	defer allocatorsMu.Unlock()
	allocator := allocators[path]
	if allocator == nil {
		allocator = newIDAllocator(path)
		allocators[path] = allocator
	}
	return allocator
}

func newIDAllocator(path string) *IDAllocator {
	return &IDAllocator{path: path, counters: map[string]int{}}
}

// NextID reserves and returns the next id for primaryBacklogDir. The persistent
// counter is floored against the primary tree on every call, so manually added
// tasks can never cause an existing id to be re-issued.
func (a *IDAllocator) NextID(primaryBacklogDir string) (string, error) {
	primaryBacklogDir, err := filepath.Abs(primaryBacklogDir)
	if err != nil {
		return "", fmt.Errorf("resolve primary backlog: %w", err)
	}
	primaryBacklogDir = filepath.Clean(primaryBacklogDir)

	a.mu.Lock()
	defer a.mu.Unlock()

	if err := a.load(); err != nil {
		return "", err
	}
	scanMax, err := maxTaskID(primaryBacklogDir)
	if err != nil {
		return "", fmt.Errorf("scan primary backlog: %w", err)
	}
	next := a.counters[primaryBacklogDir]
	if scanMax > next {
		next = scanMax
	}
	next++
	a.counters[primaryBacklogDir] = next
	if err := a.save(); err != nil {
		return "", err
	}
	return fmt.Sprintf("%04d", next), nil
}

// load merges durable counters into the in-memory high-water marks. Merging
// rather than replacing ensures the allocator cannot regress within a process.
// Caller holds a.mu.
func (a *IDAllocator) load() error {
	if a.path == "" {
		return nil
	}
	data, err := os.ReadFile(a.path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read backlog id state: %w", err)
	}
	durable := map[string]int{}
	if len(data) > 0 {
		if err := json.Unmarshal(data, &durable); err != nil {
			return fmt.Errorf("parse backlog id state: %w", err)
		}
	}
	for key, value := range durable {
		if value > a.counters[key] {
			a.counters[key] = value
		}
	}
	return nil
}

// save atomically persists the current high-water marks. Caller holds a.mu.
func (a *IDAllocator) save() error {
	if a.path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(a.path), 0o755); err != nil {
		return fmt.Errorf("create backlog id state dir: %w", err)
	}
	data, err := json.MarshalIndent(a.counters, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal backlog id state: %w", err)
	}
	tmp := a.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("write backlog id state: %w", err)
	}
	if err := os.Rename(tmp, a.path); err != nil {
		return fmt.Errorf("replace backlog id state: %w", err)
	}
	return nil
}

// DefaultIDStateFile returns the persistent daemon's backlog id counter file.
func DefaultIDStateFile() string {
	dir := os.Getenv("XDG_STATE_HOME")
	if dir == "" {
		if home, err := os.UserHomeDir(); err == nil {
			dir = filepath.Join(home, ".local", "state")
		}
	}
	if dir == "" {
		dir = os.TempDir()
	}
	return filepath.Join(dir, "ycc", "backlog-ids.json")
}
