package workspacelease

import (
	"os"
	"sort"
)

// ShellRun is one running shell command of a scope in a canonical worktree,
// registered so that shell write attribution can tell whether another scope's
// shell ran at the same time (internal/tools/shellclaims.go).
type ShellRun struct {
	key, scope string
	background bool
	others     map[*ShellRun]bool // other scopes' runs in key that overlapped this one
}

// PathStamp identifies one state of a dirty path for shell attribution: a
// change of any field is a write.
type PathStamp struct {
	Exists bool
	Size   int64
	Mtime  int64 // UnixNano
	Mode   os.FileMode
}

// BeginShell registers a shell command of token's scope in canonical worktree
// key; background marks a background job. Call it before the command's first
// snapshot and EndShell after its last one. It returns nil for an unscoped
// command. Foreground commands and background jobs overlap alike: a path
// changed while another scope's shell ran has no single likely writer.
func (s *Service) BeginShell(key string, token *Token, background bool) *ShellRun {
	if s == nil || key == "" || token.Scope() == "" {
		return nil
	}
	scope := token.Scope()
	run := &ShellRun{key: key, scope: scope, background: background, others: map[*ShellRun]bool{}}
	s.mu.Lock()
	defer s.mu.Unlock()
	for other := range s.shells {
		if other.key == key && other.scope != scope {
			other.others[run] = true
			run.others[other] = true
		}
	}
	s.shells[run] = struct{}{}
	return run
}

// ShellOverlap names, each sorted, the other scopes whose shells, foreground
// or background, ran in a run's worktree while it was registered (Ran), and
// those of them whose overlapping runs include a background job (Jobs).
type ShellOverlap struct {
	Ran  []string
	Jobs []string
}

// AttributeShell runs fn, which ends a shell's registration (EndShell) and
// records its claims, while no other shell's attribution runs, so a shell
// ending meanwhile sees either the run still registered or its claims.
func (s *Service) AttributeShell(fn func()) {
	if s == nil {
		fn()
		return
	}
	s.attrMu.Lock()
	defer s.attrMu.Unlock()
	fn()
}

// EndShell unregisters run and reports the other scopes whose shells ran in
// its worktree at any time while it was registered. Call it within
// AttributeShell.
func (s *Service) EndShell(run *ShellRun) ShellOverlap {
	if s == nil || run == nil {
		return ShellOverlap{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.shells, run)
	ran, jobs := map[string]bool{}, map[string]bool{}
	for other := range run.others {
		ran[other.scope] = true
		if other.background {
			jobs[other.scope] = true
		}
	}
	// Runs still registered keep their reference to run (they need its scope);
	// run's own references are no longer needed, so a long job does not pin
	// the earlier commands it overlapped.
	run.others = nil
	return ShellOverlap{Ran: sortedKeys(ran), Jobs: sortedKeys(jobs)}
}

func sortedKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for key := range set {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}
