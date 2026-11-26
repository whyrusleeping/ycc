package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/jobs"
)

// A direct (unsupervised) background job streams its capture to an output
// file that its start result and final report name.
func TestBackgroundBashOutputFileReadableWithReadWindow(t *testing.T) {
	jr := jobs.NewRegistry()
	t.Cleanup(func() { _ = jr.KillAll() })
	outDir := filepath.Join(t.TempDir(), "output")
	ws := &Workspace{Root: t.TempDir(), Jobs: jr, Emitter: event.NewEmitter(&captureRec{}, "coordinator"), OutputDir: outDir}
	reg := New()
	reg.Add(Editing(ws)...)
	started := dispatch(t, reg, "Bash", `{"command":"seq 1 30000 | sed 's/^/row /'","run_in_background":true}`)
	path := between(started.Content, "Its output is written to ", " while it runs")
	if started.IsError || filepath.Dir(path) != outDir {
		t.Fatalf("background Bash did not name an output file in %s: %q", outDir, started.Content)
	}
	report := dispatch(t, reg, "wait", `{"job_ids":["job_1"],"timeout_s":30}`)
	if !strings.Contains(report.Content, "[full output: "+path+": ") {
		t.Fatalf("report does not name the output file: %q", report.Content)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if digest := digestHex(data); !strings.Contains(report.Content, "sha256 "+digest) || !strings.Contains(report.Content, fmt.Sprintf("%d bytes/30000 lines", len(data))) {
		t.Fatalf("report capture stats do not match the file (sha256 %s, %d bytes): %q", digest, len(data), report.Content)
	}
	window := dispatch(t, reg, "Read", fmt.Sprintf(`{"file_path":%q,"offset":15000,"limit":2}`, path))
	if window.IsError || !strings.Contains(window.Content, "row 15000") || !strings.Contains(window.Content, "row 15001") || strings.Contains(window.Content, "row 15002") {
		t.Fatalf("Read window = %q", window.Content)
	}
}

// Files still being written count at their reservation and are never
// evicted; finished files are evicted oldest first to make room, and a new
// file that cannot fit beside the reservations is refused without evicting.
func TestOutputRetentionReservesActiveFiles(t *testing.T) {
	old := maxSessionOutputBytes
	maxSessionOutputBytes = 250
	t.Cleanup(func() { maxSessionOutputBytes = old })
	ws := &Workspace{OutputDir: filepath.Join(t.TempDir(), "output")}
	active, err := ws.createOutputFile(100)
	if err != nil {
		t.Fatal(err)
	}
	first, err := ws.saveOutput([]byte(strings.Repeat("b", 100)))
	if err != nil {
		t.Fatal(err)
	}
	second, err := ws.saveOutput([]byte(strings.Repeat("c", 100)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(first); !os.IsNotExist(err) {
		t.Fatalf("retention kept %s past the bound: %v", first, err)
	}
	if _, err := ws.saveOutput([]byte(strings.Repeat("d", 200))); err == nil || !strings.Contains(err.Error(), "still being written") {
		t.Fatalf("a capture that cannot fit beside the active reservation was not refused: %v", err)
	}
	for _, path := range []string{active.Name(), second} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("retention removed %s (active, or evicted for a refused capture): %v", path, err)
		}
	}
	if err := finishOutputFile(active); err != nil {
		t.Fatal(err)
	}
	if notice := ws.outputEvictionNotice(); !strings.Contains(notice, first) || strings.Contains(notice, second) {
		t.Fatalf("eviction notice = %q, want only %s", notice, first)
	}
}

// The output directory and its .ycc ancestors must not be symlinks, and
// eviction removes only files the store created.
func TestOutputDirRefusesSymlinksAndForeignFiles(t *testing.T) {
	root := t.TempDir()
	elsewhere := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".ycc"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, filepath.Join(root, ".ycc", "sessions")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(elsewhere, "s_1"), 0o700); err != nil {
		t.Fatal(err)
	}
	ws := &Workspace{OutputDir: filepath.Join(root, ".ycc", "sessions", "s_1", "output")}
	if _, err := ws.saveOutput([]byte("x")); err == nil || !strings.Contains(err.Error(), "symlinks are refused") {
		t.Fatalf("a symlinked sessions directory was followed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(elsewhere, "s_1", "output")); !os.IsNotExist(err) {
		t.Fatalf("an output directory was created through the symlink: %v", err)
	}

	old := maxSessionOutputBytes
	maxSessionOutputBytes = 10
	t.Cleanup(func() { maxSessionOutputBytes = old })
	ws = &Workspace{OutputDir: t.TempDir()}
	foreign := filepath.Join(ws.OutputDir, "notes.out")
	if err := os.WriteFile(foreign, make([]byte, 100), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ws.saveOutput([]byte("small")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(foreign); err != nil {
		t.Fatalf("eviction removed a file the store did not create: %v", err)
	}
}

// A job report rendered after its output file was evicted says so, even
// though the stored report still names the file as its full output.
func TestJobReportAfterEvictionSaysTheFileIsGone(t *testing.T) {
	old := maxSessionOutputBytes
	maxSessionOutputBytes = maxCapturedCommandBytes + 50_000
	t.Cleanup(func() { maxSessionOutputBytes = old })
	reg, _, _ := jobsReg(t)
	started := dispatch(t, reg, "Bash", `{"command":"head -c 100000 /dev/zero | tr '\\0' x","run_in_background":true}`)
	path := between(started.Content, "Its output is written to ", " while it runs")
	if path == "" {
		t.Fatalf("no output file: %q", started.Content)
	}
	dispatch(t, reg, "wait", `{"job_ids":["job_1"],"timeout_s":30}`)
	later := dispatch(t, reg, "Bash", `{"command":"head -c 4200000 /dev/zero | tr '\\0' y"}`)
	if _, err := os.Stat(path); !os.IsNotExist(err) || !strings.Contains(later.Content, path) {
		t.Fatalf("the later capture did not evict and report %s: %v %q", path, err, later.Content)
	}
	for _, call := range [][2]string{{"job_output", `{"job_id":"job_1"}`}, {"wait", `{"job_ids":["job_1"],"timeout_s":10}`}} {
		report := dispatch(t, reg, call[0], call[1])
		if !strings.Contains(report.Content, "[output file "+path+" no longer exists") {
			t.Fatalf("%s presents the evicted file as available: %q", call[0], report.Content)
		}
	}
}
