package notify

import (
	"bytes"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/whyrusleeping/ycc/internal/config"
)

type failingTransport struct{}

func (failingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return nil, errors.New("connection failed")
}

func TestWebhookFailureLogsOriginOnly(t *testing.T) {
	var logs bytes.Buffer
	old := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(old) })
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusForbidden) }))
	defer server.Close()
	origin := server.URL
	for _, transportFailure := range []bool{false, true} {
		logs.Reset()
		n := New(config.Notify{URL: strings.Replace(origin, "://", "://secret-user:secret-password@", 1) + "/secret-path?token=secret-query"})
		if transportFailure {
			n.client.Transport = failingTransport{}
		}
		n.Send(KindError, "project", "session", "error")
		n.Flush()
		text := logs.String()
		if !strings.Contains(text, origin) {
			t.Fatalf("missing origin: %s", text)
		}
		for _, secret := range []string{"secret-user", "secret-password", "secret-path", "secret-query"} {
			if strings.Contains(text, secret) {
				t.Fatalf("secret in log: %s", text)
			}
		}
	}
}
