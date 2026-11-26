package session

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/whyrusleeping/ycc/internal/docs"
	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/orchestrator"
	"github.com/whyrusleeping/ycc/internal/workspacelease"
)

// Experimental session coordination: list_sessions and same-file overlap
// notices, gated per project by [experimental] session_coordination in the
// primary tree's .ycc/config.toml. A session gets the tool and notices only
// when the setting was on when it was built; turning the setting off stops
// both at once.

// Bounds on list_sessions output.
const (
	listSessionsMaxClaims = 10        // claimed paths shown per session
	listSessionsMaxBytes  = 24 * 1024 // whole overview
	maxNoticeTexts        = 10        // full notices per delivery; the rest are summarized
)

// coordinationEnabled reads the live project setting for the project owning absWS.
func (m *Manager) coordinationEnabled(absWS string) bool {
	return docs.SessionCoordination(m.primaryTreeFor(absWS))
}

// SessionCoordination reports a registered project's setting.
func (m *Manager) SessionCoordination(project string) (bool, error) {
	ws, err := m.resolveProjectWorkspace(project)
	if err != nil {
		return false, err
	}
	return m.coordinationEnabled(ws), nil
}

// SetSessionCoordination changes a registered project's setting. Enabling
// applies to sessions built afterwards; disabling applies at once.
func (m *Manager) SetSessionCoordination(project string, on bool) error {
	ws, err := m.resolveProjectWorkspace(project)
	if err != nil {
		return err
	}
	return docs.SetSessionCoordination(m.primaryTreeFor(ws), on)
}

// listSessionsFor backs list_sessions for session self working in absWS.
// While session coordination is off it lists only the sessions holding
// exclusive leases, which do not depend on the setting.
func (m *Manager) listSessionsFor(absWS, self, path string, now time.Time) (string, error) {
	if !m.coordinationEnabled(absWS) {
		if strings.TrimSpace(path) != "" {
			return "", orchestrator.ErrCoordinationDisabled
		}
		var holders []LiveSession
		for _, s := range m.liveSessions(m.primaryTreeFor(absWS), self, now, false) {
			if len(s.Leases) > 0 {
				holders = append(holders, s)
			}
		}
		if len(holders) == 0 {
			return "", orchestrator.ErrCoordinationDisabled
		}
		return "Session coordination is off for this project, so this lists only the other sessions holding exclusive leases:\n" +
			liveSessionText(holders, now, false), nil
	}
	if strings.TrimSpace(path) != "" {
		return m.pathClaimants(absWS, path, now)
	}
	primary := m.primaryTreeFor(absWS)
	lines := m.liveSessionLinesWith(primary, self, now, true)
	board := m.boardSection(primary, now)
	if board == "" {
		board = "Project notice board: no active notices.\n"
	}
	if lines == "" {
		return "No other session in this project has been active in the last hour. Use path= to find who claims a file, including ended sessions.\n" + board, nil
	}
	out := "Other sessions in this project (sessions idle for over an hour are omitted unless they set a session notice; path= finds any claimant). " +
		"Claims are uncommitted writes, not reservations.\n" + lines
	if len(out) > listSessionsMaxBytes {
		out = clipText(out, listSessionsMaxBytes) + "\n[truncated]\n"
	}
	return out + board, nil
}

// PathClaim is one repository-relative path a session claims and when it
// first claimed it.
type PathClaim struct {
	Path  string
	Since time.Time
}

// sessionClaims returns scope's claimed paths from a worktree's claims, newest
// claim first.
func sessionClaims(claims map[string][]workspacelease.Claim, scope string) []PathClaim {
	var out []PathClaim
	for path, list := range claims {
		for _, c := range list {
			if c.Scope == scope {
				out = append(out, PathClaim{Path: path, Since: c.Since})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Since.Equal(out[j].Since) {
			return out[i].Since.After(out[j].Since)
		}
		return out[i].Path < out[j].Path
	})
	return out
}

// claimLines formats a session's claimed paths with their age, bounded.
func claimLines(claims []PathClaim, now time.Time) string {
	if len(claims) == 0 {
		return ""
	}
	return fmt.Sprintf("    claims %d path(s): %s\n", len(claims), claimList(claims, now))
}

// claimList lists claimed paths with their age, bounded.
func claimList(claims []PathClaim, now time.Time) string {
	parts := make([]string, 0, listSessionsMaxClaims+1)
	for i, c := range claims {
		if i == listSessionsMaxClaims {
			parts = append(parts, fmt.Sprintf("… %d more", len(claims)-i))
			break
		}
		parts = append(parts, c.Path+" ("+claimAge(c.Since, now)+")")
	}
	return strings.Join(parts, ", ")
}

// claimAge describes how long ago a claim was first recorded.
func claimAge(since, now time.Time) string {
	if since.IsZero() {
		return "age unknown"
	}
	d := now.Sub(since)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d/time.Minute))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d/time.Hour))
	}
	return fmt.Sprintf("%dd ago", int(d/(24*time.Hour)))
}

// sessionBrief is what coordination output says about a session, live or ended.
type sessionBrief struct {
	found        bool
	live         bool
	mode         string
	state        string
	focus        []string
	held         bool // live and still working on its focus
	title        string
	lastActivity time.Time
}

// briefSession describes session id of the project at primary: a live session
// from memory, an ended one from its log. A session of another project is not
// found, so nothing about it leaks across projects.
func (m *Manager) briefSession(primary, id string) sessionBrief {
	if s, ok := m.Get(id); ok {
		if canonicalPath(m.primaryTreeFor(s.Workspace)) != canonicalPath(primary) {
			return sessionBrief{}
		}
		evs := s.Log().Snapshot()
		b := sessionBrief{found: true, live: true, mode: s.Mode, state: sessionActivityState(s, evs),
			focus: focusTasks(evs), held: s.activelyWorking(), title: deriveTitle(evs)}
		if len(evs) > 0 {
			b.lastActivity = evs[len(evs)-1].TS
		}
		return b
	}
	if id == "" || filepath.Base(id) != id {
		return sessionBrief{}
	}
	absPrimary, err := filepath.Abs(primary)
	if err != nil {
		return sessionBrief{}
	}
	_, logPath, err := m.locateSessionLog(absPrimary, id)
	if err != nil {
		return sessionBrief{}
	}
	evs, _ := readSummaryEventsTolerantResult(logPath)
	if len(evs) == 0 {
		return sessionBrief{}
	}
	return sessionBrief{found: true, mode: event.Reduce(evs).Mode, state: "ended", focus: focusTasks(evs),
		title: deriveTitle(evs), lastActivity: evs[len(evs)-1].TS}
}

// purpose is a short parenthetical naming what a session is (was) doing.
func (b sessionBrief) purpose() string {
	switch {
	case !b.found:
		return ""
	case len(b.focus) > 0 && b.held:
		return " (working on " + strings.Join(b.focus, ", ") + ")"
	case len(b.focus) > 0:
		return " (earlier focus " + strings.Join(b.focus, ", ") + ")"
	case b.title != "":
		return fmt.Sprintf(" (%q)", clipText(b.title, 120))
	}
	return ""
}

// pathClaimants answers list_sessions path=: who claims path in its worktree.
func (m *Manager) pathClaimants(absWS, path string, now time.Time) (string, error) {
	abs := path
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(absWS, abs)
	}
	abs = filepath.Clean(abs)
	primary := m.primaryTreeFor(absWS)
	if !m.pathInProject(primary, abs) {
		return "", fmt.Errorf("%s is outside this project and its workstreams", path)
	}
	_, rel, claims, ok := m.ownership.PathClaims(abs, absWS)
	if !ok {
		return "", fmt.Errorf("%s is not inside a git worktree", path)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Sessions claiming %s (claims are uncommitted writes, not reservations):\n", rel)
	listed := 0
	for _, c := range claims {
		// Claims by sessions of other projects sharing the worktree, or
		// without a log, are left out entirely.
		brief := m.briefSession(primary, c.Scope)
		if !brief.found {
			continue
		}
		listed++
		fmt.Fprintf(&b, "- %s claimed %s", c.Scope, claimAge(c.Since, now))
		switch {
		case brief.live:
			fmt.Fprintf(&b, "; live %s session, %s", brief.mode, brief.state)
		default:
			fmt.Fprintf(&b, "; ended %s session", brief.mode)
		}
		if !brief.lastActivity.IsZero() {
			b.WriteString(", last activity " + brief.lastActivity.UTC().Format("2006-01-02 15:04 UTC"))
		}
		if len(brief.focus) > 0 {
			b.WriteString("; focus " + strings.Join(brief.focus, ", "))
			if !brief.held {
				b.WriteString(" (not held)")
			}
		}
		b.WriteString("\n")
		if brief.title != "" {
			b.WriteString("    title: " + clipText(brief.title, 160) + "\n")
		}
	}
	if listed == 0 {
		return fmt.Sprintf("No session of this project claims %s: none has recorded an uncommitted write to it (it may still differ from HEAD through an unattributed change).", rel), nil
	}
	return b.String(), nil
}

// pathInProject reports whether abs lies in the project's primary tree or one
// of its in-flight workstream worktrees.
func (m *Manager) pathInProject(primary, abs string) bool {
	roots := []string{primary}
	for _, ws := range m.Workstreams(m.projectLabel(primary)) {
		if ws.Status.InFlight() && ws.WorktreePath != "" {
			roots = append(roots, ws.WorktreePath)
		}
	}
	// Containment is judged on the symlink-resolved destination, so a link
	// inside the project cannot reach another project's claims.
	resolved := resolveExisting(abs)
	for _, root := range roots {
		if withinDir(resolved, canonicalPath(root)) {
			return true
		}
	}
	return false
}

// resolveExisting resolves symlinks in the longest existing prefix of p.
func resolveExisting(p string) string {
	suffix := ""
	for dir := p; ; dir = filepath.Dir(dir) {
		if r, err := filepath.EvalSymlinks(dir); err == nil {
			return filepath.Join(r, suffix)
		}
		if filepath.Dir(dir) == dir {
			return p
		}
		suffix = filepath.Join(filepath.Base(dir), suffix)
	}
}

// withinDir reports whether p is dir or below it.
func withinDir(p, dir string) bool {
	rel, err := filepath.Rel(dir, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// overlapNotice is a pending notice for a coordinator about another session's
// write to a path it claims.
type overlapNotice struct {
	path, session, text string
}

// claimOverlap is the ownership service's overlap hook: a write by session
// o.Writer touched a path other sessions already claim. Each (worktree, path,
// session pair) is reported once: to the writer through the returned tool
// result text, and to each other session's live coordinator at its next
// checkpoint. Nothing is woken.
func (m *Manager) claimOverlap(o workspacelease.Overlap) string {
	writer, ok := m.Get(o.Writer)
	if !ok || !writer.coordination || !m.coordinationEnabled(writer.Workspace) {
		return ""
	}
	primary := m.primaryTreeFor(writer.Workspace)
	var writerBrief *sessionBrief
	var lines []string
	for _, other := range o.Others {
		// Sessions of other projects sharing the worktree are never named.
		key := overlapKey(o.Worktree, o.Path, o.Writer, other)
		if m.overlapNoticed(key) {
			continue
		}
		otherBrief := m.briefSession(primary, other)
		if !otherBrief.found || !m.firstOverlap(key) {
			continue
		}
		lines = append(lines, fmt.Sprintf("[coordination notice] %s also holds uncommitted work from session %s%s. "+
			"Claims are advisory: check list_sessions path=%s and keep your changes compatible with theirs.",
			o.Path, other, otherBrief.purpose(), o.Path))
		if peer, ok := m.Get(other); ok && otherBrief.live && peer.coordination {
			if writerBrief == nil {
				brief := m.briefSession(primary, o.Writer)
				writerBrief = &brief
			}
			peer.queueCoordinationNotice(overlapNotice{path: o.Path, session: o.Writer,
				text: fmt.Sprintf("[coordination notice] Session %s%s just wrote %s, which holds your uncommitted work. "+
					"Claims are advisory: check list_sessions path=%s before changing it further.",
					o.Writer, writerBrief.purpose(), o.Path, o.Path)})
		}
	}
	return strings.Join(lines, "\n")
}

// overlapKey identifies an overlap on path in worktree between sessions a and
// b, in either order.
func overlapKey(worktree, path, a, b string) string {
	if b < a {
		a, b = b, a
	}
	return worktree + "\x00" + path + "\x00" + a + "\x00" + b
}

// overlapNoticed reports whether the overlap was already reported.
func (m *Manager) overlapNoticed(key string) bool {
	m.overlapMu.Lock()
	defer m.overlapMu.Unlock()
	return m.overlapSeen[key]
}

// firstOverlap records the overlap, reporting whether it is the first time.
func (m *Manager) firstOverlap(key string) bool {
	m.overlapMu.Lock()
	defer m.overlapMu.Unlock()
	if m.overlapSeen == nil {
		m.overlapSeen = map[string]bool{}
	}
	if m.overlapSeen[key] {
		return false
	}
	m.overlapSeen[key] = true
	return true
}

// queueCoordinationNotice holds a notice for the coordinator's next
// checkpoint. It never wakes the session or ends its wait.
func (s *Session) queueCoordinationNotice(n overlapNotice) {
	s.noticeMu.Lock()
	defer s.noticeMu.Unlock()
	s.notices = append(s.notices, n) // bounded: each (path, pair) is queued once
}

// drainCoordinationNotices delivers pending overlap notices as one user-role
// message, recorded as a system-actor coordination_notice so replay places it
// where the live loop posted it. Notices are dropped once the project setting
// is off.
func (s *Session) drainCoordinationNotices() ([]string, error) {
	s.noticeMu.Lock()
	pending := s.notices
	s.notices = nil
	s.noticeMu.Unlock()
	if len(pending) == 0 || s.coordinationOn == nil || !s.coordinationOn() {
		return nil, nil
	}
	var texts, more []string
	records := make([]any, len(pending))
	for i, n := range pending {
		if i < maxNoticeTexts {
			texts = append(texts, n.text)
		} else {
			more = append(more, n.path+" (by "+n.session+")")
		}
		records[i] = map[string]any{"path": n.path, "session": n.session}
	}
	if len(more) > 0 {
		// Not clipped: each (path, pair) is queued only once, so this is
		// bounded by real overlaps, and a dropped path would never be resent.
		texts = append(texts, fmt.Sprintf("[coordination notice] %d more of your claimed files were written by other sessions: %s. "+
			"Check list_sessions path=<file> before changing them further.", len(more), strings.Join(more, ", ")))
	}
	text := strings.Join(texts, "\n")
	s.emitter.EmitAs("system", event.CoordinationNotice, map[string]any{"text": text, "notices": records})
	if err := s.logFailure(); err != nil {
		return nil, fmt.Errorf("session event log failed: %w", err)
	}
	return []string{text}, nil
}
