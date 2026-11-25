package workspacelease

import (
	"os"
	"sort"
	"time"
)

// ShellRun is one running shell command of a scope in a canonical worktree,
// registered so that shell write attribution can tell whether another scope's
// shell ran at the same time (internal/tools/shellclaims.go).
type ShellRun struct {
	key, scope string
	// incarnation is the session instance (Token.Incarnation) that ran it.
	incarnation uint64
	background  bool
	start, end  time.Time          // end is zero while the run is registered
	others      map[*ShellRun]bool // other scopes' runs in key that overlapped this one
	// before is the worktree's dirty set when the command started
	// (SetShellSnapshot), nil until it is taken or when it is unavailable.
	before map[string]PathStamp
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
// command.
func (s *Service) BeginShell(key string, token *Token, background bool) *ShellRun {
	if s == nil || key == "" || token.Scope() == "" {
		return nil
	}
	scope := token.Scope()
	run := &ShellRun{key: key, scope: scope, incarnation: token.Incarnation(), background: background, start: time.Now(), others: map[*ShellRun]bool{}}
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

// SetShellSnapshot records run's dirty set as the command starts. Take it
// before the command is started: until then the command has written nothing.
func (s *Service) SetShellSnapshot(run *ShellRun, before map[string]PathStamp) {
	if s == nil || run == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	run.before = before
}

// ShellOverlap names, each sorted, the other scopes whose shells ran in a
// run's worktree while it was registered: Ran lists them all, Concurrent only
// those whose run counts as concurrent with it (see EndShell). For a
// background job, ShortWindows holds the starting dirty sets of other scopes'
// short foreground commands still running when the job ended: a path whose
// state differs from one of them changed in that command's window, and is
// left for the command to attribute when it ends, so its window wins whichever
// ends first.
// Claimants are the run's own session and those of the concurrent runs, each
// with the incarnation its run began in, for RecordShellAmbiguous.
type ShellOverlap struct {
	Ran          []string
	Concurrent   []string
	ShortWindows []map[string]PathStamp
	Claimants    []ShellClaimant
}

// ShellClaimant is a scope and the session instance (Token.Incarnation) whose
// shell ran; Incarnation 0 is unknown.
type ShellClaimant struct {
	Scope       string
	Incarnation uint64
}

// InShortWindow reports whether a path now in state stamp changed during one
// of the short foreground commands in o.ShortWindows.
func (o ShellOverlap) InShortWindow(rel string, stamp PathStamp) bool {
	for _, before := range o.ShortWindows {
		if prior, ok := before[rel]; !ok || prior != stamp {
			return true
		}
	}
	return false
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
// its worktree at any time while it was registered. Two overlapping runs count
// as concurrent unless one is a background job and the other a foreground
// command shorter than short ("the narrowest window wins"): a job that runs for
// hours would otherwise overlap every command and no shell write could settle
// on one scope. A foreground run still registered counts as short while it has
// run for less than short. Call it within AttributeShell.
func (s *Service) EndShell(run *ShellRun, short time.Duration) ShellOverlap {
	if s == nil || run == nil {
		return ShellOverlap{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	run.end = now
	delete(s.shells, run)
	isShort := func(r *ShellRun) bool {
		if r.background {
			return false
		}
		end := r.end
		if end.IsZero() {
			end = now
		}
		return end.Sub(r.start) < short
	}
	ran, concurrent := map[string]bool{}, map[string]bool{}
	claimants := map[ShellClaimant]bool{{Scope: run.scope, Incarnation: run.incarnation}: true}
	var windows []map[string]PathStamp
	for other := range run.others {
		ran[other.scope] = true
		if !(run.background && isShort(other)) && !(other.background && isShort(run)) {
			concurrent[other.scope] = true
			claimants[ShellClaimant{Scope: other.scope, Incarnation: other.incarnation}] = true
		}
		// A command without a starting snapshot has not started, so it
		// has written nothing yet.
		if run.background && isShort(other) && other.end.IsZero() && other.before != nil {
			windows = append(windows, other.before)
		}
	}
	// Runs still registered keep their reference to run (they need its end,
	// scope and incarnation); run's own references and its snapshot are no
	// longer needed, so a long job pins neither the earlier commands it
	// overlapped nor their dirty sets.
	run.others, run.before = nil, nil
	list := make([]ShellClaimant, 0, len(claimants))
	for c := range claimants {
		list = append(list, c)
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].Scope != list[j].Scope {
			return list[i].Scope < list[j].Scope
		}
		return list[i].Incarnation < list[j].Incarnation
	})
	return ShellOverlap{Ran: sortedKeys(ran), Concurrent: sortedKeys(concurrent), ShortWindows: windows, Claimants: list}
}

func sortedKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for key := range set {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}
