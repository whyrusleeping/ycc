// Package shareserver implements ycc-share, the public server that stores files uploaded by the
// daemon and serves each one at an unguessable URL.
package shareserver

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Config configures a Server.
type Config struct {
	Dir       string        // data directory, created 0700 if missing
	PublicURL string        // externally visible base URL used to build links
	Token     string        // upload bearer token; required
	TTL       time.Duration // lifetime of each upload; 0 keeps uploads until deleted
	MaxBytes  int64         // cap on one upload; required
	Now       func() time.Time
}

// Server serves the share API and viewer pages. It is an http.Handler.
type Server struct {
	dir       string
	publicURL string
	tokenSum  [32]byte
	ttl       time.Duration
	maxBytes  int64
	now       func() time.Time
	mux       *http.ServeMux
}

// New validates cfg, creates the data directory, and returns the server.
func New(cfg Config) (*Server, error) {
	if cfg.Dir == "" {
		return nil, errors.New("data directory is required")
	}
	if cfg.Token == "" {
		return nil, errors.New("upload token is required")
	}
	if cfg.MaxBytes <= 0 {
		return nil, errors.New("max bytes must be positive")
	}
	if cfg.TTL < 0 {
		return nil, errors.New("ttl must not be negative")
	}
	u, err := url.Parse(cfg.PublicURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("public URL %q must be an absolute http(s) URL", cfg.PublicURL)
	}
	if err := os.MkdirAll(cfg.Dir, 0o700); err != nil {
		return nil, err
	}
	s := &Server{
		dir:       cfg.Dir,
		publicURL: strings.TrimRight(cfg.PublicURL, "/"),
		tokenSum:  sha256.Sum256([]byte(cfg.Token)),
		ttl:       cfg.TTL,
		maxBytes:  cfg.MaxBytes,
		now:       cfg.Now,
	}
	if s.now == nil {
		s.now = time.Now
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/items", s.handleUpload)
	mux.HandleFunc("DELETE /api/items/{id}", s.handleDelete)
	mux.HandleFunc("GET /robots.txt", handleRobots)
	mux.HandleFunc("GET /healthz", handleHealth)
	mux.HandleFunc("GET /{$}", handleIndex)
	mux.HandleFunc("GET /{id}", s.handleItem)
	mux.HandleFunc("GET /{id}/{name...}", s.handleItem)
	s.mux = mux
	return s, nil
}

// ServeHTTP adds the headers every response carries and dispatches the request.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Robots-Tag", "noindex, nofollow")
	s.mux.ServeHTTP(w, r)
}

// Run sweeps expired items now and then hourly until ctx is done.
func (s *Server) Run(ctx context.Context) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		if err := s.Sweep(); err != nil {
			log.Printf("ycc-share: sweep: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (s *Server) authorized(r *http.Request) bool {
	tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok {
		return false
	}
	// Hashing first keeps the comparison constant-time regardless of length.
	sum := sha256.Sum256([]byte(strings.TrimSpace(tok)))
	return subtle.ConstantTimeCompare(sum[:], s.tokenSum[:]) == 1
}

type uploadResponse struct {
	ID        string `json:"id"`
	URL       string `json:"url"`
	RawURL    string `json:"raw_url"`
	Size      int64  `json:"size"`
	SHA256    string `json:"sha256"`
	ExpiresAt string `json:"expires_at"`
}

func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !s.authorized(r) {
		w.Header().Set("WWW-Authenticate", "Bearer")
		http.Error(w, "invalid or missing upload token", http.StatusUnauthorized)
		return
	}
	if r.ContentLength > s.maxBytes {
		http.Error(w, s.tooLargeMessage(), http.StatusRequestEntityTooLarge)
		return
	}
	rawName, err := url.PathUnescape(r.Header.Get("X-Ycc-Filename"))
	if err != nil {
		http.Error(w, "X-Ycc-Filename is not valid percent-encoding", http.StatusBadRequest)
		return
	}
	name := cleanName(rawName)
	id, m, err := s.store(http.MaxBytesReader(w, r.Body, s.maxBytes), name)
	if err != nil {
		if mbe := (*http.MaxBytesError)(nil); errors.As(err, &mbe) {
			http.Error(w, s.tooLargeMessage(), http.StatusRequestEntityTooLarge)
			return
		}
		log.Printf("ycc-share: upload: %v", err)
		http.Error(w, "storing the upload failed", http.StatusInternalServerError)
		return
	}
	itemURL := s.publicURL + "/" + id + "/" + url.PathEscape(name)
	resp := uploadResponse{
		ID:     id,
		URL:    itemURL,
		RawURL: itemURL + "?raw=1",
		Size:   m.Size,
		SHA256: m.SHA256,
	}
	if !m.ExpiresAt.IsZero() {
		resp.ExpiresAt = m.ExpiresAt.Format(time.RFC3339)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(resp)
}

func (s *Server) tooLargeMessage() string {
	return fmt.Sprintf("file exceeds the %s upload limit", humanSize(s.maxBytes))
}

func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !s.authorized(r) {
		w.Header().Set("WWW-Authenticate", "Bearer")
		http.Error(w, "invalid or missing upload token", http.StatusUnauthorized)
		return
	}
	id := r.PathValue("id")
	if !validID(id) {
		http.NotFound(w, r)
		return
	}
	m, err := s.readMeta(id)
	if errors.Is(err, fs.ErrNotExist) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		// Not 404: the daemon forgets a link on 404, and this one may be live.
		log.Printf("ycc-share: delete %s: %v", id, err)
		http.Error(w, "reading the item failed", http.StatusInternalServerError)
		return
	}
	if err := s.remove(id); err != nil {
		log.Printf("ycc-share: delete %s: %v", id, err)
		http.Error(w, "deleting the item failed", http.StatusInternalServerError)
		return
	}
	if m.expired(s.now()) {
		http.NotFound(w, r)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// cleanName cuts a client-supplied name to its last path component and drops control
// characters and invalid UTF-8.
func cleanName(name string) string {
	if i := strings.LastIndexAny(name, `/\`); i >= 0 {
		name = name[i+1:]
	}
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, strings.ToValidUTF8(name, ""))
	if name == "" || name == "." || name == ".." {
		return "file"
	}
	if len(name) > maxNameBytes {
		// Keep the extension: it decides how the file is shown.
		ext := path.Ext(name)
		if len(ext) > maxNameBytes/2 {
			ext = ""
		}
		stem := name[:maxNameBytes-len(ext)]
		for len(stem) > 0 && !utf8.ValidString(stem) {
			stem = stem[:len(stem)-1]
		}
		name = stem + ext
	}
	return name
}

// maxNameBytes bounds a stored file name.
const maxNameBytes = 200

func handleRobots(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write([]byte("User-agent: *\nDisallow: /\n"))
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Write([]byte("ok\n"))
}

func handleIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Security-Policy", viewerCSP)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := indexTemplate.Execute(w, nil); err != nil {
		log.Printf("ycc-share: index: %v", err)
	}
}

// disposition formats a Content-Disposition value with the file name safely encoded.
func disposition(kind, name string) string {
	if v := mime.FormatMediaType(kind, map[string]string{"filename": name}); v != "" {
		return v
	}
	return kind
}
