package web

import (
	"os/exec"
	"path/filepath"
	"testing"
)

// TestAppJSNode runs the web client's pure-helper test suite (app_test.js) under
// Node when it is available, so the envelope/frame-parser/feed-fold logic is
// exercised in CI. It t.Skip()s when node is not installed, keeping `go test`
// hermetic and node-free by default.
func TestAppJSNode(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed; skipping web client JS tests")
	}
	script, err := filepath.Abs("app_test.js")
	if err != nil {
		t.Fatalf("resolve app_test.js: %v", err)
	}
	cmd := exec.Command(node, script)
	cmd.Dir = "." // require("./dist/app.js") resolves relative to this package dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("node app_test.js failed: %v\n%s", err, out)
	}
	t.Logf("node app_test.js:\n%s", out)
}
