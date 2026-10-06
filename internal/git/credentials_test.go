package git

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/whyrusleeping/ycc/internal/credenv"
)

func TestCommitHookCannotInheritDaemonCredentials(t *testing.T) {
	t.Setenv("YCC_TOKEN", "daemon-secret")
	t.Setenv("REVIEW_PROBE_PROVIDER", "provider-secret")
	credenv.Register("REVIEW_PROBE_PROVIDER")
	r, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	seedTracked(t, r, map[string]string{"task.txt": "old\n"})
	baseline, err := r.CaptureBaseline()
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(r.Dir, "task.txt"), "new\n")
	changes, err := r.Changes(baseline)
	if err != nil {
		t.Fatal(err)
	}
	hook := filepath.Join(r.Dir, ".git", "hooks", "pre-commit")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nprintf 'credentials=%s,%s\\n' \"${YCC_TOKEN-unset}\" \"${REVIEW_PROBE_PROVIDER-unset}\" >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err = r.Commit(changes, "rejected")
	if err == nil || !strings.Contains(err.Error(), "pre-commit hook rejected commit") || !strings.Contains(err.Error(), "credentials=unset,unset") {
		t.Fatalf("hook did not run with scrubbed credentials: %v", err)
	}
	if strings.Contains(err.Error(), "daemon-secret") || strings.Contains(err.Error(), "provider-secret") {
		t.Fatalf("hook inherited daemon credentials: %v", err)
	}
}
