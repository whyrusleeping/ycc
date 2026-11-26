// Package daemon hosts the ycc service (session manager + Connect-RPC) and the
// client-side helpers to reach it. Persistence is opt-in: a one-shot in-process
// daemon (StartInProcess) is tied to the client's lifetime, while `ycc daemon`
// (Serve) runs a persistent one.
package daemon

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"connectrpc.com/connect"

	"github.com/whyrusleeping/ycc/internal/config"
	"github.com/whyrusleeping/ycc/internal/docs"
	"github.com/whyrusleeping/ycc/internal/health"
	"github.com/whyrusleeping/ycc/internal/notify"
	"github.com/whyrusleeping/ycc/internal/project"
	"github.com/whyrusleeping/ycc/internal/server"
	"github.com/whyrusleeping/ycc/internal/session"
	"github.com/whyrusleeping/ycc/internal/uianalytics"
	"github.com/whyrusleeping/ycc/internal/web"
	"github.com/whyrusleeping/ycc/internal/workstream"
	"github.com/whyrusleeping/ycc/proto/ycc/v1/yccv1connect"
)

// Options configures the daemon.
type Options struct {
	Addr      string
	Workspace string
	// WorkspaceExplicit: Workspace was named by the user (--workspace), so a
	// persistent daemon registers it even when it is not a git repository.
	WorkspaceExplicit bool
	ConfigPath        string // TOML config; empty => single Anthropic backend from Model/BaseURL/KeyEnv
	Model             string
	BaseURL           string
	KeyEnv            string
	MaxTokens         int
	Token             string
	// TokenFile is where Token was read from when the daemon generated it, so
	// startup can tell the user where to find it; empty for a supplied token.
	TokenFile string
	TLSCert   string
	TLSKey    string
	// Persist enables the durable project registry in the daemon state dir.
	// The one-shot in-process path leaves this false: cwd is the
	// single implicit project and nothing is written to the state dir.
	Persist bool
	// Web serves the embedded web client (internal/web) at "/" alongside the
	// Connect handler. The static assets
	// are unauthenticated; the RPC surface stays behind the bearer
	// AuthInterceptor unchanged. Off by default and forced off for the one-shot
	// in-process path (that daemon is a private loopback backing one CLI
	// command, not a remote-access surface).
	Web bool
	// LocalToken makes a loopback Serve with no Token create (or reuse) the
	// private local token file once it owns the state directory, so a daemon that
	// fails to lock or bind writes nothing.
	LocalToken bool
	// ShutdownDrain bounds how long graceful shutdown (SIGINT/SIGTERM) waits for
	// active model turns, foreground tools, and subagents to reach a clean
	// boundary; 0 skips the drain.
	ShutdownDrain time.Duration
}

// daemonParts is what buildHandler assembles: the HTTP handler and session
// manager, plus the persistent daemon's background services, which Serve starts
// only in its last startup phase.
type daemonParts struct {
	handler http.Handler
	mgr     *session.Manager
	reg     *config.Registry
	health  *health.Monitor // nil unless persistent
}

// buildHandler constructs the session manager and Connect HTTP handler from
// Options. It is shared by the foreground/persistent Serve path and the
// one-shot in-process path so both expose an identical tool surface. A non-nil
// adm gates the RPC surface (persistent daemons). A persistent manager is built
// with execution held, and starts no recovery side effects or background
// workers here: Serve's later startup phases do (startPersistent).
func buildHandler(o Options, adm *server.Admission) (*daemonParts, error) {
	var cfg *config.Config
	if o.ConfigPath != "" {
		c, err := config.Load(o.ConfigPath)
		if err != nil {
			return nil, fmt.Errorf("load config: %w", err)
		}
		cfg = c
		log.Printf("loaded config %s: coordinator=%s implementer=%s reviewers=%v",
			o.ConfigPath, cfg.Roles.Coordinator, cfg.Roles.Implementer, cfg.Roles.Reviewers)
	} else {
		cfg = config.DefaultAnthropic(o.BaseURL, o.Model, o.KeyEnv, o.MaxTokens)
		log.Printf("using single Anthropic backend (model=%s)", o.Model)
	}

	reg := config.NewRegistry(cfg)
	if o.ConfigPath == "" {
		reg.MarkBuiltinFallback()
	}
	reg.SetPath(persistPath(o))
	mgr := session.NewManager(reg, o.Workspace)
	if o.Persist {
		mgr.HoldExecution()
	}
	// Daemon-side push notifier: best-effort webhook that reaches out
	// when an agent needs the user. notify.New returns nil when unconfigured
	// (empty url), so this is a no-op by default; applies to both the persistent
	// and one-shot in-process paths.
	mgr.SetNotifier(notify.New(reg.Notify()))
	// A persistent daemon backs its project registry with durable state so the
	// project list survives restarts. The one-shot path keeps the
	// manager's in-memory registry, already containing cwd as its sole project.
	if o.Persist {
		preg, err := project.Open(project.StateFile())
		if err != nil {
			return nil, fmt.Errorf("load project registry: %w", err)
		}
		mgr.SetProjects(preg)
		mgr.SetIDStateFile(docs.DefaultIDStateFile())
		mgr.SetQuickRepliesFile(filepath.Join(filepath.Dir(project.StateFile()), "quick-replies.json"))
		mgr.SetPublicationsFile(filepath.Join(filepath.Dir(project.StateFile()), "publications.json"))
		// The startup workspace is not a privileged "default"; register it as a
		// normal named project so every RPC/UI uses the same project model. A
		// daemon is often started from wherever the shell happens to be, so only
		// a named directory or a git repository is taken as meant to be a project.
		if o.Workspace != "" {
			if why := startupProjectSkipReason(o.Workspace, o.WorkspaceExplicit); why != "" {
				dir, _ := filepath.Abs(o.Workspace)
				log.Printf("not registering %s as a project: %s", dir, why)
			} else if _, err := mgr.AddProject(o.Workspace, ""); err != nil {
				return nil, fmt.Errorf("register startup project: %w", err)
			}
		}
		// The workstream registry (parallel worktrees) is likewise durable; Serve
		// reconciles it in its startup phases.
		wreg, err := workstream.Open(workstream.StateFile())
		if err != nil {
			return nil, fmt.Errorf("load workstream registry: %w", err)
		}
		mgr.SetWorkstreams(wreg, workstream.DefaultWorktreesRoot())
	}
	parts := &daemonParts{mgr: mgr, reg: reg}
	srv := server.New(mgr)
	// Client usage analytics are durable only on a persistent daemon; the
	// one-shot path writes nothing to the state dir. A store failure is
	// non-fatal: analytics are best-effort.
	if o.Persist {
		if store, err := uianalytics.Open(uianalytics.DefaultDir()); err != nil {
			log.Printf("usage analytics disabled: %v", err)
		} else {
			srv.SetAnalytics(store)
		}
		// Dependency health checks make (small) provider requests, so only the
		// long-lived daemon runs them, from its last startup phase.
		mon := health.New(health.Targets(health.Sources{
			Registry: reg,
			DiskPaths: func() []string {
				paths := []string{filepath.Dir(project.StateFile())}
				for _, p := range mgr.Projects() {
					paths = append(paths, p.Path)
				}
				return paths
			},
		}))
		health.Install(mon)
		srv.SetHealth(mon)
		parts.health = mon
	}

	mux := http.NewServeMux()
	latency := server.NewLatencyRecorder()
	path, handler := yccv1connect.NewSessionServiceHandler(
		srv,
		connect.WithInterceptors(server.NewAuthInterceptor(o.Token), latency),
		connect.WithReadMaxBytes(rpcReadMaxBytes),
	)
	if adm != nil {
		handler = adm.Handler(path, handler)
	}
	mux.Handle(path, server.RequireBearer(o.Token, handler))
	mux.Handle("GET /debug/latency", server.RequireBearer(o.Token, latency.LatencyHandler(o.Token)))
	// Optionally serve the embedded web client at "/". http.ServeMux
	// longest-prefix routing keeps RPC traffic on the Connect handler's
	// "/ycc.v1.SessionService/" prefix; everything else falls to the asset
	// handler, which serves index.html for client-side routes. The assets are
	// unauthenticated by design.
	if o.Web {
		mux.Handle("/", web.Handler())
	}
	parts.handler = mux
	if o.Token == "" {
		parts.handler = server.RequireLoopbackHost(mux)
	}
	return parts, nil
}

// startPersistent runs a persistent daemon's startup phases 2-4 (docs/design/
// daemon-restart.md §1) after Serve owns the state directory and its listener:
// static worktree validation; prepared recovery with execution held (sessions
// registered meanwhile do not run yet); then release of execution, workstream
// readiness derivation, watches, the GC reaper, and the health monitor. It
// returns a stop for the services the manager does not own. It skips phase 4
// once stopping reports a requested shutdown.
func startPersistent(p *daemonParts, stopping func() bool) (stop func()) {
	// Phase 2: validate static state. Prune worktrees a crashed daemon left and
	// mark orphaned workstreams stale, deriving no readiness yet. A reconcile
	// error is non-fatal (log and continue).
	if err := p.mgr.ValidateWorkstreamTrees(); err != nil {
		log.Printf("workstream reconcile: %v", err)
	}
	// Phase 3: prepare recovered state. Execution has been held since the
	// manager was built, so anything prepared here waits for the release below.
	// A shutdown requested by now releases nothing.
	if stopping() {
		return func() {}
	}
	// Phase 4: release execution, then background scheduling.
	p.mgr.ReleaseExecution()
	p.mgr.DeriveWorkstreamReadiness()
	// Watches are recurring checks that outlive any session, so only a
	// persistent daemon schedules them.
	p.mgr.StartWatches()
	// A persistent daemon outlives any single session, so it runs the
	// background GC reaper when enabled in config: idle-session
	// reaping and/or on-disk log retention. Both default to 0 (disabled), so
	// this is a no-op unless explicitly opted in. The one-shot in-process path
	// is short-lived and carries no GC config, so it leaves GC off.
	if iv, idle, ret := p.reg.GC(); idle > 0 || ret > 0 {
		p.mgr.StartReaper(context.Background(), session.GCConfig{Interval: iv, IdleTimeout: idle, LogRetention: ret})
	}
	ctx, cancel := context.WithCancel(context.Background())
	if p.health != nil {
		// Dependency health checks make (small) provider requests, so only the
		// long-lived daemon runs them.
		go p.health.Run(ctx)
	}
	return cancel
}

// Allow four 5 MiB images base64-encoded in JSON (~27 MiB), plus message metadata.
const rpcReadMaxBytes = 32 << 20

func newHTTPServer(addr string, handler http.Handler) *http.Server {
	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	protocols.SetHTTP2(true)
	// Native prior-knowledge h2c avoids buffering HTTP/1 Upgrade bodies.
	protocols.SetUnencryptedHTTP2(true)
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		Protocols:         protocols,
		HTTP2:             &http.HTTP2Config{MaxConcurrentStreams: 250},
		ReadHeaderTimeout: 10 * time.Second,
		// Also inherited by HTTP/2; active Subscribe streams are not idle.
		IdleTimeout:    3 * time.Minute,
		MaxHeaderBytes: 64 << 10,
		// No ReadTimeout or WriteTimeout: Subscribe streams may be long-lived.
	}
}

// Serve builds the registry, session manager, and Connect handler and serves
// until SIGINT or SIGTERM, then shuts down gracefully. It blocks. Used by the
// explicit, persistent `ycc daemon`.
//
// Startup (docs/design/daemon-restart.md §1) first takes exclusive ownership:
// the state-directory lock and the bound listener, before any state mutation, so
// a second daemon or a busy port exits having changed nothing. RPCs are refused
// with Unavailable until the remaining startup phases (startPersistent) finish.
func Serve(o Options) error {
	if o.Web && o.Token == "" && !(o.LocalToken && IsLoopback(o.Addr)) {
		return fmt.Errorf("refusing to serve the web client without a token")
	}
	usingTLS := o.TLSCert != "" && o.TLSKey != ""
	if !IsLoopback(o.Addr) {
		if o.Token == "" {
			return fmt.Errorf("refusing to bind non-loopback address %s without a token", o.Addr)
		}
		if !usingTLS {
			log.Printf("warning: binding non-loopback address %s without TLS; traffic is cleartext", o.Addr)
		}
	}

	// Phase 1: exclusive ownership and a bound listener.
	if o.Persist {
		release, err := acquireStateLock(StateLockFile())
		if err != nil {
			return err
		}
		defer release()
	}
	ln, err := net.Listen("tcp", o.Addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", o.Addr, err)
	}
	defer ln.Close()
	// From here on a signal is a graceful shutdown request, even mid-startup.
	// The first one lands in stopReq, which startup polls between phases; later
	// ones stay on sigs for gracefulShutdown.
	sigs := make(chan os.Signal, 4)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigs)
	stopReq := make(chan os.Signal, 1)
	go func() { stopReq <- <-sigs }()
	stopping := func() bool { return len(stopReq) > 0 }
	if o.Token == "" && o.LocalToken && IsLoopback(o.Addr) {
		token, err := EnsureLocalToken()
		if err != nil {
			return err
		}
		o.Token, o.TokenFile = token, LocalTokenFile()
	}

	adm := server.NewAdmission()
	parts, err := buildHandler(o, adm)
	if err != nil {
		return err
	}
	mgr := parts.mgr
	httpSrv := newHTTPServer(o.Addr, parts.handler)
	serveErr := make(chan error, 1)
	go func() {
		if usingTLS {
			serveErr <- httpSrv.ServeTLS(ln, o.TLSCert, o.TLSKey)
		} else {
			serveErr <- httpSrv.Serve(ln)
		}
	}()

	// A shutdown requested during startup releases nothing further and never
	// opens admission.
	stopServices := func() {}
	if o.Persist && !stopping() {
		stopServices = startPersistent(parts, stopping)
	}
	var sig os.Signal
	if stopping() {
		sig = <-stopReq
	} else {
		adm.Open()
		log.Printf("ycc daemon listening on %s (tls=%v)", ln.Addr(), usingTLS)
		for _, line := range startupGuidance(o, mgr, usingTLS) {
			log.Print(line)
		}
		select {
		case err := <-serveErr:
			// The listener failed (e.g. an unreadable TLS key): nothing is
			// reachable, so skip the drain but still suspend sessions cleanly.
			fenceCtx, cancelFence := context.WithTimeout(context.Background(), listenerFailFenceWait)
			adm.Fence(fenceCtx)
			cancelFence()
			stopServices()
			mgr.ShutdownForRestart()
			return err
		case sig = <-stopReq:
		}
	}
	log.Printf("received %s: shutting down (drain up to %s; signal again to skip it)", sig, o.ShutdownDrain)
	gracefulShutdown(o.ShutdownDrain, sigs, adm, mgr, stopServices)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(ctx); err != nil {
		// Long-lived streams (Subscribe) do not end by themselves.
		_ = httpSrv.Close()
	}
	log.Print("ycc daemon stopped")
	return nil
}

// listenerFailFenceWait bounds how long a daemon whose listener failed waits
// for RPCs already admitted before it suspends sessions.
const listenerFailFenceWait = 5 * time.Second

// gracefulShutdown fences RPCs, stops background workers, drains active work,
// and then suspends every session for restart. One deadline, drain after the
// signal, bounds the fence and the drain together; a second signal on sigs ends
// the wait. A signal arriving after the drain ended exits the process at once,
// in case teardown hangs.
func gracefulShutdown(drain time.Duration, sigs <-chan os.Signal, adm *server.Admission, mgr *session.Manager, stopServices func()) {
	start := time.Now()
	skip, skipNow := context.WithCancel(context.Background())
	defer skipNow()
	watching := make(chan struct{})
	go func() {
		defer close(watching)
		select {
		case sig := <-sigs:
			log.Printf("received %s again: ending the shutdown drain", sig)
			skipNow()
		case <-skip.Done():
		}
	}()

	fenceCtx, cancelFence := context.WithDeadline(skip, start.Add(drain))
	if !adm.Fence(fenceCtx) {
		log.Print("shutdown: proceeding with RPCs still in flight")
	}
	cancelFence()
	stopServices()
	mgr.BeginShutdown()
	if drain > 0 {
		drainCtx, cancelDrain := context.WithDeadline(skip, start.Add(drain))
		if busy := mgr.Drain(drainCtx); len(busy) > 0 {
			log.Printf("shutdown: drain ended with %d session(s) still active: %s", len(busy), strings.Join(busy, ", "))
		}
		cancelDrain()
	}
	skipNow()
	<-watching
	go func() {
		sig := <-sigs
		log.Printf("received %s during shutdown: exiting now", sig)
		os.Exit(1)
	}()
	mgr.ShutdownForRestart()
}

// StateLockFile is the persistent daemon's exclusive state-directory lock.
func StateLockFile() string {
	return filepath.Join(filepath.Dir(project.StateFile()), "daemon.lock")
}

// startupProjectSkipReason says why a persistent daemon should not register its
// startup directory as a project, or "" to register it. A directory named with
// --workspace is always registered; an implicit one (the shell's current
// directory) only when it is a git repository other than the home directory.
func startupProjectSkipReason(dir string, explicit bool) string {
	if explicit {
		return ""
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "cannot resolve it"
	}
	if home, err := os.UserHomeDir(); err == nil && sameDir(home, abs) {
		return "it is your home directory"
	}
	out, err := exec.Command("git", "-C", abs, "rev-parse", "--is-inside-work-tree").Output()
	if err != nil || strings.TrimSpace(string(out)) != "true" {
		return "it is not a git repository"
	}
	return ""
}

// sameDir reports whether a and b name the same directory, through symlinks.
func sameDir(a, b string) bool {
	ai, err := os.Stat(a)
	if err != nil {
		return false
	}
	bi, err := os.Stat(b)
	return err == nil && os.SameFile(ai, bi)
}

// startupGuidance tells the person who just started the daemon what to do
// next: where the web app is and how to sign in, and what setup is missing.
func startupGuidance(o Options, mgr *session.Manager, usingTLS bool) []string {
	var lines []string
	if o.Web {
		scheme := "http"
		if usingTLS {
			scheme = "https"
		}
		host, port, err := net.SplitHostPort(o.Addr)
		if err == nil && (host == "" || host == "0.0.0.0" || host == "::") {
			host = "<this machine's address>"
		}
		url := scheme + "://" + o.Addr + "/"
		if err == nil {
			url = scheme + "://" + net.JoinHostPort(host, port) + "/"
		}
		lines = append(lines, "web app: "+url)
		if o.TokenFile != "" {
			lines = append(lines, "sign in with the access token in "+o.TokenFile)
		} else {
			lines = append(lines, "sign in with the access token the daemon was started with (YCC_TOKEN)")
		}
	}
	coord, _, _ := mgr.Roles()
	if m, ok := mgr.GetModel(coord); ok {
		if missing := config.MissingCredential(m); missing != "" {
			if o.ConfigPath == "" {
				lines = append(lines, "no model is set up yet: connect a Claude or ChatGPT subscription from the app's Get started screen, "+
					"or run `ycc login anthropic` (or `ycc login openai`) and restart the daemon")
			} else {
				lines = append(lines, fmt.Sprintf("coordinator model %q is not set up (%s); see Settings → Health in the app", coord, missing))
			}
		}
	}
	if len(mgr.Projects()) == 0 {
		lines = append(lines, "no projects yet: add one from the web or iOS app, or with `ycc project add <dir>`")
	}
	return lines
}

// InProcess is a running one-shot daemon: a server bound to an ephemeral
// loopback address, tied to the caller's lifetime. Call Shutdown (or Close) to
// tear it down — the listener and any in-flight work end with it.
type InProcess struct {
	Addr    string // base URL, e.g. "http://127.0.0.1:54321"
	Token   string // bearer token required by this instance
	httpSrv *http.Server
	mgr     *session.Manager
}

// StartInProcess starts the daemon in-process on an ephemeral loopback address
// (127.0.0.1:0) and returns once it is listening. There is no persistence:
// closing it ends in-flight agent work. The returned InProcess must be shut
// down by the caller (defer Shutdown / Close) so no listener survives exit.
func StartInProcess(o Options) (*InProcess, error) {
	o.Addr = "127.0.0.1:0"
	// The one-shot in-process daemon is a private loopback backing one CLI
	// command, not a remote-access surface, so it never serves the web client even
	// if the caller sets Web (matching how Addr is forced above).
	o.Web = false
	if o.Token == "" {
		var err error
		o.Token, err = randomToken()
		if err != nil {
			return nil, err
		}
	}
	parts, err := buildHandler(o, nil)
	if err != nil {
		return nil, err
	}
	handler, mgr := parts.handler, parts.mgr
	ln, err := net.Listen("tcp", o.Addr)
	if err != nil {
		return nil, fmt.Errorf("listen ephemeral: %w", err)
	}
	httpSrv := newHTTPServer(o.Addr, handler)
	ip := &InProcess{
		Addr:    "http://" + ln.Addr().String(),
		Token:   o.Token,
		httpSrv: httpSrv,
		mgr:     mgr,
	}
	go func() {
		if err := httpSrv.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Printf("in-process daemon: %v", err)
		}
	}()
	return ip, nil
}

// Shutdown gracefully stops the in-process daemon, ending in-flight work.
func (p *InProcess) Shutdown() error {
	if p == nil || p.httpSrv == nil {
		return nil
	}
	// Session runs are detached from their originating RPC context. Reclaim them
	// explicitly so their run contexts cancel before HTTP shutdown waits for
	// streaming handlers. Repeating after Shutdown closes the narrow race with an
	// already-running StartSession RPC; ReclaimAll is idempotent.
	if p.mgr != nil {
		p.mgr.ReclaimAll()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := p.httpSrv.Shutdown(ctx)
	if p.mgr != nil {
		p.mgr.ReclaimAll()
	}
	return err
}

// Close forcibly stops the in-process daemon. Safe to call multiple times.
func (p *InProcess) Close() error {
	if p == nil || p.httpSrv == nil {
		return nil
	}
	if p.mgr != nil {
		p.mgr.ReclaimAll()
	}
	err := p.httpSrv.Close()
	if p.mgr != nil {
		p.mgr.ReclaimAll()
	}
	return err
}

// persistPath returns the config file path a runtime config mutation should be
// written back to when persist=true. It prefers the
// explicitly loaded config path; otherwise it falls back to the default
// discovered location ($XDG_CONFIG_HOME/ycc/ycc.toml) so that even the
// no-config DefaultAnthropic startup can persist edits. An empty return value
// (no user config dir) disables persistence.
func persistPath(o Options) string {
	if o.ConfigPath != "" {
		return o.ConfigPath
	}
	if p, err := UserConfigPath(); err == nil {
		return p
	}
	return ""
}

// IsLoopback reports whether addr names an explicit loopback listen address.
func IsLoopback(addr string) bool {
	host := addr
	if i := strings.LastIndex(addr, ":"); i >= 0 {
		host = addr[:i]
	}
	// An empty host (e.g. ":8787", or an empty Addr — which http.Server treats as
	// ":http") is a WILDCARD bind: net.Listen binds every interface, so it is
	// network-exposed and must require a token. Only explicit loopback forms are
	// treated as loopback.
	if host == "" {
		return false
	}
	if host == "localhost" {
		return true
	}
	// Strip brackets from an IPv6 literal like "[::1]" before parsing.
	host = strings.TrimPrefix(strings.TrimSuffix(host, "]"), "[")
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}
