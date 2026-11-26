package jobshim

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"time"
)

// Control protocol: newline-delimited JSON over a Unix stream socket. On
// connect the supervisor sends a hello; a client may send kill requests; every
// connected client receives the exit record when the child is done.
type controlMsg struct {
	Type    string      `json:"type"` // "hello" or "exit" from the supervisor; "kill" from the daemon
	Version int         `json:"version,omitempty"`
	State   string      `json:"state,omitempty"` // hello: "waiting" (before go) or "running"
	Exit    *ExitRecord `json:"exit,omitempty"`
}

// checkPeer refuses a Unix-socket peer running as another user.
func checkPeer(conn net.Conn) error {
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		return fmt.Errorf("control connection is not a unix socket")
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return err
	}
	var uid int
	var credErr error
	if err := raw.Control(func(fd uintptr) { uid, credErr = peerUID(int(fd)) }); err != nil {
		return err
	}
	if credErr != nil {
		return fmt.Errorf("read control peer credentials: %w", credErr)
	}
	if uid != os.Getuid() {
		return fmt.Errorf("control peer uid %d is not this user (%d)", uid, os.Getuid())
	}
	return nil
}

// controlConn is the daemon's end of a supervisor control connection.
type controlConn struct {
	conn  net.Conn
	r     *bufio.Reader
	hello controlMsg
}

// dialControl connects to a supervisor's control socket, verifies the peer's
// uid and the protocol version, and reads its hello.
func dialControl(path string) (*controlConn, error) {
	conn, err := net.DialTimeout("unix", path, controlTimeout)
	if err != nil {
		return nil, err
	}
	if err := checkPeer(conn); err != nil {
		conn.Close()
		return nil, err
	}
	c := &controlConn{conn: conn, r: bufio.NewReader(conn)}
	_ = conn.SetReadDeadline(time.Now().Add(controlTimeout))
	msg, err := c.read()
	_ = conn.SetReadDeadline(time.Time{})
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("read supervisor hello: %w", err)
	}
	if msg.Type != "hello" || msg.Version != ProtocolVersion {
		conn.Close()
		return nil, fmt.Errorf("supervisor speaks protocol %q version %d, want hello version %d", msg.Type, msg.Version, ProtocolVersion)
	}
	c.hello = msg
	return c, nil
}

func (c *controlConn) read() (controlMsg, error) {
	line, err := c.r.ReadBytes('\n')
	if err != nil {
		return controlMsg{}, err
	}
	var msg controlMsg
	if err := json.Unmarshal(line, &msg); err != nil {
		return controlMsg{}, err
	}
	return msg, nil
}

func (c *controlConn) send(msg controlMsg) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	_ = c.conn.SetWriteDeadline(time.Now().Add(controlTimeout))
	_, err = c.conn.Write(append(data, '\n'))
	_ = c.conn.SetWriteDeadline(time.Time{})
	return err
}

// waitExit reads until the supervisor sends its exit record or the connection
// ends.
func (c *controlConn) waitExit() (*ExitRecord, error) {
	for {
		msg, err := c.read()
		if err != nil {
			return nil, err
		}
		if msg.Type == "exit" && msg.Exit != nil {
			return msg.Exit, nil
		}
	}
}

func (c *controlConn) close() { _ = c.conn.Close() }
