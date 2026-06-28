// Package session manages the lifecycle of a daemon session: it binds an event
// log, an emitter, and an agent loop, runs the agent in a goroutine, and accepts
// follow-up input ("prods") and question answers between/within turns (spec §4).
//
// The session's mode selects the agent: "work" runs the orchestrator coordinator
// (which delegates to subagents); anything else runs a single worker agent.
package session

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/whyrusleeping/ycc/internal/config"
	"github.com/whyrusleeping/ycc/internal/docs"
	"github.com/whyrusleeping/ycc/internal/engine"
	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/git"
	"github.com/whyrusleeping/ycc/internal/orchestrator"
	"github.com/whyrusleeping/ycc/internal/project"
	"github.com/whyrusleeping/ycc/internal/usage"
)

// Config parameterizes a new session.
type Config struct {
	Workspace        string
	Mode             string
	InteractionLevel string
	Prompt           string
	// Project, when set, names a registered project whose workspace is used,
	// overriding Workspace (spec §3.1).
	Project string
}

// Session is one running agent conversation backed by a persistent event log.
type Session struct {
	ID               string
	Workspace        string
	Mode             string
	InteractionLevel string

	log       *event.Log
	emitter   *event.Emitter
	loop      *engine.Loop
	inter     *interaction
	deps      *orchestrator.Deps
	reg       *config.Registry
	prompt    string
	buildLoop func(mode, prompt string) (*engine.Loop, error)

	inputCh chan string
	ctx     context.Context
	cancel  context.CancelFunc

	mu          sync.Mutex
	status      event.Status
	coordinator string   // logical model name driving the coordinator
	implementer string   // logical model name for the implementer role
	reviewers   []string // logical model names for the reviewer role
	// thinkLevels holds per-role reasoning overrides (spec §7.4, §18.2) keyed by
	// roleCoordinator/roleImplementer/roleReviewers. An empty/missing entry means
	// "use the per-role config then per-model config"; any of
	// off/low/medium/high/xhigh/max forces that level on the role until changed.
	thinkLevels map[string]string
	// usageSummarized tracks tasks whose usage/cost summary has already been
	// appended to the work log this session, so each accrues at most one summary
	// line even across repeated idle cycles (spec §6.2, §20.5).
	usageSummarized map[string]bool

	// Interrupt & steer state (spec §18.7), guarded by a dedicated mutex so it
	// never contends with the s.mu hot paths. pauseReq is set by Interrupt and
	// consumed at the next checkpoint; paused is true while a checkpoint blocks;
	// resumeReq wakes it (Resume or a steered correction); corrections buffers
	// steered-in user messages to inject on resume; resumeCh is closed to wake.
	steerMu     sync.Mutex
	pauseReq    bool
	paused      bool
	resumeReq   bool
	corrections []string
	resumeCh    chan struct{}
}

// Role name constants used to key per-role thinking overrides and resolution.
const (
	roleCoordinator = config.RoleCoordinator
	roleImplementer = config.RoleImplementer
	roleReviewers   = config.RoleReviewers
)

// Log exposes the session's event log for subscription.
func (s *Session) Log() *event.Log { return s.log }

// Status returns the current lifecycle status.
func (s *Session) Status() event.Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status
}

func (s *Session) setStatus(st event.Status) {
	s.mu.Lock()
	s.status = st
	s.mu.Unlock()
}

// Level returns the session's current interaction level (guarded read).
func (s *Session) Level() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.InteractionLevel
}

func (s *Session) currentLoop() *engine.Loop {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loop
}

func (s *Session) setLoop(l *engine.Loop) {
	s.mu.Lock()
	s.loop = l
	s.mu.Unlock()
}

// SendInput delivers user text. If the agent is currently blocked on a question,
// the text answers it; otherwise it is queued as a follow-up prod for when the
// agent next goes idle.
func (s *Session) SendInput(text string) error {
	if s.inter.Answer(text) {
		return nil
	}
	// If the running loop is paused (or pausing) at a steer checkpoint, buffer the
	// text as a correction (and echo it); the loop drains all buffered corrections
	// in order only on an explicit Resume, so multiple sends land deterministically
	// (§18.7). It does NOT auto-resume here.
	s.steerMu.Lock()
	if s.paused || s.pauseReq {
		s.corrections = append(s.corrections, text)
		s.steerMu.Unlock()
		s.emitter.EmitAs("user", event.UserInput, map[string]any{"text": text})
		return nil
	}
	s.steerMu.Unlock()
	select {
	case s.inputCh <- text:
		// Emit the echo at the moment the prod is accepted so the TUI can
		// display it immediately, rather than waiting for the run loop to
		// dequeue it (which only happens after the current/next agent turn).
		s.emitter.EmitAs("user", event.UserInput, map[string]any{"text": text})
		return nil
	default:
		return fmt.Errorf("session %s input buffer full", s.ID)
	}
}

// Answer responds to a pending question. Errors if none is pending.
func (s *Session) Answer(text string) error {
	if !s.inter.Answer(text) {
		return fmt.Errorf("session %s has no pending question", s.ID)
	}
	return nil
}

// AnswerOption responds to a pending question with a chosen option (resolved by
// index when idx >= 0 and in range) or free text. Errors if none is pending.
func (s *Session) AnswerOption(idx int, text string) error {
	if !s.inter.AnswerOption(idx, text) {
		return fmt.Errorf("session %s has no pending question", s.ID)
	}
	return nil
}

// Stop cancels the session's agent loop and closes its log.
func (s *Session) Stop() {
	s.cancel()
	s.log.Close()
}

// Checkpoint implements engine.Steer (spec §18.7). At a safe checkpoint the
// loop calls it: if no pause is pending it returns immediately (cheap no-op);
// otherwise it marks the session paused, emits interrupted, and blocks until a
// Resume or a steered SendInput wakes it (or ctx is cancelled, returned as a
// normal stop). It returns any steered-in corrections, in order, to append.
func (s *Session) Checkpoint(ctx context.Context) ([]string, error) {
	s.steerMu.Lock()
	if !s.pauseReq {
		s.steerMu.Unlock()
		return nil, nil // cheap no-op fast path
	}
	s.pauseReq = false
	s.paused = true
	s.steerMu.Unlock()

	s.setStatus(event.StatusPaused)
	s.emitter.Emit(event.Interrupted, map[string]any{})

	s.steerMu.Lock()
	for !s.resumeReq {
		s.resumeCh = make(chan struct{})
		ch := s.resumeCh
		s.steerMu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			s.steerMu.Lock()
			s.paused = false
			s.resumeCh = nil
			s.steerMu.Unlock()
			return nil, ctx.Err()
		}
		s.steerMu.Lock()
	}
	corr := s.corrections
	s.corrections = nil
	s.resumeReq = false
	s.paused = false
	s.resumeCh = nil
	s.steerMu.Unlock()

	s.setStatus(event.StatusRunning)
	s.emitter.Emit(event.Resumed, map[string]any{})
	return corr, nil
}

// signalResumeLocked wakes a blocked Checkpoint. Call only while holding steerMu.
func (s *Session) signalResumeLocked() {
	s.resumeReq = true
	if s.resumeCh != nil {
		close(s.resumeCh)
		s.resumeCh = nil
	}
}

// Interrupt requests a graceful pause: the running loop stops at its next safe
// checkpoint (between turns / after a tool result) without aborting a tool
// mid-run (spec §18.7). Resume or a steered SendInput continues it.
func (s *Session) Interrupt() error {
	s.steerMu.Lock()
	s.pauseReq = true
	s.steerMu.Unlock()
	return nil
}

// Resume continues a paused loop with no correction (spec §18.7). It cancels a
// not-yet-effective pause request, and is a no-op if nothing is paused.
func (s *Session) Resume() error {
	s.steerMu.Lock()
	if s.paused {
		s.signalResumeLocked()
	} else {
		s.pauseReq = false
	}
	s.steerMu.Unlock()
	return nil
}

// SetInteractionLevel changes the session's interaction level mid-flight. It
// takes effect at the next gate (the next ask_user) and is recorded in the
// event log so other subscribers see it (spec §11, §18.2).
func (s *Session) SetInteractionLevel(level string) error {
	switch level {
	case "interactive", "judgement", "autonomous":
	default:
		return fmt.Errorf("unknown interaction level %q", level)
	}
	s.mu.Lock()
	from := s.InteractionLevel
	s.InteractionLevel = level
	s.mu.Unlock()
	s.inter.SetLevel(level)
	s.emitter.Emit(event.InteractionLevelChanged, map[string]any{"from": from, "to": level})
	return nil
}

// SetRoleConfig reassigns per-role logical models mid-session and rebuilds the
// relevant gollama clients so the next coordinator turn / next spawned subagent
// uses the new assignment (spec §13, §18.2). Empty coordinator/implementer leaves
// that role unchanged; an empty reviewers slice leaves reviewers unchanged.
func (s *Session) SetRoleConfig(coordinator, implementer string, reviewers []string) error {
	s.mu.Lock()
	newCoord, newImpl, newRevs := s.coordinator, s.implementer, s.reviewers
	s.mu.Unlock()

	if coordinator != "" {
		if !s.reg.Has(coordinator) {
			return fmt.Errorf("unknown model %q", coordinator)
		}
		newCoord = coordinator
	}
	if implementer != "" {
		if !s.reg.Has(implementer) {
			return fmt.Errorf("unknown model %q", implementer)
		}
		newImpl = implementer
	}
	if len(reviewers) > 0 {
		for _, name := range reviewers {
			if !s.reg.Has(name) {
				return fmt.Errorf("unknown model %q", name)
			}
		}
		newRevs = append([]string(nil), reviewers...)
	}

	// Rebuild the implementer / reviewer specs so the next spawn uses the new
	// backends (the running subagents keep their context until then).
	implSpec, err := s.agentSpec(roleImplementer, newImpl)
	if err != nil {
		return err
	}
	var revSpecs []orchestrator.AgentSpec
	for _, name := range newRevs {
		rs, err := s.agentSpec(roleReviewers, name)
		if err != nil {
			return err
		}
		revSpecs = append(revSpecs, rs)
	}
	s.deps.SetImplementer(implSpec)
	s.deps.SetReviewers(revSpecs)

	// Swap the live coordinator loop's backend so its next turn uses the new
	// model while preserving its conversation history.
	client, model, err := s.reg.Build(newCoord)
	if err != nil {
		return fmt.Errorf("build coordinator backend: %w", err)
	}
	s.currentLoop().SetBackend(client, model, newCoord, s.reg.BackendFor(newCoord), s.thinkingFor(roleCoordinator, newCoord))

	s.mu.Lock()
	s.coordinator, s.implementer, s.reviewers = newCoord, newImpl, newRevs
	s.mu.Unlock()

	s.emitter.Emit(event.RoleConfigChanged, map[string]any{
		"coordinator": newCoord,
		"implementer": newImpl,
		"reviewers":   newRevs,
	})
	return nil
}

// SetThinking sets a per-role reasoning override (spec §7.4, §18.2). An empty
// role updates all three roles (back-compat / "all"); a specific role
// ("coordinator"|"implementer"|"reviewers") updates just that one. It updates the
// live coordinator loop and rebuilds the implementer/reviewer specs as relevant so
// the next spawn uses it; the change is recorded in the event log with the role.
func (s *Session) SetThinking(role, level string) error {
	if _, ok := thinkingForLevel(level); !ok {
		return fmt.Errorf("unknown thinking level %q", level)
	}
	switch role {
	case "", roleCoordinator, roleImplementer, roleReviewers:
	default:
		return fmt.Errorf("unknown thinking role %q", role)
	}

	// Determine the affected roles (empty => all three).
	affected := map[string]bool{}
	if role == "" {
		affected[roleCoordinator] = true
		affected[roleImplementer] = true
		affected[roleReviewers] = true
	} else {
		affected[role] = true
	}

	s.mu.Lock()
	if s.thinkLevels == nil {
		s.thinkLevels = map[string]string{}
	}
	from := ""
	if role == "" {
		from = s.thinkLevels[roleCoordinator]
	} else {
		from = s.thinkLevels[role]
	}
	for r := range affected {
		s.thinkLevels[r] = level
	}
	impl, revs := s.implementer, append([]string(nil), s.reviewers...)
	coord := s.coordinator
	s.mu.Unlock()

	// Rebuild the implementer spec so the next spawn uses the new thinking level.
	if affected[roleImplementer] {
		implSpec, err := s.agentSpec(roleImplementer, impl)
		if err != nil {
			return err
		}
		s.deps.SetImplementer(implSpec)
	}
	if affected[roleReviewers] {
		var revSpecs []orchestrator.AgentSpec
		for _, name := range revs {
			rs, err := s.agentSpec(roleReviewers, name)
			if err != nil {
				return err
			}
			revSpecs = append(revSpecs, rs)
		}
		s.deps.SetReviewers(revSpecs)
	}
	if affected[roleCoordinator] {
		// Update the live coordinator loop's reasoning settings for its next turn
		// (its conversation history is preserved).
		s.currentLoop().SetThinking(s.thinkingFor(roleCoordinator, coord))
	}

	emittedRole := role
	if emittedRole == "" {
		emittedRole = "all"
	}
	s.emitter.Emit(event.ThinkingLevelChanged, map[string]any{"role": emittedRole, "from": from, "to": level})
	return nil
}

// thinkingForLevel maps a session thinking level to engine.Thinking. "off"
// disables reasoning entirely; any effort level enables adaptive thinking at that
// effort with summarized display. The bool reports whether the level is valid.
func thinkingForLevel(level string) (engine.Thinking, bool) {
	switch level {
	case "off":
		return engine.Thinking{}, true
	case "low", "medium", "high", "xhigh", "max":
		return engine.Thinking{Thinking: "adaptive", Effort: level, ThinkingDisplay: "summarized"}, true
	default:
		return engine.Thinking{}, false
	}
}

// thinkingFor resolves the reasoning settings for a role/model pair applying the
// documented precedence (spec §7.4): per-role session override → per-role config
// → per-model config → package defaults.
func (s *Session) thinkingFor(role, name string) engine.Thinking {
	s.mu.Lock()
	level := s.thinkLevels[role]
	s.mu.Unlock()
	if level != "" {
		th, _ := thinkingForLevel(level)
		return th
	}
	if lvl, ok := s.reg.RoleThinking(role); ok {
		if th, ok := thinkingForLevel(lvl); ok {
			return th
		}
	}
	th := s.reg.ThinkingFor(name)
	return engine.Thinking{Thinking: th.Thinking, Effort: th.Effort, ThinkingDisplay: th.ThinkingDisplay}
}

// agentSpec builds an orchestrator.AgentSpec for a logical model name in a role.
func (s *Session) agentSpec(role, name string) (orchestrator.AgentSpec, error) {
	_, model, err := s.reg.Build(name)
	if err != nil {
		return orchestrator.AgentSpec{}, fmt.Errorf("build backend %q: %w", name, err)
	}
	th := s.thinkingFor(role, name)
	return orchestrator.AgentSpec{
		Name:    name,
		Model:   model,
		Backend: s.reg.BackendFor(name),
		NewClient: func() engine.Turner {
			c, _, _ := s.reg.Build(name)
			return c
		},
		Thinking:        th.Thinking,
		Effort:          th.Effort,
		ThinkingDisplay: th.ThinkingDisplay,
	}, nil
}

// resolveReviewTier turns a requested tier name into a concrete ReviewPlan,
// resolving the configured tier to reviewer agent specs (spec §13). Unknown
// models are skipped (graceful degradation); a tier that resolves to no agents
// falls back to the session's current reviewer assignment, and finally to
// coordinator self-review if even that is empty.
func (s *Session) resolveReviewTier(requested string) orchestrator.ReviewPlan {
	td := s.reg.ReviewTier(requested)
	plan := orchestrator.ReviewPlan{Tier: td.Name, Requested: requested, Fallback: td.Fallback}
	if td.SelfReview {
		plan.SelfReview = true
		return plan
	}
	models := td.Models
	if len(models) == 0 {
		s.mu.Lock()
		models = append([]string(nil), s.reviewers...)
		s.mu.Unlock()
	}
	for _, name := range models {
		spec, err := s.agentSpec(roleReviewers, name)
		if err != nil {
			continue // skip unbuildable/unknown model — degrade gracefully
		}
		plan.Specs = append(plan.Specs, spec)
	}
	if len(plan.Specs) == 0 {
		s.mu.Lock()
		cur := append([]string(nil), s.reviewers...)
		s.mu.Unlock()
		for _, name := range cur {
			if spec, err := s.agentSpec(roleReviewers, name); err == nil {
				plan.Specs = append(plan.Specs, spec)
			}
		}
		if len(plan.Specs) == 0 {
			plan.SelfReview = true
		}
	}
	return plan
}

func (s *Session) run() {
	s.emitter.Emit(event.SessionStarted, map[string]any{
		"workspace":         s.Workspace,
		"mode":              s.Mode,
		"interaction_level": s.Level(),
	})
	s.emitter.EmitAs("user", event.UserInput, map[string]any{"text": s.prompt})

	for {
		s.setStatus(event.StatusRunning)
		res, err := s.currentLoop().Run(s.ctx)
		if s.ctx.Err() != nil {
			return
		}
		if err != nil {
			s.setStatus(event.StatusError)
			s.emitter.Emit(event.SessionError, map[string]any{"msg": err.Error()})
		} else if res.NextMode != "" && res.NextMode != s.Mode {
			// A control tool requested a mode transition within this session. A
			// carried prompt (e.g. the pm → work hand-off) seeds the new loop
			// verbatim so it carries the target task + planning context; otherwise
			// fall back to a generic per-mode transition prompt.
			seed := res.NextPrompt
			if seed == "" {
				seed = modeTransitionPrompt(res.NextMode)
			}
			if next, berr := s.buildLoop(res.NextMode, seed); berr == nil {
				s.emitter.Emit(event.ModeChanged, map[string]any{"from": s.Mode, "to": res.NextMode})
				s.Mode = res.NextMode
				s.setLoop(next)
				continue // run the new mode's loop immediately
			} else {
				s.emitter.Emit(event.SessionError, map[string]any{"msg": "mode switch failed: " + berr.Error()})
			}
		} else {
			s.setStatus(event.StatusIdle)
			s.emitter.Emit(event.SessionIdle, map[string]any{"report": s.withAssumptions(res.Report)})
			s.summarizeUsage()
		}

		select {
		case text := <-s.inputCh:
			s.currentLoop().Post(text)
		case <-s.ctx.Done():
			return
		}
	}
}

// defaultPrompt is the starting instruction for a mode when the user gives none.
func defaultPrompt(mode string) string {
	switch mode {
	case "chat":
		return "Briefly introduce yourself as the ycc assistant and ask what I'd like to work on."
	case "pm":
		return "Review spec.md and the existing backlog against the current codebase, and ask me what I'd like to plan, document, or groom."
	default: // work
		return "Work on the backlog: choose the next ready task (one whose dependencies are all done) and complete it."
	}
}

func modeTransitionPrompt(mode string) string {
	if mode == "work" {
		return "You are now in work mode. Begin working the backlog: pick the next ready task (one whose dependencies are all done) and drive it to completion."
	}
	return "You are now in " + mode + " mode."
}

// withAssumptions appends any assumptions recorded by autonomous-mode ask_user
// calls to the final report (spec §11).
func (s *Session) withAssumptions(report string) string {
	as := s.inter.Assumptions()
	if len(as) == 0 {
		return report
	}
	var b []byte
	b = append(b, report...)
	b = append(b, "\n\nAssumptions made without consulting the user (autonomous mode):\n"...)
	for _, a := range as {
		b = append(b, "- "+a+"\n"...)
	}
	return string(b)
}

// summarizeUsage appends a one-line usage/cost summary for the session's focused
// task to that task's work log when a work-mode session goes idle (spec §6.2,
// §20.5), so per-task cost accrues in the backlog across sessions. It is
// idempotent per task within a session: at most one line per focused task.
func (s *Session) summarizeUsage() {
	if s.Mode != "work" {
		return
	}
	events := s.log.Snapshot()
	task := event.Reduce(events).FocusTask
	if task == "" {
		return
	}
	s.mu.Lock()
	if s.usageSummarized == nil {
		s.usageSummarized = map[string]bool{}
	}
	already := s.usageSummarized[task]
	s.mu.Unlock()
	if already {
		return
	}

	entries := usage.ReduceEvents(s.ID, events)
	res := usage.Aggregate(entries, s.reg, usage.Options{GroupBy: []usage.Dim{usage.DimTask}})
	var row usage.Row
	found := false
	for _, r := range res.Rows {
		if r.Task == task {
			row, found = r, true
			break
		}
	}
	if !found || row.Tokens.Total == 0 {
		return
	}

	if s.deps != nil && s.deps.Docs != nil {
		if _, err := s.deps.Docs.AppendWorkLog(task, usage.FormatWorkLogLine(row)); err != nil {
			s.emitter.Emit(event.Narration, map[string]any{"msg": "usage summary work-log append failed: " + err.Error()})
			return
		}
	}
	s.mu.Lock()
	s.usageSummarized[task] = true
	s.mu.Unlock()
}

// Manager owns the set of live sessions and the backend registry used to build
// their agent loops.
type Manager struct {
	mu               sync.Mutex
	sessions         map[string]*Session
	reg              *config.Registry
	defaultWorkspace string
	projects         *project.Registry
}

// NewManager creates a session manager backed by the given model registry. It
// starts with an in-memory project registry; call SetProjects to back it with a
// persistent one (spec §3.1).
func NewManager(reg *config.Registry, defaultWorkspace string) *Manager {
	return &Manager{
		sessions:         map[string]*Session{},
		reg:              reg,
		defaultWorkspace: defaultWorkspace,
		projects:         project.NewMemory(),
	}
}

// SetProjects backs the manager with a (persistent) project registry.
func (m *Manager) SetProjects(p *project.Registry) { m.projects = p }

// Projects returns the registered projects (name + path) for ListProjects.
func (m *Manager) Projects() []project.Project { return m.projects.List() }

// AddProject registers a workspace under an optional name (spec §3.1).
func (m *Manager) AddProject(path, name string) (project.Project, error) {
	return m.projects.Add(path, name)
}

// RemoveProject deregisters a project by name.
func (m *Manager) RemoveProject(name string) error { return m.projects.Remove(name) }

// Start creates, persists, and launches a new session.
func (m *Manager) Start(cfg Config) (*Session, error) {
	ws := cfg.Workspace
	// A named project resolves to its registered workspace, overriding ws.
	if cfg.Project != "" {
		p, ok := m.projects.Resolve(cfg.Project)
		if !ok {
			return nil, fmt.Errorf("unknown project %q", cfg.Project)
		}
		ws = p
	}
	if ws == "" {
		ws = m.defaultWorkspace
	}
	absWS, err := filepath.Abs(ws)
	if err != nil {
		return nil, fmt.Errorf("resolve workspace: %w", err)
	}
	// Auto-register a not-yet-known workspace so it becomes a first-class,
	// listable project (spec §3.1).
	if _, err := m.projects.EnsureWorkspace(absWS); err != nil {
		return nil, fmt.Errorf("register project: %w", err)
	}
	mode := cfg.Mode
	if mode == "" {
		mode = "work"
	}
	level := cfg.InteractionLevel
	if level == "" {
		level = "judgement"
	}
	// The prompt is optional: an empty one (e.g. "work" mode with no suggested
	// task) gets a sensible per-mode default so the agent has a starting point.
	prompt := strings.TrimSpace(cfg.Prompt)
	if prompt == "" {
		prompt = defaultPrompt(mode)
	}

	id, err := newID()
	if err != nil {
		return nil, err
	}
	logPath := filepath.Join(absWS, ".ycc", "sessions", id, "events.jsonl")
	log, err := event.OpenLog(logPath)
	if err != nil {
		return nil, fmt.Errorf("open event log: %w", err)
	}

	emitter := event.NewEmitter(log, "coordinator")
	inter := newInteraction(level, emitter)
	repo, err := git.Open(absWS)
	if err != nil {
		return nil, fmt.Errorf("prepare git workspace: %w", err)
	}
	coordName := m.reg.CoordinatorName()
	implName := m.reg.ImplementerName()
	reviewerNames := append([]string(nil), m.reg.ReviewerNames()...)
	implSpec, err := m.agentSpec(roleImplementer, implName)
	if err != nil {
		return nil, err
	}
	var reviewers []orchestrator.AgentSpec
	for _, name := range reviewerNames {
		rs, err := m.agentSpec(roleReviewers, name)
		if err != nil {
			return nil, err
		}
		reviewers = append(reviewers, rs)
	}
	deps := &orchestrator.Deps{
		Workspace:   absWS,
		Docs:        docs.NewStore(absWS),
		Repo:        repo,
		Emitter:     emitter,
		Implementer: implSpec,
		Reviewers:   reviewers,
		Asker:       inter,
		MaxTok:      m.reg.MaxTokens(),
		MaxTurns:    m.reg.MaxTurns(),
	}

	ctx, cancel := context.WithCancel(context.Background())
	s := &Session{
		ID:               id,
		Workspace:        absWS,
		Mode:             mode,
		InteractionLevel: level,
		log:              log,
		emitter:          emitter,
		inter:            inter,
		deps:             deps,
		reg:              m.reg,
		prompt:           prompt,
		inputCh:          make(chan string, 64),
		ctx:              ctx,
		cancel:           cancel,
		status:           event.StatusRunning,
		coordinator:      coordName,
		implementer:      implName,
		reviewers:        reviewerNames,
		thinkLevels:      map[string]string{},
		usageSummarized:  map[string]bool{},
	}

	// buildLoop assembles the agent loop for a mode; reused on mode transitions.
	// It reads the session's current coordinator assignment so a mid-session
	// role-config change drives the next coordinator loop (spec §18.2).
	s.buildLoop = func(mode, prompt string) (*engine.Loop, error) {
		reg, sys := orchestrator.BuildMode(mode, deps, s.inter.Level())
		s.mu.Lock()
		coord := s.coordinator
		s.mu.Unlock()
		client, model, err := m.reg.Build(coord)
		if err != nil {
			return nil, fmt.Errorf("build coordinator backend: %w", err)
		}
		th := s.thinkingFor(roleCoordinator, coord)
		loop := &engine.Loop{
			Client: client, Model: model, ModelName: coord, Backend: m.reg.BackendFor(coord),
			System: sys, Tools: reg, Emitter: emitter,
			MaxTok: m.reg.MaxTokens(), MaxTurns: m.reg.MaxTurns(),
			Thinking: th.Thinking, Effort: th.Effort, ThinkingDisplay: th.ThinkingDisplay,
		}
		loop.Steer = s
		loop.Seed(prompt)
		return loop, nil
	}
	loop, err := s.buildLoop(mode, prompt)
	if err != nil {
		return nil, err
	}
	s.loop = loop

	// Wire the review-tier resolver so spawn_reviewers can pick a tier per change
	// (spec §13). It resolves the configured tier to concrete reviewer specs.
	deps.ReviewTier = func(name string) orchestrator.ReviewPlan {
		return s.resolveReviewTier(name)
	}

	m.mu.Lock()
	m.sessions[id] = s
	m.mu.Unlock()

	go s.run()
	return s, nil
}

// agentSpec builds an orchestrator.AgentSpec for a logical model name in a role,
// validating that it resolves now so the per-spawn closures can assume success. It
// honors the per-role thinking config (per-role config → per-model config →
// defaults); session-level overrides are layered on later via Session.SetThinking.
func (m *Manager) agentSpec(role, name string) (orchestrator.AgentSpec, error) {
	_, model, err := m.reg.Build(name)
	if err != nil {
		return orchestrator.AgentSpec{}, fmt.Errorf("build backend %q: %w", name, err)
	}
	th := m.thinkingFor(role, name)
	return orchestrator.AgentSpec{
		Name:    name,
		Model:   model,
		Backend: m.reg.BackendFor(name),
		NewClient: func() engine.Turner {
			c, _, _ := m.reg.Build(name)
			return c
		},
		Thinking:        th.Thinking,
		Effort:          th.Effort,
		ThinkingDisplay: th.ThinkingDisplay,
	}, nil
}

// thinkingFor resolves reasoning settings for a role/model pair at startup,
// applying per-role config → per-model config → defaults (the session-level
// override layer applies once the session is live).
func (m *Manager) thinkingFor(role, name string) config.Thinking {
	if lvl, ok := m.reg.RoleThinking(role); ok {
		if th, ok := thinkingForLevel(lvl); ok {
			return config.Thinking{Thinking: th.Thinking, Effort: th.Effort, ThinkingDisplay: th.ThinkingDisplay}
		}
	}
	return m.reg.ThinkingFor(name)
}

// Get returns a session by id.
func (m *Manager) Get(id string) (*Session, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	return s, ok
}

// List returns all live sessions.
func (m *Manager) List() []*Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		out = append(out, s)
	}
	return out
}

// ListByProject returns live sessions whose workspace is the named project's
// workspace. An empty name returns all sessions; an unknown name returns none.
func (m *Manager) ListByProject(name string) []*Session {
	if name == "" {
		return m.List()
	}
	ws, ok := m.projects.Resolve(name)
	if !ok {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		if s.Workspace == ws {
			out = append(out, s)
		}
	}
	return out
}

// Models returns the configured logical models for the settings overlay pickers.
func (m *Manager) Models() []config.ModelInfo { return m.reg.Models() }

// GetModel returns a copy of a logical model's record for editing in the
// settings overlay (spec §18.2).
func (m *Manager) GetModel(name string) (config.Model, bool) { return m.reg.GetModel(name) }

// UpsertModel adds or replaces a logical model backend at runtime; persist also
// writes ycc.toml (spec §18.2).
func (m *Manager) UpsertModel(name string, mdl config.Model, persist bool) error {
	return m.reg.UpsertModel(name, mdl, persist)
}

// RemoveModel deletes a logical model backend (rejected if a role references
// it); persist also writes ycc.toml (spec §18.2).
func (m *Manager) RemoveModel(name string, persist bool) error {
	return m.reg.RemoveModel(name, persist)
}

// Backlog returns a docs.Store for the given project's workspace (empty => the
// daemon default workspace). Used by the read-only backlog RPCs (spec §18.5).
func (m *Manager) Backlog(project string) (*docs.Store, error) {
	ws := m.defaultWorkspace
	if project != "" {
		p, ok := m.projects.Resolve(project)
		if !ok {
			return nil, fmt.Errorf("unknown project %q", project)
		}
		ws = p
	}
	absWS, err := filepath.Abs(ws)
	if err != nil {
		return nil, fmt.Errorf("resolve workspace: %w", err)
	}
	return docs.NewStore(absWS), nil
}

// CaptureBacklogItem runs the lightweight, off-stream "quick-add backlog item"
// capture agent for a project (task 0016, spec §18.2): it turns a natural-language
// description into a structured backlog task without disturbing any running
// session. The agent may ask ONE clarifying question (returned via Question);
// the client re-invokes with priorQuestion/priorAnswer so it creates the task.
// project empty => the daemon default workspace; backlog writes are serialized
// in docs.Store so a concurrent work session can't corrupt the index.
func (m *Manager) CaptureBacklogItem(project, description, priorQuestion, priorAnswer string) (orchestrator.CaptureResult, error) {
	if strings.TrimSpace(description) == "" {
		return orchestrator.CaptureResult{}, fmt.Errorf("description is required")
	}
	ws := m.defaultWorkspace
	if project != "" {
		p, ok := m.projects.Resolve(project)
		if !ok {
			return orchestrator.CaptureResult{}, fmt.Errorf("%w %q", ErrUnknownProject, project)
		}
		ws = p
	}
	absWS, err := filepath.Abs(ws)
	if err != nil {
		return orchestrator.CaptureResult{}, fmt.Errorf("resolve workspace: %w", err)
	}
	store := docs.NewStore(absWS)
	coord := m.reg.CoordinatorName()
	client, model, err := m.reg.Build(coord)
	if err != nil {
		return orchestrator.CaptureResult{}, fmt.Errorf("build capture backend: %w", err)
	}
	cd := orchestrator.CaptureDeps{
		Workspace: absWS,
		Docs:      store,
		Client:    client,
		Model:     model,
		ModelName: coord,
		Backend:   m.reg.BackendFor(coord),
		Thinking:  engine.Thinking{}, // reasoning OFF for a fast capture
		MaxTok:    m.reg.MaxTokens(),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	return orchestrator.RunCapture(ctx, cd, description, priorQuestion, priorAnswer)
}

// ErrUnknownProject indicates a project name did not resolve to a registered
// workspace. It is a client-input error (distinct from IO/scan failures) so RPC
// handlers can map it to an invalid-argument code rather than internal.
var ErrUnknownProject = errors.New("unknown project")

// UsageReport scans the given project's workspace (empty => the daemon default
// workspace) for session event logs and returns the aggregated, priced usage
// breakdown (spec §20.3, §20.5). Pricing comes from the daemon's model registry.
func (m *Manager) UsageReport(project string, opts usage.Options) (*usage.Result, error) {
	ws := m.defaultWorkspace
	if project != "" {
		p, ok := m.projects.Resolve(project)
		if !ok {
			return nil, fmt.Errorf("%w %q", ErrUnknownProject, project)
		}
		ws = p
	}
	absWS, err := filepath.Abs(ws)
	if err != nil {
		return nil, fmt.Errorf("resolve workspace: %w", err)
	}
	entries, err := usage.Scan(absWS)
	if err != nil {
		return nil, err
	}
	res := usage.Aggregate(entries, m.reg, opts)
	res.Workspace = absWS
	return &res, nil
}

func newID() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "s_" + hex.EncodeToString(b), nil
}
