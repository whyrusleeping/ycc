package session

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/whyrusleeping/ycc/internal/engine"
	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/orchestrator"
	"github.com/whyrusleeping/ycc/internal/project"
)

func boardDigests(s *Session) []event.Event {
	var out []event.Event
	for _, ev := range eventsOf(s, event.CoordinationNotice) {
		if strings.HasPrefix(fmt.Sprint(ev.Data["text"]), "[project notice board]") {
			out = append(out, ev)
		}
	}
	return out
}

// The notice board end to end: intents and notices show in the listings,
// reach other running sessions only at their next checkpoint, replay
// identically, and are gone once cleared, expired, or the session ends.
func TestNoticeBoardIntentsNoticesAndDigests(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	ws := t.TempDir()
	sessionGitAt(t, ws, "init", "-q")
	sessionGitAt(t, ws, "-c", "user.name=t", "-c", "user.email=t@example.invalid", "commit", "-q", "--allow-empty", "-m", "init")
	stub, bodies := writeStub(t, ws)
	m := NewManager(stubRegistry(t, stub.URL), ws)
	m.SetProjects(project.NewMemory())
	if _, err := m.AddProject(ws, "proj"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.ReclaimAll)

	// Off: no board, no digest, no footer section.
	off, err := m.Start(Config{Project: "proj", Mode: "chat", Prompt: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	waitIdleCount(t, off, 1)
	if off.deps.Board != nil || off.board != nil {
		t.Fatal("notice board offered with the setting off")
	}
	if err := m.SetSessionCoordination("proj", true); err != nil {
		t.Fatal(err)
	}

	a, err := m.Start(Config{Project: "proj", Mode: "chat", Prompt: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	waitIdleCount(t, a, 1)
	if len(boardDigests(a)) != 0 {
		t.Fatal("empty board produced an opening digest")
	}
	reg, system := orchestrator.BuildMode("work", a.deps, false)
	names := map[string]bool{}
	for _, def := range reg.APIDefs() {
		names[def.Function.Name] = true
	}
	for _, name := range []string{"list_sessions", "set_intent", "post_notice", "clear_notice"} {
		if !names[name] {
			t.Fatalf("work mode lacks %s", name)
		}
	}
	if !strings.Contains(system, "PROJECT NOTICE BOARD") {
		t.Fatal("coordinator prompt lacks the notice board guidance")
	}

	// Intent and notice from A.
	if _, err := a.board.SetIntent("deploying normalize-v2\nto cubey;\u202e avoid restarts"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.board.Post("Tests A and B are red on master", "master tests", 2*time.Hour); err != nil {
		t.Fatal(err)
	}
	notices := m.activeNotices(ws, time.Now())
	if len(notices) != 1 || notices[0].ID != "n1" || notices[0].SessionID != a.ID || notices[0].Actor != "coordinator" ||
		notices[0].Subject != "master tests" || notices[0].ExpiresAt.Sub(notices[0].PostedAt) != 2*time.Hour {
		t.Fatalf("recorded notice = %+v", notices)
	}
	if posted := eventsOf(a, event.NoticePosted); len(posted) != 1 || posted[0].Data["id"] != "n1" {
		t.Fatalf("poster's log lacks notice_posted: %+v", posted)
	}
	// A new session opens with the board and A's intent.
	b, err := m.Start(Config{Project: "proj", Mode: "chat", Prompt: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	waitIdleCount(t, b, 1)
	opening := boardDigests(b)
	if len(opening) != 1 || opening[0].Actor != "system" {
		t.Fatalf("opening digests = %+v", opening)
	}
	text := fmt.Sprint(opening[0].Data["text"])
	for _, want := range []string{"n1 [master tests] \"Tests A and B are red on master\"", "session " + a.ID,
		a.ID + ": \"deploying normalize-v2 to cubey; avoid restarts\"", "not an authorization"} {
		if !strings.Contains(text, want) {
			t.Fatalf("opening digest lacks %q:\n%s", want, text)
		}
	}
	found := false
	for _, body := range bodies() {
		found = found || strings.Contains(body, "Tests A and B are red on master")
	}
	if !found {
		t.Fatal("the model never saw the opening digest")
	}
	// Listings: list_sessions and the list_backlog footer; watch-run context
	// lists intents with the sessions and gets notices as its opening digest.
	for name, got := range map[string]string{
		"list_sessions": func() string { out, _ := m.listSessionsFor(ws, b.ID, "", time.Now()); return out }(),
		"footer":        m.otherSessionsNote(ws, b.ID, time.Now()),
	} {
		if !strings.Contains(got, "intent: \"deploying normalize-v2 to cubey; avoid restarts\"") || !strings.Contains(got, "n1 [master tests]") {
			t.Fatalf("%s lacks the intent or notice:\n%s", name, got)
		}
	}
	if got := m.watchActivitySnapshot(ws); !strings.Contains(got, "intent: \"deploying normalize-v2") {
		t.Fatalf("watch-run context lacks the intent:\n%s", got)
	}

	// B posts while A is idle: A is not woken, and hears of it at its next
	// checkpoint only. A pending question on A is untouched.
	answered := make(chan string, 1)
	go func() {
		ans, _ := a.inter.Ask(context.Background(), "Restart the service?", []string{"yes", "no"})
		answered <- ans
	}()
	waitFor(t, a.PendingQuestion)
	if _, err := b.board.Post("Measuring until 08:26; don't restart the service", "cubey", time.Hour); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if got := len(eventsOf(a, event.SessionIdle)); got != 1 || len(boardDigests(a)) != 0 || !a.PendingQuestion() {
		t.Fatalf("notice woke A or touched its question: idles=%d digests=%d pending=%v", got, len(boardDigests(a)), a.PendingQuestion())
	}
	if !a.inter.Answer("no") || <-answered != "no" {
		t.Fatal("the question was not left for the user to answer")
	}
	if err := a.SendInput("hello again"); err != nil {
		t.Fatal(err)
	}
	waitIdleCount(t, a, 2)
	digests := boardDigests(a)
	if len(digests) != 1 || !strings.Contains(fmt.Sprint(digests[0].Data["text"]), "- new: n2 [cubey]") ||
		strings.Contains(fmt.Sprint(digests[0].Data["text"]), "n1 ") {
		t.Fatalf("A's checkpoint digests = %+v", digests)
	}
	var replayed []string
	for _, msg := range engine.ReplayHistory(a.Log().Snapshot()) {
		if msg.Role == "user" && strings.HasPrefix(msg.Content, "[project notice board]") {
			replayed = append(replayed, msg.Content)
		}
	}
	if len(replayed) != 1 || replayed[0] != digests[0].Data["text"] {
		t.Fatalf("replayed digests = %q", replayed)
	}

	// A clears B's notice; B hears of the clear, A (who cleared it) does not.
	if _, err := a.board.Clear("n2", "measurement finished"); err != nil {
		t.Fatal(err)
	}
	if got := m.activeNotices(ws, time.Now()); len(got) != 1 || got[0].ID != "n1" {
		t.Fatalf("active after clear = %+v", got)
	}
	if texts, _ := b.drainBoardDigest(time.Now()); len(texts) != 1 || !strings.Contains(texts[0], "cleared: n2") ||
		!strings.Contains(texts[0], "by session "+a.ID+": measurement finished") {
		t.Fatalf("B's clear digest = %q", texts)
	}
	if texts, _ := a.drainBoardDigest(time.Now()); texts != nil {
		t.Fatalf("clearer was told of its own clear: %q", texts)
	}
	// Expiry is silent and drops the notice everywhere.
	later := time.Now().Add(3 * time.Hour)
	if texts, _ := b.drainBoardDigest(later); texts != nil {
		t.Fatalf("expiry produced a digest: %q", texts)
	}
	if section := m.boardSection(ws, later); section != "" {
		t.Fatalf("expired notice still listed:\n%s", section)
	}

	// Reopen: nothing pending is restored; the reopened session starts from
	// the current board, without expired or cleared notices (or its own).
	if _, err := b.board.Post("Deploying build 42", "cubey", time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := b.board.Post("Build 43 is next", "cubey", time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := m.Stop(a.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { _, live := m.Get(a.ID); return !live })
	if note := m.otherSessionsNote(ws, b.ID, time.Now()); strings.Contains(note, "intent:") {
		t.Fatalf("ended session's intent still listed:\n%s", note)
	}
	ra, err := m.Reopen("proj", a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if texts, err := ra.drainBoardDigest(time.Now().Add(2 * time.Minute)); err != nil || len(texts) != 1 ||
		!strings.Contains(texts[0], "n4 [cubey]") || strings.Contains(texts[0], "n1 ") || strings.Contains(texts[0], "n2 ") || strings.Contains(texts[0], "n3 ") {
		t.Fatalf("reopened session's first digest = %q %v", texts, err)
	}

	// The user clears through the RPC path; the board records it.
	if err := m.ClearProjectNotice("proj", "n4", "superseded"); err != nil {
		t.Fatal(err)
	}
	if texts, _ := ra.drainBoardDigest(time.Now()); len(texts) != 1 || !strings.Contains(texts[0], "cleared: n4 \"Build 43 is next\", by the user: superseded") {
		t.Fatalf("user clear digest = %q", texts)
	}

	// Off again: tools report disabled, nothing is delivered or listed.
	if err := m.SetSessionCoordination("proj", false); err != nil {
		t.Fatal(err)
	}
	if _, err := b.board.Post("x", "", time.Hour); !errors.Is(err, orchestrator.ErrCoordinationDisabled) {
		t.Fatalf("post with the setting off: %v", err)
	}
	if _, err := b.board.SetIntent("x"); !errors.Is(err, orchestrator.ErrCoordinationDisabled) {
		t.Fatalf("set_intent with the setting off: %v", err)
	}
	fresh := &Session{ID: "s_new", board: ra.board, coordinationOn: func() bool { return m.coordinationEnabled(ws) }}
	if texts, _ := fresh.drainBoardDigest(time.Now()); texts != nil {
		t.Fatalf("digest with the setting off: %q", texts)
	}
	if note := m.otherSessionsNote(ws, "", time.Now()); strings.Contains(note, "notice board") {
		t.Fatalf("footer lists the board with the setting off:\n%s", note)
	}
	if notices, intents, err := m.ProjectNoticeBoard("proj"); err != nil || notices != nil || intents != nil {
		t.Fatalf("RPC board with the setting off: %v %v %v", notices, intents, err)
	}
}

// Text is sanitized and bounded, expiry is required and bounded, and a session
// has caps on active notices and hourly posts; the board persists.
func TestNoticeBoardCaps(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	ws := t.TempDir()
	sessionGitAt(t, ws, "init", "-q")
	m := &Manager{}
	now := time.Date(2026, 11, 20, 5, 0, 0, 0, time.UTC)
	post := func(session, text string, d time.Duration, at time.Time) (Notice, error) {
		return m.postNotice(ws, Notice{Text: text, SessionID: session, Actor: "coordinator"}, d, at)
	}
	for _, tc := range []struct {
		text string
		d    time.Duration
	}{
		{" \n\t ", time.Hour},
		{strings.Repeat("é", noticeMaxText+1), time.Hour},
		{"ok", 30 * time.Second},
		{"ok", 25 * time.Hour},
	} {
		if _, err := post("s1", tc.text, tc.d, now); err == nil {
			t.Fatalf("post(%q, %s) accepted", tc.text, tc.d)
		}
	}
	if _, err := m.postNotice(ws, Notice{Text: "ok", Subject: strings.Repeat("x", noticeMaxSubject+1)}, time.Hour, now); err == nil {
		t.Fatal("over-long subject accepted")
	}
	n, err := post("s1", "line one\r\nline\x1b two", time.Hour, now)
	if err != nil || n.Text != "line one line two" {
		t.Fatalf("sanitized text = %q, %v", n.Text, err)
	}
	for i := 0; i < noticeMaxActive-1; i++ {
		if _, err := post("s1", "more", time.Hour, now); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := post("s1", "one too many", time.Hour, now); err == nil || !strings.Contains(err.Error(), "active notices") {
		t.Fatalf("active cap: %v", err)
	}
	if _, err := post("s2", "another session", time.Hour, now); err != nil {
		t.Fatalf("cap leaked across sessions: %v", err)
	}
	// Clearing frees an active slot but not the hourly budget.
	for i := 0; i < noticeMaxPerHour-noticeMaxActive; i++ {
		if _, err := m.clearNotice(ws, fmt.Sprintf("n%d", i+1), "s1", "done", now); err != nil {
			t.Fatal(err)
		}
		if _, err := post("s1", "again", time.Hour, now); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := m.clearNotice(ws, "n5", "s1", "done", now); err != nil {
		t.Fatal(err)
	}
	if _, err := post("s1", "rate", time.Hour, now.Add(30*time.Minute)); err == nil || !strings.Contains(err.Error(), "last hour") {
		t.Fatalf("hourly cap: %v", err)
	}
	if _, err := post("s1", "next hour", time.Hour, now.Add(61*time.Minute)); err != nil {
		t.Fatalf("hourly cap did not roll over: %v", err)
	}
	if _, err := m.clearNotice(ws, "n1", "s2", "again", now); !errors.Is(err, ErrUnknownNotice) {
		t.Fatalf("clearing a cleared notice: %v", err)
	}
	if _, err := m.clearNotice(ws, "n6", "s2", strings.Repeat("r", noticeMaxReason+1), now); err == nil {
		t.Fatal("over-long reason accepted")
	}

	// The board persists outside the worktree, and a fresh manager reads it.
	if _, err := os.Stat(noticesPath(ws)); err != nil {
		t.Fatal(err)
	}
	if got := sessionGitAt(t, ws, "status", "--porcelain", "--untracked-files=all"); strings.TrimSpace(got) != "" {
		t.Fatalf("board left files in the worktree: %s", got)
	}
	reloaded := (&Manager{}).activeNotices(ws, now.Add(61*time.Minute))
	if len(reloaded) == 0 || reloaded[len(reloaded)-1].Text != "next hour" {
		t.Fatalf("reloaded board = %+v", reloaded)
	}
}

// A digest is bounded; changes it leaves out stay pending for the next
// checkpoint, and a clear just before expiry is still reported.
func TestNoticeBoardDigestOverflowAndLateClear(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	ws := t.TempDir()
	m := &Manager{}
	s, _ := newSteerSession()
	s.board = &sessionBoard{m: m, s: s, primary: ws}
	s.coordinationOn = func() bool { return true }
	s.boardOpened, s.boardSeen = true, map[string]bool{}
	now := time.Now()
	total := boardDigestMaxLines + 2
	for i := 0; i < total; i++ {
		n, err := m.postNotice(ws, Notice{Text: fmt.Sprintf("notice %d", i), SessionID: fmt.Sprint("p", i), Actor: "coordinator"}, time.Hour, now)
		if err != nil {
			t.Fatal(err)
		}
		s.boardSeen[n.ID] = true
		if _, err := m.clearNotice(ws, n.ID, "user", "done", now.Add(59*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	first, err := s.drainBoardDigest(now.Add(61 * time.Minute))
	if err != nil || len(first) != 1 || strings.Count(first[0], "- cleared:") != boardDigestMaxLines ||
		!strings.Contains(first[0], "2 more changes follow") {
		t.Fatalf("first digest err=%v: %q", err, first)
	}
	second, err := s.drainBoardDigest(now.Add(62 * time.Minute))
	if err != nil || len(second) != 1 || strings.Count(second[0], "- cleared:") != 2 {
		t.Fatalf("second digest err=%v: %q", err, second)
	}
	if third, _ := s.drainBoardDigest(now.Add(63 * time.Minute)); third != nil {
		t.Fatalf("third digest = %q", third)
	}
}
