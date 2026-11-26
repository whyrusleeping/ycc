package jobshim

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Launcher starts supervised jobs. It is shared by every session of a daemon.
type Launcher struct {
	// Executable is the ycc binary re-executed as the supervisor.
	Executable string
	// RuntimeDir is the daemon-owned, owner-only directory holding control
	// sockets. It is outside every workspace and short enough for sun_path.
	RuntimeDir string
	// RetainedBytes bounds each session's finished, notified job evidence
	// (zero means DefaultSessionRetainedBytes).
	RetainedBytes int64
}

// NewLauncher returns a launcher that re-executes this binary. An empty
// runtimeDir selects DefaultRuntimeDir.
func NewLauncher(runtimeDir string) (*Launcher, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("locate ycc executable: %w", err)
	}
	if runtimeDir == "" {
		runtimeDir = DefaultRuntimeDir()
	}
	if err := ensurePrivateDir(runtimeDir); err != nil {
		return nil, err
	}
	return &Launcher{Executable: exe, RuntimeDir: runtimeDir}, nil
}

// DefaultRuntimeDir is $XDG_RUNTIME_DIR/ycc when that is set and short, else
// /tmp/ycc-<uid>.
func DefaultRuntimeDir() string {
	if x := os.Getenv("XDG_RUNTIME_DIR"); x != "" && filepath.IsAbs(x) && len(x) < 48 {
		return filepath.Join(x, "ycc")
	}
	return filepath.Join("/tmp", fmt.Sprintf("ycc-%d", os.Getuid()))
}

// ensurePrivateDir creates dir 0700 or verifies an existing one is a real
// directory owned by this user, tightening its mode. A directory another user
// planted (e.g. in /tmp) is refused.
func ensurePrivateDir(dir string) error {
	if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return fmt.Errorf("create runtime dir: %w", err)
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return fmt.Errorf("runtime dir %s is not a directory", dir)
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); !ok || int(st.Uid) != os.Getuid() {
		return fmt.Errorf("runtime dir %s is not owned by this user", dir)
	}
	if fi.Mode().Perm() != 0o700 {
		if err := os.Chmod(dir, 0o700); err != nil {
			return err
		}
	}
	return nil
}

// Session is a launcher bound to one session directory.
type Session struct {
	l           *Launcher
	dir         string
	retentionMu sync.Mutex
	retained    int64 // per-session override of the retention bound
}

// ForSession binds the launcher to sessionDir (<workspace>/.ycc/sessions/<id>).
func (l *Launcher) ForSession(sessionDir string) *Session {
	if abs, err := filepath.Abs(sessionDir); err == nil {
		sessionDir = abs
	}
	return &Session{l: l, dir: sessionDir}
}

// Dir returns the session directory.
func (s *Session) Dir() string { return s.dir }

// JobDir returns the directory of jobID's evidence.
func (s *Session) JobDir(jobID string) string { return filepath.Join(JobsDir(s.dir), jobID) }

// SocketPath derives jobID's control socket path. It is computed by the daemon,
// never read back from workspace files.
func (s *Session) SocketPath(jobID string) string {
	sum := sha256.Sum256([]byte(s.dir + "\x00" + jobID))
	return filepath.Join(s.l.RuntimeDir, "j-"+hex.EncodeToString(sum[:8])+".sock")
}

// SetRetainedBytes overrides this session's retention bound (tests and
// operators); zero restores the launcher's bound.
func (s *Session) SetRetainedBytes(n int64) {
	s.retentionMu.Lock()
	s.retained = n
	s.retentionMu.Unlock()
}

// RetainedBytes is the session's retention bound.
func (s *Session) RetainedBytes() int64 {
	s.retentionMu.Lock()
	defer s.retentionMu.Unlock()
	return s.retainedLocked()
}

func (s *Session) retainedLocked() int64 {
	if s.retained > 0 {
		return s.retained
	}
	if s.l.RetainedBytes > 0 {
		return s.l.RetainedBytes
	}
	return DefaultSessionRetainedBytes
}

// Request describes one job to launch.
type Request struct {
	JobID   string
	Command string
	Dir     string
	Env     []string // scrubbed environment; passed through exec, never written
	Timeout time.Duration
}

var jobIDPattern = regexp.MustCompile(`^job_[0-9]+$`)

// Pending is a supervisor that is ready (holds shim.lock and serves its control
// socket) but has not started the child. Exactly one of Start and Abort must
// be called.
type Pending struct {
	s          *Session
	jobID      string
	dir        string
	socket     string
	cmd        *exec.Cmd
	gate       *os.File
	ctl        *controlConn
	procExited chan struct{}
}

// Prepare creates the job directory and spec, spawns the supervisor in its own
// session, and waits for its readiness handshake.
func (s *Session) Prepare(req Request) (*Pending, error) {
	if !jobIDPattern.MatchString(req.JobID) {
		return nil, fmt.Errorf("invalid job id %q", req.JobID)
	}
	socket := s.SocketPath(req.JobID)
	if len(socket) > maxSocketPath {
		return nil, fmt.Errorf("control socket path %s exceeds %d bytes", socket, maxSocketPath)
	}
	if err := ensurePrivateDir(s.l.RuntimeDir); err != nil {
		return nil, err
	}
	if err := s.checkRuntimeOutside(req.Dir); err != nil {
		return nil, err
	}
	dir, err := s.createJobDir(req.JobID)
	if err != nil {
		return nil, err
	}
	spec := Spec{Version: ProtocolVersion, JobID: req.JobID, Command: req.Command, Dir: req.Dir,
		TimeoutMS: req.Timeout.Milliseconds(), Socket: socket}
	data, err := json.Marshal(spec)
	if err == nil {
		err = writeFileAtomic(filepath.Join(dir, specFile), data)
	}
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("write job spec: %w", err)
	}
	p, err := s.spawn(req, dir, socket)
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	return p, nil
}

// workspace is the workspace that owns the session directory
// (<workspace>/.ycc/sessions/<id>).
func (s *Session) workspace() string { return filepath.Dir(filepath.Dir(filepath.Dir(s.dir))) }

// checkRuntimeOutside refuses a runtime directory that resolves inside the
// session's workspace or the job's working directory: control sockets must
// never live where workspace content (or an agent) can replace them.
func (s *Session) checkRuntimeOutside(jobDir string) error {
	rt, err := filepath.EvalSymlinks(s.l.RuntimeDir)
	if err != nil {
		return fmt.Errorf("resolve runtime dir: %w", err)
	}
	for _, root := range []string{s.workspace(), jobDir} {
		if root == "" {
			continue
		}
		resolved, err := filepath.EvalSymlinks(root)
		if err != nil {
			continue
		}
		if rel, err := filepath.Rel(resolved, rt); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return fmt.Errorf("runtime dir %s is inside the workspace %s", s.l.RuntimeDir, root)
		}
	}
	return nil
}

// createJobDir creates <session>/jobs/<id> owner-only. It refuses symlinks at
// every path component below the workspace (.ycc, sessions, the session
// directory, jobs, and the job directory itself), so workspace content cannot
// redirect where evidence is written.
func (s *Session) createJobDir(jobID string) (string, error) {
	for _, dir := range []string{filepath.Dir(filepath.Dir(s.dir)), filepath.Dir(s.dir), s.dir} {
		fi, err := os.Lstat(dir)
		if err != nil {
			return "", fmt.Errorf("session dir: %w", err)
		}
		if !fi.IsDir() {
			return "", fmt.Errorf("%s is not a directory (symlinks are refused)", dir)
		}
	}
	jobs := JobsDir(s.dir)
	if err := os.Mkdir(jobs, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return "", fmt.Errorf("create jobs dir: %w", err)
	}
	if fi, err := os.Lstat(jobs); err != nil || !fi.IsDir() {
		return "", fmt.Errorf("jobs dir %s is not a directory", jobs)
	}
	dir := filepath.Join(jobs, jobID)
	if err := os.Mkdir(dir, 0o700); err != nil {
		return "", fmt.Errorf("create job dir: %w", err)
	}
	return dir, nil
}

func (s *Session) spawn(req Request, dir, socket string) (*Pending, error) {
	readyR, readyW, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	defer readyR.Close()
	gateR, gateW, err := os.Pipe()
	if err != nil {
		readyW.Close()
		return nil, err
	}
	logf, err := os.OpenFile(filepath.Join(dir, supervisorLog), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		readyW.Close()
		gateR.Close()
		gateW.Close()
		return nil, err
	}
	cmd := exec.Command(s.l.Executable, HelperArg, dir)
	cmd.Dir = dir
	cmd.Env = req.Env
	cmd.Stderr = logf
	cmd.ExtraFiles = []*os.File{readyW, gateR}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	startErr := cmd.Start()
	readyW.Close()
	gateR.Close()
	logf.Close()
	if startErr != nil {
		gateW.Close()
		return nil, fmt.Errorf("start job supervisor: %w", startErr)
	}
	p := &Pending{s: s, jobID: req.JobID, dir: dir, socket: socket, cmd: cmd, gate: gateW,
		procExited: make(chan struct{})}
	// Reap the supervisor while this daemon is attached, so it never lingers
	// as a zombie.
	go func() {
		_ = cmd.Wait()
		close(p.procExited)
	}()
	if err := p.handshake(readyR); err != nil {
		p.discard()
		return nil, err
	}
	return p, nil
}

func (p *Pending) handshake(readyR *os.File) error {
	_ = readyR.SetReadDeadline(time.Now().Add(readyTimeout))
	line, err := bufio.NewReader(readyR).ReadBytes('\n')
	if err != nil {
		return fmt.Errorf("job supervisor did not become ready: %v%s", err, p.supervisorStderr())
	}
	var hello controlMsg
	if err := json.Unmarshal(line, &hello); err != nil || hello.Type != "hello" || hello.Version != ProtocolVersion {
		return fmt.Errorf("job supervisor protocol mismatch: got %q, want hello version %d", strings.TrimSpace(string(line)), ProtocolVersion)
	}
	ctl, err := dialControl(p.socket)
	if err != nil {
		return fmt.Errorf("connect job supervisor control socket: %w", err)
	}
	p.ctl = ctl
	return nil
}

func (p *Pending) supervisorStderr() string {
	select {
	case <-p.procExited:
	case <-time.After(time.Second):
	}
	data, _ := os.ReadFile(filepath.Join(p.dir, supervisorLog))
	if msg := strings.TrimSpace(string(data)); msg != "" {
		return " (" + msg + ")"
	}
	return ""
}

// discard closes the start gate and waits for a supervisor that never started
// a child. Killing it is safe only here, before "go": no child exists.
func (p *Pending) discard() {
	_ = p.gate.Close()
	if p.ctl != nil {
		p.ctl.close()
	}
	select {
	case <-p.procExited:
	case <-time.After(readyTimeout):
		_ = p.cmd.Process.Kill()
		<-p.procExited
	}
}

// Abort closes the start gate without starting the child (job_started could not
// be made durable), waits for the supervisor, and removes the job directory,
// which no durable event references.
func (p *Pending) Abort() {
	p.discard()
	_ = os.RemoveAll(p.dir)
}

// Start tells the supervisor to start the child. Call it only after the
// job_started event is durable.
func (p *Pending) Start() (*Handle, error) {
	_, err := p.gate.Write([]byte("go\n"))
	_ = p.gate.Close()
	h := &Handle{s: p.s, jobID: p.jobID, dir: p.dir, socket: p.socket,
		procExited: p.procExited, ctl: p.ctl, done: make(chan struct{})}
	if err != nil {
		// The gate's read end is gone: the supervisor never received "go", so
		// the child never started.
		p.ctl.close()
		<-p.procExited
		rec, readErr := ReadExit(p.dir)
		if readErr != nil {
			rec = &ExitRecord{Version: ProtocolVersion, JobID: p.jobID, State: StateNotStarted, ExitCode: -1,
				Error: "supervisor exited before the start signal", Finished: time.Now().UTC()}
			if data, err := json.Marshal(rec); err == nil {
				_ = writeFileAtomic(filepath.Join(p.dir, exitFile), data)
			}
		}
		h.outcome = Outcome{Exit: rec}
		close(h.done)
		return h, fmt.Errorf("start job supervisor child: %w", err)
	}
	go h.watch()
	return h, nil
}

// Outcome is what the daemon learned about a supervised job's execution.
type Outcome struct {
	// Exit is the supervisor's exit record; nil when execution is unknown.
	Exit *ExitRecord
	// Unknown says why supervision was lost without an exit record. The child
	// may still be running.
	Unknown string
	// Detached means the daemon stopped following the job (Detach) before its
	// outcome was known; the supervisor and its child keep running.
	Detached bool
}

// Handle is the daemon's attachment to a started supervised job.
type Handle struct {
	s          *Session
	jobID      string
	dir        string
	socket     string
	procExited <-chan struct{}
	stopWatch  chan struct{} // closed by Detach to end an attached handle's lock watch

	mu       sync.Mutex
	ctl      *controlConn
	detached bool
	done     chan struct{}
	outcome  Outcome
}

// Dir returns the job directory.
func (h *Handle) Dir() string { return h.dir }

// Reader reads the job's retained output.
func (h *Handle) Reader() *Reader { return NewReader(h.dir) }

// Done is closed once the outcome is known.
func (h *Handle) Done() <-chan struct{} { return h.done }

// Exited is closed once the supervisor process has exited and been reaped,
// which is when its shim.lock is released.
func (h *Handle) Exited() <-chan struct{} { return h.procExited }

// Outcome returns the job's outcome once Done is closed.
func (h *Handle) Outcome() Outcome {
	<-h.done
	return h.outcome
}

const (
	exitGrace           = 2 * time.Second
	socketCheckInterval = time.Second
)

func socketPresent(path string) bool {
	fi, err := os.Lstat(path)
	return err == nil && fi.Mode()&os.ModeSocket != 0
}

// watch follows the attached control connection until the supervisor's exit
// record arrives or supervision is lost. Its control socket vanishing while
// the connection stays open is loss too: nothing else could reach the
// supervisor, so the outcome is unknown rather than a job left running.
func (h *Handle) watch() {
	defer close(h.done)
	for {
		h.mu.Lock()
		ctl := h.ctl
		h.mu.Unlock()
		exitCh := make(chan *ExitRecord, 1)
		go func() {
			rec, _ := ctl.waitExit()
			exitCh <- rec
		}()
		var rec *ExitRecord
		received, vanished := false, false
		ticker := time.NewTicker(socketCheckInterval)
		for !received && !vanished {
			select {
			case rec = <-exitCh:
				received = true
			case <-ticker.C:
				vanished = !socketPresent(h.socket)
			}
		}
		ticker.Stop()
		if vanished {
			// A supervisor exiting normally unlinks its socket just after
			// sending its record; give that a moment before calling it loss.
			select {
			case rec = <-exitCh:
			case <-h.procExited:
			case <-time.After(exitGrace):
			}
		}
		ctl.close()
		if rec != nil {
			h.outcome = Outcome{Exit: rec}
			return
		}
		if h.isDetached() {
			h.outcome = Outcome{Detached: true}
			return
		}
		if !vanished {
			// The connection ended without an exit record. A supervisor that
			// is exiting normally has already written exit.json.
			select {
			case <-h.procExited:
			case <-time.After(exitGrace):
			}
		}
		if rec, err := ReadExit(h.dir); err == nil {
			h.outcome = Outcome{Exit: rec}
			return
		}
		select {
		case <-h.procExited:
			h.outcome = Outcome{Unknown: "the job supervisor exited without an exit record"}
			return
		default:
		}
		var next *controlConn
		var err error
		if !vanished {
			next, err = dialControl(h.socket)
		}
		if vanished || err != nil {
			h.outcome = Outcome{Unknown: "the job supervisor's control socket vanished while it still holds " + lockFile}
			return
		}
		h.mu.Lock()
		if h.detached {
			h.mu.Unlock()
			next.close()
			h.outcome = Outcome{Detached: true}
			return
		}
		h.ctl = next
		h.mu.Unlock()
	}
}

// isDetached reports whether Detach was called.
func (h *Handle) isDetached() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.detached
}

// Detach stops following the job without deciding its outcome, leaving the
// supervisor and its child running for a later daemon to attach (graceful
// restart). It reports false when the outcome was already known (the job
// finished first), in which case Outcome holds it.
func (h *Handle) Detach() bool {
	h.mu.Lock()
	select {
	case <-h.done:
		h.mu.Unlock()
		return h.outcome.Detached
	default:
	}
	h.detached = true
	ctl := h.ctl
	h.mu.Unlock()
	if ctl != nil {
		ctl.close()
	}
	<-h.done
	if h.outcome.Detached && h.stopWatch != nil {
		close(h.stopWatch)
	}
	return h.outcome.Detached
}

// Kill asks the supervisor to kill its child. The daemon never signals the
// child or the supervisor itself.
func (h *Handle) Kill() error {
	h.mu.Lock()
	ctl := h.ctl
	h.mu.Unlock()
	select {
	case <-h.done:
		if h.outcome.Exit != nil {
			return nil
		}
		return errors.New(h.outcome.Unknown)
	default:
	}
	if ctl != nil && ctl.send(controlMsg{Type: "kill"}) == nil {
		return nil
	}
	fresh, err := dialControl(h.socket)
	if err != nil {
		return fmt.Errorf("job supervisor unreachable: %w", err)
	}
	defer fresh.close()
	return fresh.send(controlMsg{Type: "kill"})
}

// Confirm waits for the supervisor process to exit after supervision was lost
// and returns the exit record it left, or nil when there is none (execution
// stays unknown).
func (h *Handle) Confirm() *ExitRecord {
	<-h.procExited
	rec, err := ReadExit(h.dir)
	if err != nil {
		return nil
	}
	return rec
}

// MarkHarvested records that the daemon has consumed the exit record, so GC
// may later remove the session directory.
func (h *Handle) MarkHarvested() error {
	_ = os.Remove(filepath.Join(h.dir, unknownFile))
	return writeMarker(h.dir, harvestedFile, time.Now().UTC().Format(time.RFC3339Nano)+"\n")
}

// MarkUnknown records the fail-closed execution-unknown state, which protects
// the job directory (and its session) from GC until cleared.
func (h *Handle) MarkUnknown(reason string) error {
	return writeMarker(h.dir, unknownFile, reason+"\n")
}
