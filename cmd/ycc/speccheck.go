package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	cli "github.com/urfave/cli/v3"

	"github.com/whyrusleeping/ycc/internal/docs"
	"github.com/whyrusleeping/ycc/internal/specdoctor"
)

// specCheckCommand is the daemon-free deterministic spec/code drift check
// (spec §6.4). It resolves the project's docs set (spec entry point + configured
// `doc_globs`), runs internal/specdoctor over it, prints the markdown stale-
// reference report, and exits non-zero exactly when stale references are found —
// so it doubles as a pre-commit / CI gate. It is the same reference pre-pass the
// spec-doctor pm preset drives (via Bash) as phase 1.
func (a *app) specCheckCommand() *cli.Command {
	return &cli.Command{
		Name:  "spec-check",
		Usage: "deterministically check the design docs for stale references (CI gate)",
		Description: "Extracts the file paths, package directories, and code symbols the project's\n" +
			"design docs mention (spec entry point + configured doc_globs) and reports any that\n" +
			"no longer exist in the repository. Zero false positives (ambiguous spans skipped).\n\n" +
			"Runs locally against the workspace; no daemon needed. Exits 0 when every reference\n" +
			"resolves (or there are no docs to check), and 1 when stale references are found, so\n" +
			"it works as a pre-commit / CI gate.",
		Action: func(ctx context.Context, cmd *cli.Command) error {
			stale, err := runSpecCheck(a.workspace, os.Stdout)
			if err != nil {
				return err
			}
			if stale {
				return cli.Exit("", 1)
			}
			return nil
		},
	}
}

// runSpecCheck is the testable core of `ycc spec-check`: it resolves the docs set
// under workspace, runs the deterministic reference check, writes the markdown
// report to out, and reports whether any stale references were found. A workspace
// with no design docs is not a failure — it writes a note and returns stale=false.
func runSpecCheck(workspace string, out io.Writer) (bool, error) {
	ws, err := filepath.Abs(workspace)
	if err != nil {
		ws = workspace
	}
	store := docs.NewStore(ws)
	files, err := store.DocFiles()
	if err != nil {
		return false, fmt.Errorf("resolving docs set: %w", err)
	}
	var docFiles []specdoctor.DocFile
	for _, abs := range files {
		data, err := os.ReadFile(abs)
		if err != nil {
			continue
		}
		rel, err := filepath.Rel(ws, abs)
		if err != nil {
			rel = abs
		}
		docFiles = append(docFiles, specdoctor.DocFile{Path: filepath.ToSlash(rel), Content: string(data)})
	}
	if len(docFiles) == 0 {
		fmt.Fprintln(out, "spec-check: no design docs found (no spec entry point and no doc_globs matches).")
		return false, nil
	}
	rep := specdoctor.Check(ws, docFiles)
	fmt.Fprint(out, rep.Markdown())
	return rep.Stale(), nil
}
