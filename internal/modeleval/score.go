package modeleval

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/whyrusleeping/ycc/internal/event"
)

type fileState struct {
	Mode   fs.FileMode
	Size   int64
	Digest [sha256.Size]byte
}

type treeSnapshot map[string]fileState

type gitSnapshot struct {
	Head       string
	IndexTree  string
	StagedDiff string
	Status     map[string]string
}

func snapshotTree(root string) (treeSnapshot, error) {
	out := treeSnapshot{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if rel == ".git" && entry.IsDir() {
			return filepath.SkipDir
		}
		if rel == "." {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		state := fileState{Mode: info.Mode(), Size: info.Size()}
		if entry.IsDir() {
			// Directory inode sizes vary by filesystem and are not fixture data.
			state.Size = 0
		}
		h := sha256.New()
		if info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			_, _ = io.WriteString(h, target)
		} else if info.Mode().IsRegular() {
			f, err := os.Open(path)
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(h, f)
			closeErr := f.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
		} else {
			_, _ = io.WriteString(h, info.Mode().String())
		}
		copy(state.Digest[:], h.Sum(nil))
		out[filepath.ToSlash(rel)] = state
		return nil
	})
	return out, err
}

func (s treeSnapshot) writeHash(h hash.Hash) {
	names := make([]string, 0, len(s))
	for name := range s {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		state := s[name]
		fmt.Fprintf(h, "%s\x00%s\x00%d\x00%x\x00", name, state.Mode, state.Size, state.Digest)
	}
}

func unintendedMutations(before treeSnapshot, root string, allowed map[string]string) []string {
	after, err := snapshotTree(root)
	if err != nil {
		return []string{"<snapshot failed: " + err.Error() + ">"}
	}
	return changedPaths(before, after, allowed)
}

func changedPaths(before, after treeSnapshot, allowed map[string]string) []string {
	allowedPaths := make(map[string]bool, len(allowed))
	for name := range allowed {
		allowedPaths[filepath.ToSlash(filepath.Clean(name))] = true
	}
	all := make(map[string]bool, len(before)+len(after))
	for name := range before {
		all[name] = true
	}
	for name := range after {
		all[name] = true
	}
	var changed []string
	for name := range all {
		beforeState, existedBefore := before[name]
		afterState, existsAfter := after[name]
		if beforeState == afterState {
			continue
		}
		if !allowedPaths[name] {
			changed = append(changed, name)
			continue
		}
		// Changing content is the allowed outcome, but changing an existing
		// target's file type or permissions is still an unintended mutation.
		if existedBefore && existsAfter && beforeState.Mode != afterState.Mode {
			changed = append(changed, name+" (mode changed)")
		}
	}
	sort.Strings(changed)
	return changed
}

func snapshotGit(root string) (*gitSnapshot, error) {
	if info, err := os.Stat(filepath.Join(root, ".git")); err != nil || !info.IsDir() {
		if err == nil {
			err = fmt.Errorf(".git is not a directory")
		}
		return nil, err
	}
	run := func(args ...string) (string, error) {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		out, err := cmd.Output()
		if err != nil {
			return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
		}
		return string(out), nil
	}
	head, err := run("rev-parse", "HEAD")
	if err != nil {
		return nil, err
	}
	indexTree, err := run("write-tree")
	if err != nil {
		return nil, err
	}
	staged, err := run("diff", "--cached", "--binary", "--no-ext-diff")
	if err != nil {
		return nil, err
	}
	statusRaw, err := run("status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return nil, err
	}
	status := map[string]string{}
	entries := strings.Split(statusRaw, "\x00")
	for i := 0; i < len(entries); i++ {
		entry := entries[i]
		if entry == "" {
			continue
		}
		if len(entry) < 4 || entry[2] != ' ' {
			return nil, fmt.Errorf("unexpected git status entry %q", entry)
		}
		status[filepath.ToSlash(entry[3:])] = entry[:2]
		// Porcelain emits a second NUL-delimited path for renames/copies.
		if (entry[0] == 'R' || entry[0] == 'C') && i+1 < len(entries) {
			i++
			status[filepath.ToSlash(entries[i])] = entry[:2]
		}
	}
	return &gitSnapshot{
		Head: strings.TrimSpace(head), IndexTree: strings.TrimSpace(indexTree),
		StagedDiff: staged, Status: status,
	}, nil
}

func unintendedGitMutations(before *gitSnapshot, root string, allowed map[string]string) []string {
	if before == nil {
		return nil
	}
	after, err := snapshotGit(root)
	if err != nil {
		return []string{".git (repository state unavailable: " + err.Error() + ")"}
	}
	var problems []string
	if after.Head != before.Head {
		problems = append(problems, ".git/HEAD (commit changed)")
	}
	if after.IndexTree != before.IndexTree || after.StagedDiff != before.StagedDiff {
		problems = append(problems, ".git/index (staged state changed)")
	}
	allowedPaths := make(map[string]bool, len(allowed))
	for name := range allowed {
		allowedPaths[filepath.ToSlash(filepath.Clean(name))] = true
	}
	all := map[string]bool{}
	for name := range before.Status {
		all[name] = true
	}
	for name := range after.Status {
		all[name] = true
	}
	for name := range all {
		if before.Status[name] == after.Status[name] {
			continue
		}
		// An expected tracked file may become worktree-modified, but it may not
		// be staged, deleted, renamed, or otherwise change index status.
		if allowedPaths[name] && before.Status[name] == "" && after.Status[name] == " M" {
			continue
		}
		problems = append(problems, name+" (git status changed from "+fmt.Sprintf("%q to %q", before.Status[name], after.Status[name])+")")
	}
	sort.Strings(problems)
	return problems
}

func hasSuccessfulToolByActor(events []RunEvent, name, actor string) bool {
	for _, ev := range events {
		if ev.Type == event.ToolResult && ev.Actor == actor && fmt.Sprint(ev.Data["name"]) == name && !asBool(ev.Data["error"]) {
			return true
		}
	}
	return false
}

func hasTool(events []RunEvent, name string, wantError bool) bool {
	for _, ev := range events {
		if ev.Type == event.ToolResult && fmt.Sprint(ev.Data["name"]) == name && asBool(ev.Data["error"]) == wantError {
			return true
		}
	}
	return false
}

func countSuccessfulTool(events []RunEvent, name string) int {
	count := 0
	for _, ev := range events {
		if ev.Type == event.ToolResult && fmt.Sprint(ev.Data["name"]) == name && !asBool(ev.Data["error"]) {
			count++
		}
	}
	return count
}

func hasToolAfterError(events []RunEvent, name string) bool {
	failed := false
	for _, ev := range events {
		if ev.Type != event.ToolResult || fmt.Sprint(ev.Data["name"]) != name {
			continue
		}
		if asBool(ev.Data["error"]) {
			failed = true
		} else if failed {
			return true
		}
	}
	return false
}

func toolArgs(ev RunEvent) (map[string]any, bool) {
	if ev.Type != event.ToolCall {
		return nil, false
	}
	raw, ok := ev.Data["args"].(string)
	if !ok {
		return nil, false
	}
	var args map[string]any
	if json.Unmarshal([]byte(raw), &args) != nil {
		return nil, false
	}
	return args, true
}

func hasScopedSymbolSearch(events []RunEvent) bool {
	qualified := map[string]bool{}
	for _, ev := range events {
		name, _ := ev.Data["name"].(string)
		if ev.Type == event.ToolCall && name == "Search" {
			args, ok := toolArgs(ev)
			path, _ := args["path"].(string)
			if ok && (path == "pkg" || path == "./pkg" || path == "pkg/") {
				qualified[fmt.Sprint(ev.Data["id"])] = true
			}
		} else if ev.Type == event.ToolCall && name == "Bash" {
			args, ok := toolArgs(ev)
			command, _ := args["command"].(string)
			if !ok || !strings.Contains(command, "rg") || !strings.Contains(command, "ComputeInvoice") {
				continue
			}
			// Accept common rg path operands and pkg-only glob forms without
			// requiring one provider-specific shell spelling.
			for _, field := range strings.Fields(command) {
				field = strings.Trim(field, "'\";,()")
				if field == "pkg" || field == "./pkg" || strings.HasPrefix(field, "pkg/") || strings.HasPrefix(field, "./pkg/") {
					qualified[fmt.Sprint(ev.Data["id"])] = true
					break
				}
			}
		} else if ev.Type == event.ToolResult && qualified[fmt.Sprint(ev.Data["id"])] && !asBool(ev.Data["error"]) {
			result, _ := ev.Data["result"].(string)
			if strings.Contains(result, "billing.go") && strings.Contains(result, "ComputeInvoice") && !strings.Contains(result, "[exit:") {
				return true
			}
		}
	}
	return false
}

var scopedLineReport = regexp.MustCompile(`(?i)pkg/billing\.go(?:(?::|[^\n]*\bline\s+)3\b)`)

func reportsScopedDefinition(report string) bool {
	return strings.Contains(report, "ComputeInvoice") && scopedLineReport.MatchString(report)
}

func hasAtomicMultiHunk(events []RunEvent) bool {
	calls := map[string]RunEvent{}
	for _, ev := range events {
		if ev.Type == event.ToolCall && fmt.Sprint(ev.Data["name"]) == "atomic_edit" {
			calls[fmt.Sprint(ev.Data["id"])] = ev
		}
		if ev.Type != event.ToolResult || fmt.Sprint(ev.Data["name"]) != "atomic_edit" || asBool(ev.Data["error"]) {
			continue
		}
		args, ok := toolArgs(calls[fmt.Sprint(ev.Data["id"])])
		if !ok {
			continue
		}
		if replacements, ok := args["replacements"].([]any); ok && len(replacements) >= 2 {
			return true
		}
	}
	return false
}

func hasFailingThenPassingCheck(events []RunEvent) bool {
	commands := map[string]bool{}
	failedAt := -1
	for i, ev := range events {
		if ev.Type == event.ToolCall && fmt.Sprint(ev.Data["name"]) == "Bash" {
			args, ok := toolArgs(ev)
			command, _ := args["command"].(string)
			if ok && strings.Contains(command, "./check.sh") {
				commands[fmt.Sprint(ev.Data["id"])] = true
			}
			continue
		}
		if ev.Type != event.ToolResult || fmt.Sprint(ev.Data["name"]) != "Bash" || !commands[fmt.Sprint(ev.Data["id"])] {
			continue
		}
		result, _ := ev.Data["result"].(string)
		isFailure := asBool(ev.Data["error"]) || strings.Contains(result, "[exit:") || strings.Contains(result, "[command timed out")
		if failedAt < 0 && isFailure && strings.Contains(result, "ERROR expected MODE=stable") {
			failedAt = i
		} else if failedAt >= 0 && i > failedAt && !isFailure && strings.Contains(result, "PASS") {
			return true
		}
	}
	return false
}

func usesInvestigationEvidence(events []RunEvent) bool {
	investigatedAt := map[string]int{}
	mutations := map[string]bool{}
	for i, ev := range events {
		actor := ev.Actor
		if ev.Type == event.ToolResult && fmt.Sprint(ev.Data["name"]) == "investigate" && !asBool(ev.Data["error"]) {
			result, _ := ev.Data["result"].(string)
			if strings.Contains(result, "channel=cobalt") && strings.Contains(result, "source=evidence.txt") {
				investigatedAt[actor] = i
			}
			continue
		}
		key := actor + "\x00" + fmt.Sprint(ev.Data["id"])
		if ev.Type == event.ToolResult && mutations[key] && !asBool(ev.Data["error"]) {
			result, _ := ev.Data["result"].(string)
			if !strings.Contains(result, "[exit:") && !strings.Contains(result, "[command timed out") {
				return true
			}
			continue
		}
		investigation, found := investigatedAt[actor]
		if ev.Type != event.ToolCall || !found || investigation >= i {
			continue
		}
		name, _ := ev.Data["name"].(string)
		if name != "Edit" && name != "Write" && name != "atomic_edit" && name != "Bash" {
			continue
		}
		raw, _ := ev.Data["args"].(string)
		if strings.Contains(raw, "release.conf") && strings.Contains(raw, "cobalt") {
			mutations[key] = true
		}
	}
	return false
}
