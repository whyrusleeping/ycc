package orchestrator

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/whyrusleeping/gollama"
	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/git"
)

const (
	maxReviewDiffBytes  = 64 * 1024
	reviewDiffNudge     = "Orient yourself: the scoped change under review is preloaded below."
	reviewDiffCallID    = "preload_diff"
	reviewDiffTruncated = "\n…[review diff truncated: inspect the listed paths in the workspace for the rest]"
)

type reviewDiffBuild struct {
	History    []gollama.Message
	Call       gollama.ToolCall
	Result     string
	Duration   int64
	SnapshotID string
	BaselineID string
	Paths      []string
	Command    string
	Tree       string
	DiffBytes  int
	DiffSHA256 string
	Err        error
}

// buildReviewDiffHistory captures a non-mutating, explicitly scoped snapshot
// and presents it as a genuine-looking Bash exchange. The snapshot identifier
// lets reports and reviewer evidence name exactly what was inspected.
func buildReviewDiffHistory(repo *git.Repo, baseline *git.Baseline) reviewDiffBuild {
	if repo == nil {
		return reviewDiffBuild{Err: fmt.Errorf("git repository is not available")}
	}
	if baseline == nil {
		return reviewDiffBuild{Err: fmt.Errorf("session git baseline is unavailable; start a new session before review")}
	}
	start := time.Now()
	changes, err := repo.Changes(baseline)
	duration := time.Since(start).Milliseconds()
	if err != nil {
		return reviewDiffBuild{Err: err}
	}
	scope := boundedPathList(changes.Paths, 4096)
	header := fmt.Sprintf("CHANGESET SNAPSHOT %s (baseline %s)\nSCOPE: %s (%d paths)\nDIFF: %d bytes, sha256 %s\n\n",
		changes.ID, changes.BaselineID, scope, len(changes.Paths), len(changes.Diff), sha256Text(changes.Diff))
	// Tree is already the immutable task-scoped snapshot built on BaseCommit, so
	// diffing the two trees needs no potentially unbounded path argument list.
	command := "git diff --binary --no-color --no-ext-diff " + changes.BaseCommit + " " + changes.Tree + " --"
	built := reviewDiffBuild{
		Duration: duration, SnapshotID: changes.ID, BaselineID: changes.BaselineID,
		Paths: append([]string(nil), changes.Paths...), Command: command, Tree: changes.Tree,
		DiffBytes: len(changes.Diff), DiffSHA256: sha256Text(changes.Diff),
	}
	if changes.Diff == "" {
		return built
	}
	diff := strings.ToValidUTF8(changes.Diff, "�")
	if len(header)+len(diff) > maxReviewDiffBytes {
		budget := maxReviewDiffBytes - len(header) - len(reviewDiffTruncated)
		if budget < 0 {
			budget = 0
		}
		diff = validUTF8Prefix(diff, budget) + reviewDiffTruncated
	}
	args, _ := json.Marshal(map[string]string{"command": command})
	call := gollama.ToolCall{
		ID:   reviewDiffCallID,
		Type: "function",
		Function: gollama.ToolCallFunction{
			Name:      "Bash",
			Arguments: string(args),
		},
	}
	result := header + diff
	built.History = []gollama.Message{
		{Role: "user", Content: reviewDiffNudge},
		{Role: "assistant", ToolCalls: []gollama.ToolCall{call}},
		{Role: "tool", ToolCallID: call.ID, Content: result},
	}
	built.Call = call
	built.Result = result
	return built
}

func compactReviewDiffEvidence(built reviewDiffBuild) string {
	return fmt.Sprintf("CURRENT SCOPED CHANGESET %s (baseline %s)\nManifest: %d paths: %s\nDiff: %d bytes, sha256 %s\nExact retrieval: %s",
		built.SnapshotID, built.BaselineID, len(built.Paths), boundedPathList(built.Paths, 4096), built.DiffBytes, built.DiffSHA256, built.Command)
}

func boundedPathList(paths []string, budget int) string {
	if len(paths) == 0 {
		return "(none)"
	}
	var b strings.Builder
	for i, path := range paths {
		path = strings.ToValidUTF8(path, "�")
		sep := ""
		if i > 0 {
			sep = ", "
		}
		if b.Len()+len(sep)+len(path) > budget {
			fmt.Fprintf(&b, " …[%d more paths]", len(paths)-i)
			break
		}
		b.WriteString(sep)
		b.WriteString(path)
	}
	return b.String()
}

func sha256Text(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

func changedSinceReviewEvidence(repo *git.Repo, previous reviewDiffBuild, current reviewDiffBuild) string {
	if previous.Tree == "" || previous.Tree == current.Tree {
		return fmt.Sprintf("CHANGED SINCE PRIOR REVIEW %s -> %s\nManifest: (no tree changes)", previous.SnapshotID, current.SnapshotID)
	}
	paths := append(append([]string(nil), previous.Paths...), current.Paths...)
	sort.Strings(paths)
	paths = uniqueStrings(paths)
	args := []string{"diff", "--name-status", "--no-renames", previous.Tree, current.Tree, "--"}
	out, err := exec.Command("git", append([]string{"-C", repo.Dir}, args...)...).CombinedOutput()
	manifest := strings.ToValidUTF8(strings.TrimSpace(string(out)), "�")
	if err != nil {
		manifest = "(delta manifest unavailable: " + err.Error() + ")"
	}
	const maxDeltaBytes = 16 * 1024
	if len(manifest) > maxDeltaBytes {
		manifest = validUTF8Prefix(manifest, maxDeltaBytes-80) + "\n…[delta manifest truncated; use exact retrieval below]"
	}
	command := "git diff --no-color --no-ext-diff " + previous.Tree + " " + current.Tree + " --"
	return fmt.Sprintf("CHANGED SINCE PRIOR REVIEW %s -> %s\nCandidate scope (%d paths): %s\nDelta manifest (name-status):\n%s\nExact delta retrieval: %s",
		previous.SnapshotID, current.SnapshotID, len(paths), boundedPathList(paths, 4096), manifest, command)
}

func uniqueStrings(in []string) []string {
	out := in[:0]
	for _, s := range in {
		if len(out) == 0 || out[len(out)-1] != s {
			out = append(out, s)
		}
	}
	return out
}

func boundedDiffExcerpt(diff string, budget int) string {
	if strings.TrimSpace(diff) == "" {
		return "(no task-owned changes in the workspace)"
	}
	sourceBytes := len(diff)
	diff = strings.ToValidUTF8(diff, "�")
	if len(diff) <= budget {
		return diff
	}
	marker := fmt.Sprintf("\n…[middle omitted from %d-byte source diff; inspect with the exact git command]…\n", sourceBytes)
	available := budget - len(marker)
	if available < 0 {
		available = 0
	}
	headBudget := available * 2 / 3
	tailBudget := available - headBudget
	return validUTF8Prefix(diff, headBudget) + marker + validUTF8Suffix(diff, tailBudget)
}

func validUTF8Suffix(s string, n int) string {
	if n >= len(s) {
		return s
	}
	start := len(s) - n
	for start < len(s) && !utf8.RuneStart(s[start]) {
		start++
	}
	return s[start:]
}

// emitSyntheticReviewDiff records the exchange exactly as installed in a
// reviewer's history. In particular, it emits no user_input event: replay treats
// every such event as coordinator-authored conversation.
func emitSyntheticReviewDiff(em *event.Emitter, spec AgentSpec, actor string, built reviewDiffBuild) {
	if len(built.History) == 0 || em == nil {
		return
	}
	em = em.With(actor)
	em.Emit(event.ModelTurn, map[string]any{
		"text": "", "tool_calls": 1, "model_name": spec.Name,
		"backend": spec.Backend, "model_id": spec.Model, "synthetic": true,
		"changeset_id": built.SnapshotID,
	})
	em.Emit(event.ToolCall, map[string]any{
		"name": "Bash", "args": built.Call.Function.Arguments,
		"id": built.Call.ID, "synthetic": true, "changeset_id": built.SnapshotID,
	})
	em.Emit(event.ToolResult, map[string]any{
		"name": "Bash", "result": built.Result, "error": false,
		"id": built.Call.ID, "duration_ms": built.Duration, "synthetic": true,
		"changeset_id": built.SnapshotID,
	})
}
