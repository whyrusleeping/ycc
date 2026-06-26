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
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	"github.com/whyrusleeping/ycc/internal/config"
	"github.com/whyrusleeping/ycc/internal/docs"
	"github.com/whyrusleeping/ycc/internal/engine"
	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/git"
	"github.com/whyrusleeping/ycc/internal/orchestrator"
)

// Config parameterizes a new session.
type Config struct {
	Workspace        string
	Mode             string
	InteractionLevel string
	Prompt           string
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
}

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
	select {
	case s.inputCh <- text:
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
	implSpec, err := s.agentSpec(newImpl)
	if err != nil {
		return err
	}
	var revSpecs []orchestrator.AgentSpec
	for _, name := range newRevs {
		rs, err := s.agentSpec(name)
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
	s.currentLoop().SetBackend(client, model)

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

// agentSpec builds an orchestrator.AgentSpec for a logical model name.
func (s *Session) agentSpec(name string) (orchestrator.AgentSpec, error) {
	_, model, err := s.reg.Build(name)
	if err != nil {
		return orchestrator.AgentSpec{}, fmt.Errorf("build backend %q: %w", name, err)
	}
	return orchestrator.AgentSpec{
		Name:  name,
		Model: model,
		NewClient: func() engine.Turner {
			c, _, _ := s.reg.Build(name)
			return c
		},
	}, nil
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
		}

		select {
		case text := <-s.inputCh:
			s.emitter.EmitAs("user", event.UserInput, map[string]any{"text": text})
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

// Manager owns the set of live sessions and the backend registry used to build
// their agent loops.
type Manager struct {
	mu               sync.Mutex
	sessions         map[string]*Session
	reg              *config.Registry
	defaultWorkspace string
}

// NewManager creates a session manager backed by the given model registry.
func NewManager(reg *config.Registry, defaultWorkspace string) *Manager {
	return &Manager{
		sessions:         map[string]*Session{},
		reg:              reg,
		defaultWorkspace: defaultWorkspace,
	}
}

// Start creates, persists, and launches a new session.
func (m *Manager) Start(cfg Config) (*Session, error) {
	ws := cfg.Workspace
	if ws == "" {
		ws = m.defaultWorkspace
	}
	absWS, err := filepath.Abs(ws)
	if err != nil {
		return nil, fmt.Errorf("resolve workspace: %w", err)
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
	implSpec, err := m.agentSpec(implName)
	if err != nil {
		return nil, err
	}
	var reviewers []orchestrator.AgentSpec
	for _, name := range reviewerNames {
		rs, err := m.agentSpec(name)
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
		loop := &engine.Loop{Client: client, Model: model, System: sys, Tools: reg, Emitter: emitter, MaxTok: m.reg.MaxTokens(), MaxTurns: m.reg.MaxTurns()}
		loop.Seed(prompt)
		return loop, nil
	}
	loop, err := s.buildLoop(mode, prompt)
	if err != nil {
		return nil, err
	}
	s.loop = loop

	m.mu.Lock()
	m.sessions[id] = s
	m.mu.Unlock()

	go s.run()
	return s, nil
}

// agentSpec builds an orchestrator.AgentSpec for a logical model name, validating
// that it resolves now so the per-spawn closures can assume success.
func (m *Manager) agentSpec(name string) (orchestrator.AgentSpec, error) {
	_, model, err := m.reg.Build(name)
	if err != nil {
		return orchestrator.AgentSpec{}, fmt.Errorf("build backend %q: %w", name, err)
	}
	return orchestrator.AgentSpec{
		Name:  name,
		Model: model,
		NewClient: func() engine.Turner {
			c, _, _ := m.reg.Build(name)
			return c
		},
	}, nil
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

// Models returns the configured logical models for the settings overlay pickers.
func (m *Manager) Models() []config.ModelInfo { return m.reg.Models() }

func newID() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "s_" + hex.EncodeToString(b), nil
}
