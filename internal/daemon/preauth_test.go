package daemon

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type unreadBody struct{ reads int }

func (b *unreadBody) Read([]byte) (int, error) {
	b.reads++
	return 0, io.EOF
}

func (b *unreadBody) Close() error { return nil }

func TestPreAuthDoesNotReadBody(t *testing.T) {
	o := baseOptions(t)
	o.Token = "secret"
	h, mgr, err := buildHandler(o)
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.ReclaimAll()

	for _, name := range []string{"gzip", "oversized", "upgrade", "wrong-token", "latency", "unknown-content-type"} {
		t.Run(name, func(t *testing.T) {
			path := "/ycc.v1.SessionService/ListSessions"
			method := http.MethodPost
			if name == "latency" {
				path = "/debug/latency"
				method = http.MethodGet
			}
			r := httptest.NewRequest(method, path, nil)
			body := new(unreadBody)
			r.Body = body
			r.ContentLength = rpcReadMaxBytes + 1
			r.Header.Set("Content-Type", "application/json")
			switch name {
			case "gzip":
				r.Header.Set("Content-Encoding", "gzip")
			case "upgrade":
				r.Header.Set("Connection", "Upgrade, HTTP2-Settings")
				r.Header.Set("Upgrade", "h2c")
				r.Header.Set("HTTP2-Settings", "AAMAAABk")
			case "wrong-token":
				r.Header.Set("Authorization", "Bearer wrong")
			case "unknown-content-type":
				r.Header.Set("Content-Type", "application/octet-stream")
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if body.reads != 0 {
				t.Fatalf("unauthenticated body read %d times", body.reads)
			}
			assertConnectError(t, w, http.StatusUnauthorized, "unauthenticated")
		})
	}
}

func TestAuthenticatedReadLimit(t *testing.T) {
	o := baseOptions(t)
	o.Token = "secret"
	h, mgr, err := buildHandler(o)
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.ReclaimAll()

	// Compressible whitespace exceeds the limit before JSON decoding. Test both
	// wire size and decompressed size, without changing the production limit.
	payload := strings.Repeat(" ", rpcReadMaxBytes+1)
	for _, encoding := range []string{"identity", "gzip"} {
		t.Run(encoding, func(t *testing.T) {
			var body io.Reader = strings.NewReader(payload)
			if encoding == "gzip" {
				var compressed bytes.Buffer
				gz := gzip.NewWriter(&compressed)
				if _, err := io.Copy(gz, body); err != nil {
					t.Fatal(err)
				}
				if err := gz.Close(); err != nil {
					t.Fatal(err)
				}
				body = &compressed
			}
			r := httptest.NewRequest(http.MethodPost, "/ycc.v1.SessionService/ListSessions", body)
			r.Header.Set("Authorization", "Bearer secret")
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Content-Encoding", encoding)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			assertConnectError(t, w, http.StatusTooManyRequests, "resource_exhausted")
		})
	}
}

func assertConnectError(t *testing.T, w *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	var body struct {
		Code string `json:"code"`
	}
	if w.Code != status || w.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("status=%d content-type=%q body=%s", w.Code, w.Header().Get("Content-Type"), w.Body)
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body.Code != code {
		t.Fatalf("body=%s, want code %q (decode error: %v)", w.Body, code, err)
	}
}

func TestH2CUpgradeRejectsBeforeReceivingBody(t *testing.T) {
	o := baseOptions(t)
	o.Token = "secret"
	h, mgr, err := buildHandler(o)
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.ReclaimAll()
	srv := httptest.NewUnstartedServer(h)
	srv.Config = newHTTPServer("", h)
	srv.Start()
	defer srv.Close()

	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	// Do not send the declared body. The old h2c Upgrade handler waited to read
	// all of it before routing; native HTTP/2 treats Upgrade as ordinary HTTP/1.
	_, err = io.WriteString(conn, "POST /ycc.v1.SessionService/ListSessions HTTP/1.1\r\n"+
		"Host: localhost\r\nContent-Type: application/json\r\nContent-Length: 33554433\r\n"+
		"Connection: Upgrade, HTTP2-Settings\r\nUpgrade: h2c\r\nHTTP2-Settings: AAMAAABk\r\n\r\n")
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatalf("response before sending body: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized || resp.ProtoMajor != 1 {
		t.Fatalf("Upgrade response: %s %s", resp.Proto, resp.Status)
	}
}

func TestNativeTLSHTTP2(t *testing.T) {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor != 2 {
			t.Errorf("request protocol = %s, want HTTP/2", r.Proto)
		}
		w.WriteHeader(http.StatusNoContent)
	})
	srv := httptest.NewUnstartedServer(h)
	srv.Config = newHTTPServer("", h)
	srv.EnableHTTP2 = true
	srv.StartTLS()
	defer srv.Close()

	client := srv.Client()
	client.Transport.(*http.Transport).ForceAttemptHTTP2 = true
	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.ProtoMajor != 2 || resp.StatusCode != http.StatusNoContent {
		t.Fatalf("TLS response: %s %s", resp.Proto, resp.Status)
	}
}
