// Package session manages the lifecycle of a daemon session: it binds an event
// log, an emitter, and an agent loop, runs the agent in a goroutine, and accepts
// follow-up input ("prods") between turns (spec §4, §5).
//
// M1 runs a single worker agent per session (the coordinator modes arrive in
// later milestones); the value proven here is the daemon/client seam.
package session

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"sync"

	"github.com/whyrusleeping/ycc/internal/docs"
	"github.com/whyrusleeping/ycc/internal/engine"
	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/git"
	"github.com/whyrusleeping/ycc/internal/orchestrator"
	"github.com/whyrusleeping/ycc/internal/tools"
)

const workerSystemPrompt = `You are an autonomous coding agent working inside a single workspace directory.

You complete the user's task using the provided tools to read, search, and modify
files and to run shell commands. Inspect the workspace before changing it, make the
smallest change that satisfies the task, and verify your work when feasible.

When the current task is complete, call the finish tool with a concise report. The
session stays open afterward: the user may send follow-up instructions, which you
should then carry out.`

// ClientFactory builds a fresh backend client for a session. A factory (rather
// than a shared client) avoids data races on per-client state across sessions.
type ClientFactory func() engine.Turner

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

	log     *event.Log
	emitter *event.Emitter
	loop    *engine.Loop
	prompt  string

	inputCh chan string
	ctx     context.Context
	cancel  context.CancelFunc

	mu     sync.Mutex
	status event.Status
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

// SendInput queues a follow-up instruction for the agent. It is consumed when
// the agent next becomes idle. Returns an error if the input buffer is full.
func (s *Session) SendInput(text string) error {
	select {
	case s.inputCh <- text:
		return nil
	default:
		return fmt.Errorf("session %s input buffer full", s.ID)
	}
}

// Stop cancels the session's agent loop and closes its log.
func (s *Session) Stop() {
	s.cancel()
	s.log.Close()
}

func (s *Session) run() {
	s.emitter.Emit(event.SessionStarted, map[string]any{
		"workspace":         s.Workspace,
		"mode":              s.Mode,
		"interaction_level": s.InteractionLevel,
	})
	s.emitter.EmitAs("user", event.UserInput, map[string]any{"text": s.prompt})

	for {
		s.setStatus(event.StatusRunning)
		res, err := s.loop.Run(s.ctx)
		if s.ctx.Err() != nil {
			return
		}
		if err != nil {
			s.setStatus(event.StatusError)
			s.emitter.Emit(event.SessionError, map[string]any{"msg": err.Error()})
		} else {
			s.setStatus(event.StatusIdle)
			s.emitter.Emit(event.SessionIdle, map[string]any{"report": res.Report})
		}

		select {
		case text := <-s.inputCh:
			s.emitter.EmitAs("user", event.UserInput, map[string]any{"text": text})
			s.loop.Post(text)
		case <-s.ctx.Done():
			return
		}
	}
}

// Manager owns the set of live sessions and the backend configuration used to
// build their agent loops.
type Manager struct {
	mu               sync.Mutex
	sessions         map[string]*Session
	newClient        ClientFactory
	model            string
	maxTok           int
	defaultWorkspace string
}

// NewManager creates a session manager. newClient builds a backend per session;
// model and maxTok configure the agent loop; defaultWorkspace is used when a
// StartSession request leaves the workspace empty.
func NewManager(newClient ClientFactory, model string, maxTok int, defaultWorkspace string) *Manager {
	return &Manager{
		sessions:         map[string]*Session{},
		newClient:        newClient,
		model:            model,
		maxTok:           maxTok,
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

	id, err := newID()
	if err != nil {
		return nil, err
	}

	logPath := filepath.Join(absWS, ".ycc", "sessions", id, "events.jsonl")
	log, err := event.OpenLog(logPath)
	if err != nil {
		return nil, fmt.Errorf("open event log: %w", err)
	}

	// The mode selects the agent: "work" runs the orchestrator coordinator (which
	// delegates to subagents); anything else runs a single worker agent.
	var (
		reg     *tools.Registry
		sys     string
		emitter *event.Emitter
	)
	if mode == "work" {
		repo, err := git.Open(absWS)
		if err != nil {
			return nil, fmt.Errorf("prepare git workspace: %w", err)
		}
		emitter = event.NewEmitter(log, "coordinator")
		reg = orchestrator.CoordinatorTools(&orchestrator.Deps{
			Workspace:     absWS,
			Docs:          docs.NewStore(absWS),
			Repo:          repo,
			Emitter:       emitter,
			NewClient:     func() engine.Turner { return m.newClient() },
			Model:         m.model,
			ReviewerModel: "claude",
			MaxTok:        m.maxTok,
		})
		sys = orchestrator.CoordinatorSystem()
	} else {
		emitter = event.NewEmitter(log, "agent")
		reg = tools.New()
		reg.Add(tools.Worker(&tools.Workspace{Root: absWS})...)
		sys = workerSystemPrompt
	}

	loop := &engine.Loop{
		Client:  m.newClient(),
		Model:   m.model,
		System:  sys,
		Tools:   reg,
		Emitter: emitter,
		MaxTok:  m.maxTok,
	}
	loop.Seed(cfg.Prompt)

	ctx, cancel := context.WithCancel(context.Background())
	s := &Session{
		ID:               id,
		Workspace:        absWS,
		Mode:             mode,
		InteractionLevel: level,
		log:              log,
		emitter:          emitter,
		loop:             loop,
		prompt:           cfg.Prompt,
		inputCh:          make(chan string, 64),
		ctx:              ctx,
		cancel:           cancel,
		status:           event.StatusRunning,
	}

	m.mu.Lock()
	m.sessions[id] = s
	m.mu.Unlock()

	go s.run()
	return s, nil
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

func newID() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "s_" + hex.EncodeToString(b), nil
}
