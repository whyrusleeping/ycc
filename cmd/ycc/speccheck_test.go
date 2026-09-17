package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// runSpecCheck resolves the docs set and runs the deterministic reference check:
// an accurate spec's references resolve (stale=false); a reference to a removed
// repo path is flagged (stale=true) and named in the report.
func TestRunSpecCheckStale(t *testing.T) {
	ws := t.TempDir()
	if err := os.MkdirAll(filepath.Join(ws, "internal", "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "internal", "docs", "x.go"), []byte("package docs\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	spec := "# Spec\n\nThe `internal/docs` package exists but `internal/removed` was deleted.\n"
	if err := os.WriteFile(filepath.Join(ws, "spec.md"), []byte(spec), 0o644); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	stale, err := runSpecCheck(ws, &out)
	if err != nil {
		t.Fatalf("runSpecCheck: %v", err)
	}
	if !stale {
		t.Fatalf("expected stale=true for a removed package reference:\n%s", out.String())
	}
	if !bytes.Contains(out.Bytes(), []byte("internal/removed")) {
		t.Fatalf("report should flag the removed package:\n%s", out.String())
	}
}

func TestRunSpecCheckConfiguredDocs(t *testing.T) {
	ws := t.TempDir()
	write := func(rel, body string) {
		t.Helper()
		path := filepath.Join(ws, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(".ycc/config.toml", "doc_globs = [\"docs/design/*.md\"]\n")
	write("internal/docs/x.go", "package docs\n")
	write("spec.md", "# Spec\n")
	write("docs/design/architecture.md", "# Architecture\n\nThe `internal/removed` package no longer exists.\n")
	write("docs/reports/advisory.md", "The `also/removed` package is advisory.\n")

	var out bytes.Buffer
	stale, err := runSpecCheck(ws, &out)
	if err != nil {
		t.Fatalf("runSpecCheck: %v", err)
	}
	if !stale {
		t.Fatalf("configured design doc should be checked:\n%s", out.String())
	}
	if !bytes.Contains(out.Bytes(), []byte("across 2 docs")) {
		t.Fatalf("report should include the entry point and configured design doc:\n%s", out.String())
	}
	if !bytes.Contains(out.Bytes(), []byte("docs/design/architecture.md")) {
		t.Fatalf("report should identify the configured design doc:\n%s", out.String())
	}
	if bytes.Contains(out.Bytes(), []byte("also/removed")) {
		t.Fatalf("report should exclude non-configured advisory docs:\n%s", out.String())
	}
}

// A clean spec (every reference resolves) reports stale=false.
func TestRunSpecCheckClean(t *testing.T) {
	ws := t.TempDir()
	if err := os.MkdirAll(filepath.Join(ws, "internal", "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "internal", "docs", "x.go"), []byte("package docs\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	spec := "# Spec\n\nThe `internal/docs` package exists.\n"
	if err := os.WriteFile(filepath.Join(ws, "spec.md"), []byte(spec), 0o644); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	stale, err := runSpecCheck(ws, &out)
	if err != nil {
		t.Fatalf("runSpecCheck: %v", err)
	}
	if stale {
		t.Fatalf("expected stale=false for an accurate spec:\n%s", out.String())
	}
}

// A workspace with no design docs is not a failure: it reports a note and
// stale=false so a CI gate passes.
func TestRunSpecCheckNoDocs(t *testing.T) {
	ws := t.TempDir()
	var out bytes.Buffer
	stale, err := runSpecCheck(ws, &out)
	if err != nil {
		t.Fatalf("runSpecCheck: %v", err)
	}
	if stale {
		t.Fatalf("no-docs workspace should not be stale:\n%s", out.String())
	}
	if !bytes.Contains(out.Bytes(), []byte("no design docs")) {
		t.Fatalf("expected a no-docs note:\n%s", out.String())
	}
}
