package tools

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/whyrusleeping/ycc/internal/credenv"
	"github.com/whyrusleeping/ycc/internal/workspacelease"
)

// Shared-HEAD warnings. Sessions in one worktree share its HEAD, index and
// stash, so a shell that amends, resets, rebases or switches HEAD, or stashes,
// can hide or discard other live sessions' uncommitted work and rewrites the
// history their baselines build on. The shell's bracketing snapshots record
// HEAD and the stash of the shell's worktree and of any worktree the command
// targets with cd or git -C. The command has already run, so its result says
// what changed and names the sessions at risk instead of letting it pass
// silently. A snapshot cannot tell who made a change, so the warning reports
// what changed while the command ran rather than blaming it.

// maxWatchedTrees bounds the worktrees one command's snapshots watch.
const maxWatchedTrees = 4

// maxTargetDirs bounds the directories a command's text scan considers, and
// lookups bounds the git calls resolving them to worktrees.
const (
	maxTargetDirs   = 8
	maxTargetLookup = 2 * maxTargetDirs
)

// maxSharedHeadPeers bounds the sessions a shared-HEAD warning names.
const maxSharedHeadPeers = 8

// treeHead is the HEAD state of one worktree a shell command may rewrite,
// captured before the command.
type treeHead struct {
	top       string // worktree top as git prints it
	gitDir    string // its (per-worktree) git directory, holding HEAD
	launch    bool   // the shell's own worktree
	state     headState
	stashText bool // the command text may run git stash in this worktree
	stashSure bool // ... and nowhere else
}

// headState is the shared git state a shell can rewrite under other sessions.
type headState struct {
	ok     bool   // read successfully
	ref    string // HEAD's symbolic full name; "HEAD" when detached
	commit string // HEAD's commit; "" on an unborn branch
	stash  string // refs/stash's commit; "" without a stash
}

// watchHeads captures the HEAD state of the shell's worktree (top, gitDir;
// "" outside a git worktree) and of the worktrees command targets.
func (w *Workspace) watchHeads(top, gitDir, command string) []treeHead {
	ctx, cancel := context.WithTimeout(context.Background(), shellSnapshotTimeout)
	defer cancel()
	var heads []treeHead
	byDir := map[string]int{}
	if top != "" {
		heads = append(heads, treeHead{top: top, gitDir: gitDir, launch: true})
		byDir[w.Root] = 0
	}
	lookups := 0
	tree := func(dir string) int {
		if i, ok := byDir[dir]; ok {
			return i
		}
		byDir[dir] = -1
		if len(heads) >= maxWatchedTrees || lookups >= maxTargetLookup {
			return -1
		}
		lookups++
		out, err := gitOutput(ctx, dir, "rev-parse", "--show-toplevel", "--absolute-git-dir")
		lines := strings.Split(strings.TrimSpace(out), "\n")
		if err != nil || len(lines) != 2 {
			return -1
		}
		for i, h := range heads {
			if h.top == lines[0] {
				byDir[dir] = i
				return i
			}
		}
		heads = append(heads, treeHead{top: lines[0], gitDir: lines[1]})
		byDir[dir] = len(heads) - 1
		return len(heads) - 1
	}
	dirs, stashes, unresolved := gitTargets(w.Root, command)
	for _, dir := range dirs {
		tree(dir)
	}
	for _, candidates := range stashes {
		trees := map[int]bool{}
		for _, dir := range candidates {
			if i := tree(dir); i >= 0 {
				trees[i] = true
			}
		}
		for i := range trees {
			heads[i].stashText = true
			heads[i].stashSure = heads[i].stashSure || len(trees) == 1 && !unresolved
		}
	}
	for i := range heads {
		heads[i].state = readHead(ctx, heads[i].top, heads[i].gitDir)
	}
	return heads
}

// readHead reads a worktree's HEAD and stash.
func readHead(ctx context.Context, top, gitDir string) headState {
	data, err := os.ReadFile(filepath.Join(gitDir, "HEAD"))
	if err != nil {
		return headState{}
	}
	var st headState
	content := strings.TrimSpace(string(data))
	switch {
	case strings.HasPrefix(content, "ref: "):
		st.ref = strings.TrimPrefix(content, "ref: ")
	case isObjectName(content):
		st.ref, st.commit = "HEAD", content
	default:
		return headState{}
	}
	if st.ref == "refs/heads/.invalid" {
		// The reftable backend keeps HEAD in its ref store.
		ref, err := gitOutput(ctx, top, "symbolic-ref", "-q", "HEAD")
		var exit *exec.ExitError
		switch {
		case err == nil:
			st.ref = strings.TrimSpace(ref)
		case errors.As(err, &exit) && exit.ExitCode() == 1:
			st.ref = "HEAD"
			commit, err := gitOutput(ctx, top, "rev-parse", "-q", "--verify", "HEAD")
			if err != nil {
				return headState{}
			}
			st.commit = strings.TrimSpace(commit)
		default:
			return headState{}
		}
	}
	args := []string{"for-each-ref", "--format=%(refname) %(objectname)", "refs/stash"}
	if st.ref != "HEAD" {
		args = append(args, st.ref)
	}
	out, err := gitOutput(ctx, top, args...)
	if err != nil {
		return headState{}
	}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		name, commit, _ := strings.Cut(line, " ")
		switch name {
		case "refs/stash":
			st.stash = commit
		case st.ref:
			st.commit = commit
		}
	}
	st.ok = true
	return st
}

func isObjectName(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	return strings.Trim(s, "0123456789abcdef") == ""
}

func gitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	cmd.Env = credenv.Scrub(os.Environ())
	out, err := cmd.Output()
	return string(out), err
}

// sharedHeadNotices re-reads the HEAD state before captured and describes,
// for each worktree other live sessions share, a HEAD rewrite or switch or a
// stash change seen while the command ran, or a git stash in its text.
func (w *Workspace) sharedHeadNotices(before *shellSnapshot, scope string) string {
	ctx, cancel := context.WithTimeout(context.Background(), shellSnapshotTimeout)
	defer cancel()
	var notices []string
	for _, h := range before.heads {
		var changes []string
		stashChanged := false
		if after := readHead(ctx, h.top, h.gitDir); h.state.ok && after.ok {
			changes, stashChanged = headChanges(ctx, h.top, h.state, after)
		}
		ranStash := h.stashText && !stashChanged
		if len(changes) == 0 && !ranStash {
			continue
		}
		key, err := workspacelease.Canonical(h.top)
		if err != nil {
			continue
		}
		peers := w.Ownership.SharedHeadPeers(key, scope)
		if len(peers) == 0 {
			continue
		}
		if len(peers) > maxSharedHeadPeers {
			peers = append(append([]string{}, peers[:maxSharedHeadPeers]...), fmt.Sprintf("and %d more", len(peers)-maxSharedHeadPeers))
		}
		var b strings.Builder
		b.WriteString("[shared HEAD]")
		if len(changes) > 0 {
			b.WriteString(" While this command ran, " + strings.Join(changes, ", and ") + ".")
		}
		switch {
		case ranStash && h.stashSure:
			b.WriteString(" This command runs git stash.")
		case ranStash:
			b.WriteString(" This command may run git stash here (it changes directory, so where it runs is uncertain).")
		}
		where := "this worktree"
		if !h.launch {
			where = h.top
		}
		fmt.Fprintf(&b, " Other live sessions share the HEAD, index and stash of %s: %s. ", where, strings.Join(peers, "; "))
		b.WriteString("Stashing or rewriting a shared HEAD can hide, discard or misattribute their uncommitted work and rewrites the history they build on. " +
			"If this command did it, check with git status and git stash list that their changes are still in the tree (restore any you removed). " +
			"Commit through the commit tool rather than git, and use a workstream when you need history of your own.")
		notices = append(notices, b.String())
	}
	return strings.Join(notices, "\n")
}

// headChanges describes how HEAD and the stash changed from b to a in the
// worktree at top; a fast-forward of HEAD is no change.
func headChanges(ctx context.Context, top string, b, a headState) (changes []string, stashChanged bool) {
	switch {
	case b.ref != a.ref:
		changes = append(changes, fmt.Sprintf("HEAD moved from %s to %s", describeHead(b), describeHead(a)))
	case b.commit == a.commit || b.commit == "":
	case a.commit != "" && isAncestor(ctx, top, b.commit, a.commit):
	case b.ref == "HEAD":
		changes = append(changes, fmt.Sprintf("detached HEAD moved from %s to %s, which is not a fast-forward",
			shortCommit(b.commit), shortCommit(a.commit)))
	default:
		changes = append(changes, fmt.Sprintf("branch %s was rewritten (%s to %s is not a fast-forward: an amend, reset or rebase)",
			strings.TrimPrefix(b.ref, "refs/heads/"), shortCommit(b.commit), shortCommit(a.commit)))
	}
	if b.stash != a.stash {
		stashChanged = true
		changes = append(changes, "the stash changed")
	}
	return changes, stashChanged
}

// isAncestor reports whether commit old is an ancestor of new in the
// repository at top. An inconclusive check counts as an ancestor, so only a
// proven rewrite is reported.
func isAncestor(ctx context.Context, top, old, new string) bool {
	cmd := exec.CommandContext(ctx, "git", "-C", top, "merge-base", "--is-ancestor", old, new)
	cmd.Env = credenv.Scrub(os.Environ())
	err := cmd.Run()
	var exit *exec.ExitError
	return !(errors.As(err, &exit) && exit.ExitCode() == 1)
}

// describeHead names what HEAD pointed at: a branch, or a detached commit.
func describeHead(h headState) string {
	if h.ref == "HEAD" {
		return "detached " + shortCommit(h.commit)
	}
	return "branch " + strings.TrimPrefix(h.ref, "refs/heads/") + " (" + shortCommit(h.commit) + ")"
}

func shortCommit(commit string) string {
	switch {
	case commit == "":
		return "no commits"
	case len(commit) > 10:
		return commit[:10]
	}
	return commit
}

// commandPrefixes are words that may precede the command word of a simple
// command without being it.
var commandPrefixes = map[string]bool{"then": true, "do": true, "else": true, "if": true, "elif": true,
	"while": true, "until": true, "!": true, "{": true, "time": true, "command": true, "exec": true, "nohup": true}

// gitTargets finds the directories a shell command's cd and git invocations
// may work in (dirs), and for each git stash subcommand that changes the tree
// or the stash (anything but list and show) the directories it may run in
// (stashes). The scan does not follow control flow: a cd may sit in a branch
// that never runs or in a subshell, so each cd adds a directory that later
// commands may be in, rather than replacing root. Relative directories
// resolve against each of those. Directories written as absolute paths are
// kept apart from those composed from relative ones, which multiply, so the
// speculative ones never crowd out a named target; each kind is bounded by
// maxTargetDirs, and dirs lists root, then named, then composed directories.
// unresolved reports a cd it could not resolve (variables, ~, cd -) or a
// directory left out, after which no location is certain. This is a
// heuristic over simple commands, not a shell parser: quoted text and
// here-documents are data.
func gitTargets(root, command string) (dirs []string, stashes [][]string, unresolved bool) {
	places, targets := newDirSet(root), newDirSet(root)
	for _, words := range shellSegments(command) {
		i := 0
		for i < len(words) && (commandPrefixes[words[i]] || isAssignment(words[i])) {
			i++
		}
		if i >= len(words) {
			continue
		}
		switch filepath.Base(words[i]) {
		case "cd":
			arg := ""
			if i+1 < len(words) {
				arg = words[i+1]
			}
			moved := places.resolve(arg)
			if len(moved.all()) == 0 {
				unresolved = true
			}
			places.merge(moved)
		case "git":
			candidates, j := places, i+1
		options:
			for j < len(words) {
				switch word := words[j]; {
				case word == "-C" && j+1 < len(words):
					candidates = candidates.resolve(words[j+1])
					j += 2
				case word == "-c" && j+1 < len(words):
					j += 2
				case strings.HasPrefix(word, "-"):
					j++
				default:
					break options
				}
			}
			targets.merge(candidates)
			unresolved = unresolved || candidates.full
			if j < len(words) && words[j] == "stash" && len(candidates.all()) > 0 {
				if j+1 >= len(words) || (words[j+1] != "list" && words[j+1] != "show") {
					stashes = append(stashes, candidates.all())
				}
			}
		}
	}
	targets.merge(places)
	return targets.all(), stashes, unresolved || places.full || targets.full
}

// dirSet is a bounded set of directories: those named by absolute paths (and
// the shell's own), and those composed from relative paths.
type dirSet struct {
	named, composed []string
	full            bool // a directory was left out
}

func newDirSet(dirs ...string) dirSet {
	return dirSet{named: dirs}
}

// all lists the named directories, then the composed ones.
func (s dirSet) all() []string {
	return append(slices.Clone(s.named), s.composed...)
}

func (s *dirSet) add(dir string, named bool) {
	if dir == "" || slices.Contains(s.named, dir) || slices.Contains(s.composed, dir) {
		return
	}
	list := &s.composed
	if named {
		list = &s.named
	}
	if len(*list) >= maxTargetDirs {
		s.full = true
		return
	}
	*list = append(*list, dir)
}

func (s *dirSet) merge(o dirSet) {
	for _, dir := range o.named {
		s.add(dir, true)
	}
	for _, dir := range o.composed {
		s.add(dir, false)
	}
	s.full = s.full || o.full
}

// resolve resolves the directory word arg against each directory of s.
func (s dirSet) resolve(arg string) dirSet {
	out := dirSet{full: s.full}
	for _, dir := range s.all() {
		out.add(resolveDir(dir, arg), filepath.IsAbs(arg))
	}
	return out
}

// resolveDir resolves a directory word against base, or "" when unknown.
func resolveDir(base, dir string) string {
	switch {
	case dir == "" || dir == "-" || strings.ContainsAny(dir, "$`*?") || strings.HasPrefix(dir, "~"):
		return ""
	case filepath.IsAbs(dir):
		return filepath.Clean(dir)
	case base == "":
		return ""
	}
	return filepath.Join(base, dir)
}

func isAssignment(word string) bool {
	name, _, ok := strings.Cut(word, "=")
	if !ok || name == "" {
		return false
	}
	for i, c := range name {
		if !(c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || i > 0 && c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}

// heredoc is a pending here-document: its delimiter, and whether <<- strips
// leading tabs.
type heredoc struct {
	delim string
	strip bool
}

// shellSegments splits command into its simple commands' words: unquoted
// ; & | ( ) newlines, backticks and $( end a command, comments, line
// continuations and here-document bodies are skipped, and quotes and
// backslash escapes are removed from words.
func shellSegments(command string) [][]string {
	var (
		segments [][]string
		words    []string
		heredocs []heredoc
	)
	end := func() {
		if len(words) > 0 {
			segments = append(segments, words)
			words = nil
		}
	}
	for i := 0; i < len(command); {
		c := command[i]
		switch {
		case c == ' ' || c == '\t':
			i++
		case strings.HasPrefix(command[i:], "\\\n"):
			i += 2
		case c == '\n':
			end()
			i++
			for _, h := range heredocs {
				i = skipHeredoc(command, i, h)
			}
			heredocs = nil
		case c == '#':
			for i < len(command) && command[i] != '\n' {
				i++
			}
		case strings.HasPrefix(command[i:], "<<<"):
			i += 3
		case strings.HasPrefix(command[i:], "<<"):
			i += 2
			h := heredoc{}
			if i < len(command) && command[i] == '-' {
				h.strip = true
				i++
			}
			for i < len(command) && (command[i] == ' ' || command[i] == '\t') {
				i++
			}
			h.delim, i = readWord(command, i)
			if h.delim != "" {
				heredocs = append(heredocs, h)
			}
		case strings.HasPrefix(command[i:], "$("):
			end()
			i += 2
		case strings.IndexByte(";&|()`", c) >= 0:
			end()
			i++
		case c == '<' || c == '>':
			i++
		default:
			var word string
			word, i = readWord(command, i)
			words = append(words, word)
		}
	}
	end()
	return segments
}

// readWord reads the shell word at i, removing quotes and backslash escapes,
// and returns it with the index where it ends: unquoted whitespace, an
// operator character, or $(.
func readWord(s string, i int) (string, int) {
	var b strings.Builder
	for i < len(s) {
		c := s[i]
		switch {
		case c == '\\' && i+1 < len(s):
			if s[i+1] != '\n' { // a line continuation joins the lines
				b.WriteByte(s[i+1])
			}
			i += 2
		case c == '\'':
			end := strings.IndexByte(s[i+1:], '\'')
			if end < 0 {
				b.WriteString(s[i+1:])
				return b.String(), len(s)
			}
			b.WriteString(s[i+1 : i+1+end])
			i += end + 2
		case c == '"':
			for i++; i < len(s) && s[i] != '"'; i++ {
				if s[i] == '\\' && i+1 < len(s) {
					if i++; s[i] == '\n' {
						continue
					}
				}
				b.WriteByte(s[i])
			}
			if i >= len(s) {
				return b.String(), len(s)
			}
			i++
		case c == '$' && i+1 < len(s) && s[i+1] == '(':
			return b.String(), i
		case strings.IndexByte(" \t\n;&|()<>`", c) >= 0:
			return b.String(), i
		default:
			b.WriteByte(c)
			i++
		}
	}
	return b.String(), i
}

// skipHeredoc returns the index after the body of h starting at i: the line
// after its delimiter line, or the end of s.
func skipHeredoc(s string, i int, h heredoc) int {
	for i < len(s) {
		line, rest, found := strings.Cut(s[i:], "\n")
		next := len(s)
		if found {
			next = len(s) - len(rest)
		}
		if h.strip {
			line = strings.TrimLeft(line, "\t")
		}
		if line == h.delim {
			return next
		}
		i = next
	}
	return i
}
