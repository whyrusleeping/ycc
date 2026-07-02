package orchestrator

import (
	"context"
	"os"
	"path/filepath"

	"github.com/whyrusleeping/gollama"
	"github.com/whyrusleeping/ycc/internal/specdoctor"
	"github.com/whyrusleeping/ycc/internal/tools"
)

// specCheck runs the deterministic spec-doctor pre-pass (task 0100): it reads the
// project's docs set (spec entry point + configured doc_globs) and reports which
// file paths, package directories, and code symbols they mention that no longer
// exist in the repo. The result is a markdown report of stale references — the
// mechanically-detectable half of spec/code drift, with zero false positives —
// which seeds and grounds the LLM comparison pass of the spec-doctor flow.
func specCheck(d *Deps) *gollama.Tool {
	return &gollama.Tool{
		Name: "spec_check",
		Description: "Deterministically check the design docs for stale references: extract the file paths, package " +
			"directories, and code symbols the docs mention and report any that no longer exist in the repository. " +
			"Zero false positives (ambiguous spans are skipped). Run this FIRST in a spec-doctor pass — its stale-" +
			"reference report grounds the deeper drift/coverage comparison against the code. Takes no parameters.",
		Params: tools.Obj(map[string]any{}),
		Call: func(ctx context.Context, params any) (*gollama.ToolResult, error) {
			files, err := d.Docs.DocFiles()
			if err != nil {
				return tools.ErrResult("spec_check: %v", err), nil
			}
			var docFiles []specdoctor.DocFile
			for _, abs := range files {
				data, err := os.ReadFile(abs)
				if err != nil {
					continue
				}
				rel, err := filepath.Rel(d.Workspace, abs)
				if err != nil {
					rel = abs
				}
				docFiles = append(docFiles, specdoctor.DocFile{Path: filepath.ToSlash(rel), Content: string(data)})
			}
			if len(docFiles) == 0 {
				return tools.OkResult("spec_check: no design docs found (no spec entry point and no doc_globs matches)."), nil
			}
			rep := specdoctor.Check(d.Workspace, docFiles)
			return tools.OkResult(rep.Markdown()), nil
		},
	}
}
