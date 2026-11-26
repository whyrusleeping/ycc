package jobshim

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// supervisorMain is the helper process. FD 3 is the readiness pipe and FD 4 the
// start gate. It never starts the child unless the daemon writes "go" on the
// gate, which the daemon does only after job_started is durable.
func supervisorMain(jobDir string) int {
	ready := os.NewFile(3, "ready")
	gate := os.NewFile(4, "gate")
	fail := func(format string, args ...any) int {
		fmt.Fprintf(os.Stderr, "ycc job supervisor: "+format+"\n", args...)
		if ready != nil {
			_ = ready.Close()
		}
		if gate != nil {
			_ = gate.Close()
		}
		return 2
	}
	if ready == nil || gate == nil {
		return fail("missing readiness or start-gate pipe")
	}
	var spec Spec
	if err := readJSON(filepath.Join(jobDir, specFile), &spec); err != nil {
		return fail("read spec: %v", err)
	}
	if spec.Version != ProtocolVersion {
		return fail("spec version %d, want %d", spec.Version, ProtocolVersion)
	}
	// The lock is held for the supervisor's whole life (released by the kernel
	// when it exits, however it exits). Go opens it close-on-exec, so the child
	// never inherits it.
	lock, err := os.OpenFile(filepath.Join(jobDir, lockFile), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return fail("open lock: %v", err)
	}
	// Keep the file referenced until return: a collected *os.File would close
	// the descriptor and silently drop the lock.
	defer lock.Close()
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return fail("acquire %s: %v", lockFile, err)
	}
	out, err := newOutputWriter(jobDir)
	if err != nil {
		return fail("create output files: %v", err)
	}
	srv, err := listenControl(spec.Socket)
	if err != nil {
		return fail("bind control socket: %v", err)
	}
	go srv.serve()
	// A termination signal to the supervisor is treated as a kill request, so
	// it still writes an exit record instead of leaving execution unknown.
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	go func() {
		<-signals
		srv.requestKill()
	}()
	hello, _ := json.Marshal(controlMsg{Type: "hello", Version: ProtocolVersion})
	if _, err := ready.Write(append(hello, '\n')); err != nil {
		srv.close()
		return fail("signal readiness: %v", err)
	}
	_ = ready.Close()

	var word [3]byte
	_, gateErr := io.ReadFull(gate, word[:])
	_ = gate.Close()
	if gateErr != nil || string(word[:]) != "go\n" || srv.killRequested() {
		// The daemon could not record job_started (or went away): never run.
		rec := &ExitRecord{Version: ProtocolVersion, JobID: spec.JobID, State: StateNotStarted,
			ExitCode: -1, Finished: time.Now().UTC(), Output: out.finish()}
		finishSupervisor(jobDir, srv, rec)
		return 0
	}
	rec := runChild(spec, out, srv)
	finishSupervisor(jobDir, srv, rec)
	return 0
}

var errKillRequested = errors.New("kill requested")

func runChild(spec Spec, out *outputWriter, srv *controlServer) *ExitRecord {
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	runCtx := ctx
	if spec.TimeoutMS > 0 {
		var cancelTimeout context.CancelFunc
		runCtx, cancelTimeout = context.WithTimeout(ctx, time.Duration(spec.TimeoutMS)*time.Millisecond)
		defer cancelTimeout()
	}
	cmd := exec.CommandContext(runCtx, "sh", "-c", spec.Command)
	cmd.Dir = spec.Dir
	cmd.Env = os.Environ()
	cmd.Stdout = out
	cmd.Stderr = out
	// The child gets its own process group, so the supervisor's kill covers
	// the shell's pipeline. Only the supervisor signals it, and only while
	// Wait has not returned (exec invokes Cancel only then).
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = WaitDelay
	rec := &ExitRecord{Version: ProtocolVersion, JobID: spec.JobID}
	rec.Started = time.Now().UTC()
	if err := cmd.Start(); err != nil {
		rec.State, rec.ExitCode, rec.Error = StateStartError, -1, err.Error()
		rec.Finished = time.Now().UTC()
		rec.Output = out.finish()
		return rec
	}
	srv.setRunning()
	go func() {
		select {
		case <-srv.killCh:
			cancel(errKillRequested)
		case <-ctx.Done():
		}
	}()
	err := cmd.Wait()
	rec.Finished = time.Now().UTC()
	rec.Output = out.finish()
	rec.ExitCode = cmd.ProcessState.ExitCode()
	switch {
	case errors.Is(context.Cause(ctx), errKillRequested):
		rec.State = StateKilled
	case spec.TimeoutMS > 0 && errors.Is(runCtx.Err(), context.DeadlineExceeded):
		rec.State = StateTimedOut
	default:
		rec.State = StateExited
	}
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) || exitErr.ExitCode() < 0 {
			rec.Error = err.Error()
		}
	}
	return rec
}

// finishSupervisor publishes the exit record: atomically on disk first, then to
// every connected control client (which is how the daemon still learns the
// outcome when the disk write fails), and finally removes the socket.
func finishSupervisor(jobDir string, srv *controlServer, rec *ExitRecord) {
	data, err := json.Marshal(rec)
	if err == nil {
		err = writeFileAtomic(filepath.Join(jobDir, exitFile), data)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "ycc job supervisor: write exit record: %v\n", err)
	}
	srv.publishExit(rec)
	srv.close()
}

type controlServer struct {
	ln      *net.UnixListener
	path    string
	killCh  chan struct{}
	mu      sync.Mutex
	conns   map[net.Conn]bool
	running bool
	exit    *ExitRecord
	killed  bool
	closed  bool
}

func listenControl(path string) (*controlServer, error) {
	if len(path) > maxSocketPath {
		return nil, fmt.Errorf("control socket path is %d bytes, over the %d-byte limit", len(path), maxSocketPath)
	}
	// Only this supervisor holds the job's lock, so a socket file left at the
	// daemon-derived path is stale.
	if fi, err := os.Lstat(path); err == nil && fi.Mode()&os.ModeSocket != 0 {
		_ = os.Remove(path)
	}
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, err
	}
	_ = os.Chmod(path, 0o600)
	return &controlServer{ln: ln, path: path, killCh: make(chan struct{}), conns: map[net.Conn]bool{}}, nil
}

func (s *controlServer) serve() {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		if err := checkPeer(conn); err != nil {
			fmt.Fprintf(os.Stderr, "ycc job supervisor: refused control connection: %v\n", err)
			conn.Close()
			continue
		}
		go s.handle(conn)
	}
}

func (s *controlServer) handle(conn net.Conn) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		conn.Close()
		return
	}
	state := "waiting"
	if s.running {
		state = "running"
	}
	exit := s.exit
	s.conns[conn] = true
	s.mu.Unlock()
	if writeMsg(conn, controlMsg{Type: "hello", Version: ProtocolVersion, State: state}) != nil {
		s.drop(conn)
		return
	}
	if exit != nil {
		_ = writeMsg(conn, controlMsg{Type: "exit", Version: ProtocolVersion, Exit: exit})
		s.drop(conn)
		return
	}
	r := bufio.NewReader(conn)
	for {
		line, err := r.ReadBytes('\n')
		if err != nil {
			s.drop(conn)
			return
		}
		var msg controlMsg
		if json.Unmarshal(line, &msg) == nil && msg.Type == "kill" {
			s.requestKill()
		}
	}
}

func writeMsg(conn net.Conn, msg controlMsg) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	_ = conn.SetWriteDeadline(time.Now().Add(controlTimeout))
	_, err = conn.Write(append(data, '\n'))
	return err
}

func (s *controlServer) drop(conn net.Conn) {
	s.mu.Lock()
	delete(s.conns, conn)
	s.mu.Unlock()
	conn.Close()
}

func (s *controlServer) requestKill() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.killed {
		s.killed = true
		close(s.killCh)
	}
}

func (s *controlServer) killRequested() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.killed
}

func (s *controlServer) setRunning() {
	s.mu.Lock()
	s.running = true
	s.mu.Unlock()
}

func (s *controlServer) publishExit(rec *ExitRecord) {
	s.mu.Lock()
	s.exit = rec
	conns := make([]net.Conn, 0, len(s.conns))
	for c := range s.conns {
		conns = append(conns, c)
	}
	s.mu.Unlock()
	for _, c := range conns {
		_ = writeMsg(c, controlMsg{Type: "exit", Version: ProtocolVersion, Exit: rec})
	}
}

func (s *controlServer) close() {
	s.mu.Lock()
	s.closed = true
	conns := make([]net.Conn, 0, len(s.conns))
	for c := range s.conns {
		conns = append(conns, c)
	}
	s.conns = map[net.Conn]bool{}
	s.mu.Unlock()
	_ = s.ln.Close() // unlinks the socket path
	for _, c := range conns {
		_ = c.Close()
	}
}
