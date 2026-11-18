package shareserver

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

const testToken = "s3cret-token"

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func newTestServer(t *testing.T, mutate func(*Config)) (*Server, *fakeClock, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "data")
	clock := &fakeClock{t: time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)}
	cfg := Config{
		Dir:       dir,
		PublicURL: "https://share.example.com/",
		Token:     testToken,
		TTL:       24 * time.Hour,
		MaxBytes:  1 << 20,
		Now:       clock.Now,
	}
	if mutate != nil {
		mutate(&cfg)
	}
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return s, clock, dir
}

func do(s *Server, method, target, token string, body []byte, hdr map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, target, bytes.NewReader(body))
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}

func upload(t *testing.T, s *Server, name string, body []byte) uploadResponse {
	t.Helper()
	w := do(s, "POST", "/api/items", testToken, body, map[string]string{"X-Ycc-Filename": name})
	if w.Code != http.StatusCreated {
		t.Fatalf("upload %s: %d %s", name, w.Code, w.Body)
	}
	var resp uploadResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	return resp
}

func dirEntries(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// Catches: uploads or deletes accepted without the right token, and a 401 that clients cannot
// recognise as an auth challenge.
func TestAuthRequired(t *testing.T) {
	s, _, dir := newTestServer(t, nil)
	for _, tok := range []string{"", "wrong", testToken + "x", testToken[:4]} {
		w := do(s, "POST", "/api/items", tok, []byte("hi"), map[string]string{"X-Ycc-Filename": "a.txt"})
		if w.Code != http.StatusUnauthorized || w.Header().Get("WWW-Authenticate") != "Bearer" {
			t.Fatalf("upload with token %q: %d %q", tok, w.Code, w.Header().Get("WWW-Authenticate"))
		}
	}
	if got := dirEntries(t, dir); len(got) != 0 {
		t.Fatalf("rejected uploads left files: %v", got)
	}
	item := upload(t, s, "a.txt", []byte("hi"))
	if w := do(s, "DELETE", "/api/items/"+item.ID, "wrong", nil, nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("delete with wrong token: %d", w.Code)
	}
	if w := do(s, "GET", "/"+item.ID, "", nil, nil); w.Code != http.StatusOK {
		t.Fatalf("item gone after unauthorized delete: %d", w.Code)
	}
}

// Catches: response URLs or metadata drifting from the documented contract, and names that
// keep directory components or control characters.
func TestUploadResponseAndStorage(t *testing.T) {
	s, clock, dir := newTestServer(t, nil)
	item := upload(t, s, "..%2Fsub%2Fmy%20re%0Aport.md", []byte("# hi\n"))
	if len(item.ID) != 22 || !validID(item.ID) {
		t.Fatalf("id %q", item.ID)
	}
	wantURL := "https://share.example.com/" + item.ID + "/my%20report.md"
	if item.URL != wantURL || item.RawURL != wantURL+"?raw=1" {
		t.Fatalf("urls %q %q", item.URL, item.RawURL)
	}
	if sum := sha256.Sum256([]byte("# hi\n")); item.Size != 5 || item.SHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("size %d sha256 %s", item.Size, item.SHA256)
	}
	if want := clock.Now().Add(24 * time.Hour).Format(time.RFC3339); item.ExpiresAt != want {
		t.Fatalf("expires_at %q, want %q", item.ExpiresAt, want)
	}
	m, err := s.readMeta(item.ID)
	if err != nil || m.Name != "my report.md" || !strings.HasPrefix(m.MediaType, "text/markdown") {
		t.Fatalf("meta %+v %v", m, err)
	}
	for _, name := range []string{item.ID + ".blob", item.ID + ".json"} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("%s: %v %v", name, info, err)
		}
	}
	if info, _ := os.Stat(dir); info.Mode().Perm() != 0o700 {
		t.Fatalf("data dir mode %v", info.Mode())
	}

	s2, _, _ := newTestServer(t, func(c *Config) { c.TTL = 0 })
	if got := upload(t, s2, "", []byte("x")); got.ExpiresAt != "" || !strings.HasSuffix(got.URL, "/file") {
		t.Fatalf("no-ttl upload: %+v", got)
	}
}

// Catches: malformed ids or traversal reaching the filesystem instead of a uniform 404.
func TestBadIDsAreNotFound(t *testing.T) {
	s, _, dir := newTestServer(t, nil)
	item := upload(t, s, "a.txt", []byte("hello"))
	// A file in the parent of the data dir that a traversal would hit.
	os.WriteFile(filepath.Join(filepath.Dir(dir), "secret.json"), []byte(`{"name":"x"}`), 0o600)

	for _, p := range []string{
		"/" + item.ID[:21],
		"/" + item.ID + "A",
		"/" + strings.Repeat("!", 22),
		"/..%2Fsecret",
		"/%2E%2E%2Fsecret/x",
		"/..%2F" + item.ID[:16],
		"/AAAAAAAAAAAAAAAAAAAAAA",
		"/" + item.ID + ".json",
		"/api/items",
	} {
		w := do(s, "GET", p, "", nil, nil)
		if w.Code != http.StatusNotFound {
			t.Errorf("GET %s: %d", p, w.Code)
		}
		checkCommonHeaders(t, p, w)
	}
	if w := do(s, "DELETE", "/api/items/..%2F"+item.ID[:19], testToken, nil, nil); w.Code != http.StatusNotFound {
		t.Errorf("delete traversal: %d", w.Code)
	}
}

// Catches: oversized uploads accepted, or partial blobs/temp files left on disk.
func TestOversizeUpload(t *testing.T) {
	s, _, dir := newTestServer(t, func(c *Config) { c.MaxBytes = 1000 })
	body := bytes.Repeat([]byte("x"), 1001)

	// Declared length over the limit.
	w := do(s, "POST", "/api/items", testToken, body, nil)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("declared oversize: %d", w.Code)
	}
	// Unknown length: the limit is enforced while streaming.
	r := httptest.NewRequest("POST", "/api/items", struct{ *bytes.Reader }{bytes.NewReader(body)})
	r.ContentLength = -1
	r.Header.Set("Authorization", "Bearer "+testToken)
	w = httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("streamed oversize: %d %s", w.Code, w.Body)
	}
	if got := dirEntries(t, dir); len(got) != 0 {
		t.Fatalf("oversize upload left files: %v", got)
	}
	upload(t, s, "ok.bin", body[:1000])
}

// Catches: expired items still served or deletable, and the sweep leaving their files (or
// deleting live items and in-progress uploads).
func TestExpiryAndSweep(t *testing.T) {
	s, clock, dir := newTestServer(t, nil)
	old := upload(t, s, "old.txt", []byte("old"))
	clock.Advance(12 * time.Hour)
	fresh := upload(t, s, "fresh.txt", []byte("fresh"))
	clock.Advance(12 * time.Hour)

	if w := do(s, "GET", "/"+old.ID+"/old.txt?raw=1", "", nil, nil); w.Code != http.StatusNotFound {
		t.Fatalf("expired item served: %d", w.Code)
	}
	if w := do(s, "GET", "/"+fresh.ID+"?raw=1", "", nil, nil); w.Code != http.StatusOK || w.Body.String() != "fresh" {
		t.Fatalf("live item: %d %q", w.Code, w.Body)
	}

	// An in-progress upload's temp file must survive; an hour-old one is debris, as is an
	// old blob whose metadata was never written.
	for name, age := range map[string]time.Duration{
		tmpPrefix + "live": time.Minute,
		tmpPrefix + "dead": 2 * time.Hour,
		newID() + ".blob":  2 * time.Hour,
	} {
		p := filepath.Join(dir, name)
		os.WriteFile(p, nil, 0o600)
		os.Chtimes(p, time.Time{}, clock.Now().Add(-age))
	}
	if err := s.Sweep(); err != nil {
		t.Fatal(err)
	}
	got := dirEntries(t, dir)
	want := []string{tmpPrefix + "live", fresh.ID + ".blob", fresh.ID + ".json"}
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("after sweep: %v, want %v", got, want)
	}
}

// Catches: delete leaving the item reachable, or distinguishing deleted from unknown ids.
func TestDelete(t *testing.T) {
	s, clock, dir := newTestServer(t, nil)
	item := upload(t, s, "a.txt", []byte("hello"))
	w := do(s, "DELETE", "/api/items/"+item.ID, testToken, nil, nil)
	if w.Code != http.StatusNoContent || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("delete: %d %q", w.Code, w.Header().Get("Cache-Control"))
	}
	if got := dirEntries(t, dir); len(got) != 0 {
		t.Fatalf("delete left files: %v", got)
	}
	if w := do(s, "GET", "/"+item.ID, "", nil, nil); w.Code != http.StatusNotFound {
		t.Fatalf("deleted item served: %d", w.Code)
	}
	if w := do(s, "DELETE", "/api/items/"+item.ID, testToken, nil, nil); w.Code != http.StatusNotFound {
		t.Fatalf("second delete: %d", w.Code)
	}
	expired := upload(t, s, "b.txt", []byte("bye"))
	clock.Advance(25 * time.Hour)
	if w := do(s, "DELETE", "/api/items/"+expired.ID, testToken, nil, nil); w.Code != http.StatusNotFound {
		t.Fatalf("delete expired: %d", w.Code)
	}
}

func checkCommonHeaders(t *testing.T, what string, w *httptest.ResponseRecorder) {
	t.Helper()
	h := w.Header()
	if h.Get("X-Content-Type-Options") != "nosniff" || h.Get("Referrer-Policy") != "no-referrer" ||
		h.Get("X-Robots-Tag") != "noindex, nofollow" {
		t.Errorf("%s: missing common headers: %v", what, h)
	}
}

// Catches: raw HTML executing on the share origin, viewer pages loosening their CSP, PDFs
// sandboxed into a blank frame, browsers caching revoked items, and download names breaking
// the header.
func TestServingHeaders(t *testing.T) {
	s, _, _ := newTestServer(t, nil)
	page := upload(t, s, "page.html", []byte("<script>alert(1)</script>"))
	pdf := upload(t, s, "doc.pdf", []byte("%PDF-1.4\n%%EOF\n"))
	png := upload(t, s, "pic.png", []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\x0dIHDR"))
	tricky := upload(t, s, `we"ird;name ü.txt`, []byte("text"))

	type want struct {
		path, ctype, csp string
	}
	cases := []want{
		{"/" + page.ID + "/page.html", "text/html; charset=utf-8", viewerCSP},
		{"/" + page.ID + "?raw=1", "text/plain; charset=utf-8", rawCSP},
		{"/" + page.ID + "/page.html?render=1", "text/html; charset=utf-8", renderCSP},
		{"/" + pdf.ID + "/doc.pdf", "text/html; charset=utf-8", viewerCSP},
		{"/" + pdf.ID + "/doc.pdf?raw=1", "application/pdf", "frame-ancestors 'self'"},
		{"/" + png.ID + "?raw=1", "image/png", rawCSP},
		{"/" + png.ID, "text/html; charset=utf-8", viewerCSP},
	}
	for _, c := range cases {
		w := do(s, "GET", c.path, "", nil, nil)
		if w.Code != http.StatusOK {
			t.Errorf("%s: %d", c.path, w.Code)
			continue
		}
		checkCommonHeaders(t, c.path, w)
		h := w.Header()
		if h.Get("Content-Type") != c.ctype {
			t.Errorf("%s: content type %q, want %q", c.path, h.Get("Content-Type"), c.ctype)
		}
		if got, ok := h["Content-Security-Policy"]; c.csp == "" && ok || c.csp != "" && (len(got) != 1 || got[0] != c.csp) {
			t.Errorf("%s: CSP %q, want %q", c.path, got, c.csp)
		}
		// Revalidated every time, so an unpublished link stops serving at once.
		if h.Get("Cache-Control") != "private, no-cache" {
			t.Errorf("%s: cache-control %q", c.path, h.Get("Cache-Control"))
		}
	}
	if w := do(s, "GET", "/"+page.ID+"?raw=1", "", nil, nil); w.Body.String() != "<script>alert(1)</script>" {
		t.Errorf("raw body %q", w.Body)
	}
	if w := do(s, "GET", "/"+png.ID+"?render=1", "", nil, nil); w.Code != http.StatusNotFound {
		t.Errorf("render of non-HTML: %d", w.Code)
	}
	w := do(s, "GET", "/"+tricky.ID+"?download=1", "", nil, nil)
	if cd := w.Header().Get("Content-Disposition"); cd != `attachment; filename*=utf-8''we%22ird%3Bname%20%C3%BC.txt` {
		t.Errorf("download disposition %q", cd)
	}
	// The viewer embeds HTML only through a sandboxed frame of ?render=1.
	body := do(s, "GET", "/"+page.ID, "", nil, nil).Body.String()
	if !strings.Contains(body, `<iframe src="?render=1"`) || !strings.Contains(body, `sandbox="allow-scripts allow-popups allow-popups-to-escape-sandbox allow-modals"`) ||
		strings.Contains(body, "<script") {
		t.Errorf("html viewer page:\n%s", body)
	}
	for _, p := range []string{"/", "/robots.txt", "/healthz"} {
		checkCommonHeaders(t, p, do(s, "GET", p, "", nil, nil))
	}
	w = do(s, "POST", "/api/items", testToken, []byte("x"), nil)
	checkCommonHeaders(t, "upload", w)
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("upload cache-control %q", w.Header().Get("Cache-Control"))
	}
}

// Catches: raw HTML in Markdown rendering as markup on the share origin.
func TestMarkdownOmitsRawHTML(t *testing.T) {
	s, _, _ := newTestServer(t, nil)
	src := "# Title\n\n<script>alert('x')</script>\n\nInline <img src=x onerror=alert(1)> and [link](javascript:alert(1))\n\n| a | b |\n|---|---|\n| 1 | 2 |\n"
	item := upload(t, s, "notes.md", []byte(src))
	body := do(s, "GET", "/"+item.ID+"/notes.md", "", nil, nil).Body.String()
	for _, bad := range []string{"<script", "onerror", "javascript:"} {
		if strings.Contains(body, bad) {
			t.Errorf("markdown page contains %q:\n%s", bad, body)
		}
	}
	if !strings.Contains(body, `<h1 id="title">Title</h1>`) || !strings.Contains(body, "<table>") {
		t.Errorf("markdown not rendered:\n%s", body)
	}
}

// Catches: an unauthenticated viewer making the server render unbounded pages, which every
// view repeats: many short lines, and Markdown beyond the text cap.
func TestViewerRenderingIsBounded(t *testing.T) {
	s, _, _ := newTestServer(t, func(c *Config) { c.MaxBytes = 8 << 20 })
	lines := upload(t, s, "many.txt", bytes.Repeat([]byte("x\n"), maxTextLines+10))
	body := do(s, "GET", "/"+lines.ID, "", nil, nil).Body.String()
	if got := strings.Count(body, "<span>x</span>"); got != maxTextLines {
		t.Errorf("text page shows %d lines, want %d", got, maxTextLines)
	}
	if !strings.Contains(body, "View the whole file as Raw") {
		t.Error("truncated text page lacks the Raw note")
	}

	big := append([]byte("# Big\n\n"), bytes.Repeat([]byte("word "), maxTextView/5+10)...)
	md := upload(t, s, "big.md", big)
	body = do(s, "GET", "/"+md.ID, "", nil, nil).Body.String()
	if strings.Contains(body, `<h1 id="big">`) || !strings.Contains(body, "View the whole file as Raw") {
		t.Error("oversized markdown was rendered instead of shown as capped text")
	}
}

// Catches: binary files rendered as text, and source files misclassified by host mime.types.
func TestTextDetection(t *testing.T) {
	s, _, _ := newTestServer(t, nil)
	cases := []struct {
		name string
		body []byte
		text bool
	}{
		{"main.go", []byte("package main\n\nfunc main() {}\n"), true},
		{"app.ts", []byte("export const x = 1\n"), true},
		{"Makefile", []byte("all:\n\tgo build\n"), true},
		{"data.json", []byte(`{"a": 1}`), true},
		{"blob.go", []byte("pack\x00age"), false},
		{"latin1.txt", []byte("caf\xe9\n"), false},
	}
	for _, c := range cases {
		item := upload(t, s, c.name, c.body)
		body := do(s, "GET", "/"+item.ID, "", nil, nil).Body.String()
		if got := strings.Contains(body, `<pre class="lines"`); got != c.text {
			t.Errorf("%s: shown as text = %v, want %v", c.name, got, c.text)
		}
	}
}
