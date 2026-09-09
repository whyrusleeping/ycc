package modeleval

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Scenario is a disposable repository task and its fact-based oracle.
type Scenario struct {
	Name        string
	Version     string
	Prompt      string
	Resume      string
	Expected    map[string]string
	Setup       func(string) error
	CheckEvents func([]RunEvent, string, string) []string
}

// Scenarios returns the small built-in behavioral suite. Fixtures deliberately
// describe outcomes rather than a preferred command syntax, except where the
// behavior under evaluation is a particular tool boundary.
func Scenarios() []Scenario {
	return []Scenario{
		{
			Name:    "scoped-symbol-search",
			Version: "2",
			Prompt:  "Find the definition of ComputeInvoice under pkg only. Report its repository-relative file and line. Do not modify anything.",
			Setup: func(root string) error {
				return writeFiles(root, map[string]string{
					"pkg/billing.go":  "package pkg\n\nfunc ComputeInvoice() int { return 42 }\n",
					"vendor/noise.go": "package vendor\n\nfunc ComputeInvoice() int { return 0 }\n",
					"README.md":       "fixture\n",
				})
			},
			CheckEvents: func(events []RunEvent, report, strategy string) []string {
				var problems []string
				if !hasScopedSymbolSearch(events) {
					problems = append(problems, "no pkg-scoped Search or rg call was observed")
				}
				if !reportsScopedDefinition(report) {
					problems = append(problems, "final report did not identify ComputeInvoice at pkg/billing.go line 3")
				}
				return problems
			},
		},
		{
			Name:     "failed-edit-recovery",
			Version:  "1",
			Prompt:   "Change timeout to 60 in settings.conf. Begin with the operator's stale exact replacement (`timeout = 30` -> `timeout = 60`) using Edit, then recover from the resulting failure by inspecting current repository state. Preserve the comment.",
			Expected: map[string]string{"settings.conf": "# service timeout\ntimeout = 60\n"},
			Setup: func(root string) error {
				return writeFiles(root, map[string]string{"settings.conf": "# service timeout\ntimeout = 45\n"})
			},
			CheckEvents: func(events []RunEvent, report, strategy string) []string {
				if !hasTool(events, "Edit", true) {
					return []string{"no failed Edit was observed at the real tool boundary"}
				}
				if !hasToolAfterError(events, "Edit") {
					return []string{"no successful Edit recovery followed the failed Edit"}
				}
				return nil
			},
		},
		{
			Name:     "atomic-multi-hunk",
			Version:  "2",
			Prompt:   "In flags.conf, change both alpha and beta from false to true as one atomic multi-hunk mutation. Use atomic_edit so validation of every old value happens before the file is replaced.",
			Expected: map[string]string{"flags.conf": "alpha=true\nmiddle=keep\nbeta=true\n"},
			Setup: func(root string) error {
				return writeFiles(root, map[string]string{"flags.conf": "alpha=false\nmiddle=keep\nbeta=false\n"})
			},
			CheckEvents: func(events []RunEvent, report, strategy string) []string {
				if countSuccessfulTool(events, "atomic_edit") != 1 || !hasAtomicMultiHunk(events) {
					return []string{"expected exactly one successful atomic_edit call containing multiple replacements"}
				}
				return nil
			},
		},
		{
			Name:     "long-failing-test-output",
			Version:  "2",
			Prompt:   "Run ./check.sh, diagnose its failure from the bounded command output, and make config.env pass. Re-run the check to verify it.",
			Expected: map[string]string{"config.env": "MODE=stable\n"},
			Setup: func(root string) error {
				script := `#!/bin/sh
mode=$(sed -n 's/^MODE=//p' config.env)
if [ "$mode" = stable ]; then
  echo PASS
  exit 0
fi
i=0
while [ "$i" -lt 3000 ]; do
  printf 'diagnostic-%04d: irrelevant padding to exercise bounded output projection xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx\n' "$i"
  i=$((i+1))
done
echo "ERROR expected MODE=stable, got MODE=$mode" >&2
exit 1
`
				if err := writeFiles(root, map[string]string{"config.env": "MODE=broken\n", "check.sh": script}); err != nil {
					return err
				}
				return os.Chmod(filepath.Join(root, "check.sh"), 0o755)
			},
			CheckEvents: func(events []RunEvent, report, strategy string) []string {
				if !hasFailingThenPassingCheck(events) {
					return []string{"./check.sh was not observed failing with the diagnostic and subsequently passing"}
				}
				return nil
			},
		},
		{
			Name:     "preserve-unrelated-dirty-work",
			Version:  "2",
			Prompt:   "Change Greeting in app.go from hello to welcome. Preserve all unrelated dirty work exactly.",
			Expected: map[string]string{"app.go": "package main\n\nconst Greeting = \"welcome\"\n"},
			Setup: func(root string) error {
				if err := writeFiles(root, map[string]string{
					"app.go":    "package main\n\nconst Greeting = \"hello\"\n",
					"notes.txt": "committed notes\n",
				}); err != nil {
					return err
				}
				if err := initGit(root); err != nil {
					return err
				}
				return os.WriteFile(filepath.Join(root, "notes.txt"), []byte("unrelated draft\n"), 0o644)
			},
		},
		{
			Name:     "interrupt-rollover-recovery",
			Version:  "2",
			Prompt:   "Read PLAN.md, then call handoff with a concise statement of the required state change. Do not mutate state.txt before handoff; execution will intentionally roll over at that boundary.",
			Resume:   "Execution was interrupted after the handoff. Recover from the replayed evidence, complete the requested state.txt change, verify it, and finish.",
			Expected: map[string]string{"state.txt": "state=ready\n"},
			Setup: func(root string) error {
				return writeFiles(root, map[string]string{
					"PLAN.md":   "Set state.txt to state=ready while preserving its one-line format.\n",
					"state.txt": "state=pending\n",
				})
			},
			CheckEvents: func(events []RunEvent, report, strategy string) []string {
				if !hasTool(events, "handoff", false) {
					return []string{"handoff boundary was not reached"}
				}
				return nil
			},
		},
		{
			Name:     "delegated-investigation-evidence",
			Version:  "2",
			Prompt:   "Determine the release channel from evidence.txt and update release.conf. If an investigate tool is available, delegate the investigation and use its returned evidence; otherwise investigate directly. Do not alter the evidence source.",
			Expected: map[string]string{"release.conf": "channel=cobalt\n"},
			Setup: func(root string) error {
				return writeFiles(root, map[string]string{
					"evidence.txt": "approved release channel: cobalt\n",
					"release.conf": "channel=pending\n",
				})
			},
			CheckEvents: func(events []RunEvent, report, strategy string) []string {
				if strategy != "direct" && !usesInvestigationEvidence(events) {
					return []string{"no actor used source-attributed investigate evidence in a subsequent mutation"}
				}
				return nil
			},
		},
	}
}

func writeFiles(root string, files map[string]string) error {
	for name, content := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func initGit(root string) error {
	commands := [][]string{
		{"git", "init", "-q"},
		{"git", "config", "user.email", "eval@example.invalid"},
		{"git", "config", "user.name", "Behavior Eval"},
		{"git", "add", "."},
		{"git", "commit", "-qm", "fixture"},
	}
	for _, argv := range commands {
		cmd := exec.Command(argv[0], argv[1:]...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("%s: %w: %s", strings.Join(argv, " "), err, out)
		}
	}
	return nil
}

func scenarioByName(name string) (Scenario, bool) {
	for _, scenario := range Scenarios() {
		if scenario.Name == name {
			return scenario, true
		}
	}
	return Scenario{}, false
}

func scenarioFingerprint(s Scenario, baseline treeSnapshot) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%s\x00%s\x00%s\x00", s.Name, s.Version, s.Prompt, s.Resume)
	baseline.writeHash(h)
	return fmt.Sprintf("sha256:%x", h.Sum(nil))
}
