package tools

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// gitTargets finds git and cd targets at command positions only. A cd adds a
// place later commands may be in rather than replacing the shell's directory,
// since the scan does not follow control flow.
func TestGitTargets(t *testing.T) {
	for _, tc := range []struct {
		command    string
		dirs       []string
		stashes    [][]string
		unresolved bool
	}{
		{command: "go test ./...", dirs: []string{"/r"}},
		{command: "git status", dirs: []string{"/r"}},
		{command: "GIT_PAGER=cat git -c x=y --no-pager -C sub stash push -m wip",
			dirs: []string{"/r", "/r/sub"}, stashes: [][]string{{"/r/sub"}}},
		{command: "git stash list; git stash show -p", dirs: []string{"/r"}},
		{command: "cd ../b && git stash && (git -C c reset --hard)",
			dirs: []string{"/r", "/b", "/r/c", "/b/c"}, stashes: [][]string{{"/r", "/b"}}},
		{command: "if false; then cd /b; fi; git stash apply",
			dirs: []string{"/r", "/b"}, stashes: [][]string{{"/r", "/b"}}},
		{command: "echo 'git stash' \"git reset\" # git stash", dirs: []string{"/r"}},
		{command: "x=$(git stash) || git -C $DIR stash", dirs: []string{"/r"}, stashes: [][]string{{"/r"}}},
		{command: "cd - && git stash", dirs: []string{"/r"}, stashes: [][]string{{"/r"}}, unresolved: true},
		{command: "if true; then /usr/bin/git stash; fi", dirs: []string{"/r"}, stashes: [][]string{{"/r"}}},
		{command: "cat <<'EOF' > notes\ngit stash pop\nEOF\ngit status", dirs: []string{"/r"}},
		{command: "cat <<-END\n\tgit stash\n\tEND\ngit stash", dirs: []string{"/r"}, stashes: [][]string{{"/r"}}},
		{command: "git stash \\\n  list", dirs: []string{"/r"}},
		{command: "git \\\n  stash apply", dirs: []string{"/r"}, stashes: [][]string{{"/r"}}},
		{command: "git \"sta\\\nsh\" pop", dirs: []string{"/r"}, stashes: [][]string{{"/r"}}},
		{command: "echo \"unterminated git stash", dirs: []string{"/r"}},
		{command: "cd a && cd b && cd c && git status && git -C /s reset --hard HEAD~1",
			dirs: []string{"/r", "/s", "/r/a", "/r/b", "/r/a/b", "/r/c", "/r/a/c", "/r/b/c", "/r/a/b/c"}},
	} {
		dirs, stashes, unresolved := gitTargets("/r", tc.command)
		if !reflect.DeepEqual(dirs, tc.dirs) || !reflect.DeepEqual(stashes, tc.stashes) || unresolved != tc.unresolved {
			t.Errorf("%q: dirs %q stashes %q unresolved %v, want %q %q %v",
				tc.command, dirs, stashes, unresolved, tc.dirs, tc.stashes, tc.unresolved)
		}
	}
	// Relative cds branch from every place seen so far; the scan keeps a
	// bounded set, the shell's own directory first, and marks it uncertain.
	var command strings.Builder
	for i := 0; i < 15; i++ {
		fmt.Fprintf(&command, "cd dir%d; git -C sub%d status; ", i, i)
	}
	command.WriteString("git stash apply; git -C /s stash")
	dirs, stashes, unresolved := gitTargets("/r", command.String())
	if len(dirs) > 2*maxTargetDirs || dirs[0] != "/r" || !slices.Contains(dirs, "/s") || len(stashes) != 2 ||
		len(stashes[0]) > 2*maxTargetDirs || stashes[0][0] != "/r" || !reflect.DeepEqual(stashes[1], []string{"/s"}) || !unresolved {
		t.Errorf("unbounded scan: dirs %q stashes %q unresolved %v", dirs, stashes, unresolved)
	}
}
