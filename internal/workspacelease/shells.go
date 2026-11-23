package workspacelease

import "sort"

// ShellRun is one running shell command of a scope in a canonical worktree,
// registered so that shell write attribution can tell whether another scope's
// shell ran at the same time (internal/tools/shellclaims.go).
type ShellRun struct {
	key, scope string
	others     map[string]bool // other scopes whose shells ran in key during this run
}

// BeginShell registers a shell command of scope in canonical worktree key. Call
// it before the command's first snapshot and EndShell after its last one. It
// returns nil for an unscoped command.
func (s *Service) BeginShell(key, scope string) *ShellRun {
	if s == nil || key == "" || scope == "" {
		return nil
	}
	run := &ShellRun{key: key, scope: scope, others: map[string]bool{}}
	s.mu.Lock()
	defer s.mu.Unlock()
	for other := range s.shells {
		if other.key == key && other.scope != scope {
			other.others[scope] = true
			run.others[other.scope] = true
		}
	}
	s.shells[run] = struct{}{}
	return run
}

// EndShell unregisters run and returns, sorted, the other scopes whose shells
// ran in its worktree at any time while it was registered.
func (s *Service) EndShell(run *ShellRun) []string {
	if s == nil || run == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.shells, run)
	out := make([]string, 0, len(run.others))
	for scope := range run.others {
		out = append(out, scope)
	}
	sort.Strings(out)
	return out
}
