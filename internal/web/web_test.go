package web

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// webRoot is the client source tree relative to this package.
const webRoot = "../../clients/web"

type buildManifest struct {
	Algorithm  string   `json:"algorithm"`
	InputsHash string   `json:"inputs_hash"`
	InputFiles []string `json:"input_files"`
	InputDirs  []string `json:"input_dirs"`
	FileCount  int      `json:"file_count"`
}

// inputsHash mirrors clients/web/scripts/inputs-hash.mjs ("sha256-v1"): the
// listed files plus every regular file under the listed directories (skipping
// dot-entries), as "/"-separated paths relative to clients/web, sorted
// bytewise; sha256 over "<path>\n<sha256hex(content)>\n" for each.
func inputsHash(t *testing.T, root string, files, dirs []string) (string, int) {
	t.Helper()
	var paths []string
	for _, f := range files {
		info, err := os.Stat(filepath.Join(root, f))
		if err != nil {
			t.Fatalf("build input %s: %v", f, err)
		}
		if info.Mode().IsRegular() {
			paths = append(paths, f)
		}
	}
	for _, dir := range dirs {
		err := filepath.WalkDir(filepath.Join(root, dir), func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if strings.HasPrefix(d.Name(), ".") && p != filepath.Join(root, dir) {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if !d.Type().IsRegular() {
				return nil
			}
			rel, err := filepath.Rel(root, p)
			if err != nil {
				return err
			}
			paths = append(paths, filepath.ToSlash(rel))
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", dir, err)
		}
	}
	sort.Strings(paths)
	outer := sha256.New()
	for _, p := range paths {
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(p)))
		if err != nil {
			t.Fatalf("read %s: %v", p, err)
		}
		inner := sha256.Sum256(data)
		io.WriteString(outer, p+"\n"+hex.EncodeToString(inner[:])+"\n")
	}
	return hex.EncodeToString(outer.Sum(nil)), len(paths)
}

// TestBundleFresh fails when the committed bundle in dist was built from
// different sources than those in clients/web. It needs no Node: rebuild with
// scripts/web-build.sh (or `npm run build` in clients/web) and commit dist.
func TestBundleFresh(t *testing.T) {
	raw, err := fs.ReadFile(assets, "dist/manifest.json")
	if err != nil {
		t.Fatalf("embedded dist/manifest.json: %v (build the web client)", err)
	}
	var m buildManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("parse manifest: %v", err)
	}
	if m.Algorithm != "sha256-v1" {
		t.Fatalf("manifest algorithm %q; update this test to match scripts/inputs-hash.mjs", m.Algorithm)
	}
	if len(m.InputFiles) == 0 || len(m.InputDirs) == 0 {
		t.Fatalf("manifest declares no build inputs: %+v", m)
	}
	got, n := inputsHash(t, webRoot, m.InputFiles, m.InputDirs)
	if got != m.InputsHash {
		t.Fatalf("internal/web/dist is stale: clients/web inputs hash %s (%d files), manifest has %s (%d files).\n"+
			"Rebuild with scripts/web-build.sh and commit internal/web/dist.", got[:12], n, m.InputsHash[:12], m.FileCount)
	}
}

func get(t *testing.T, h http.Handler, method, target string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, target, nil))
	return rec
}

func firstAsset(t *testing.T) string {
	t.Helper()
	entries, err := fs.ReadDir(assets, "dist/assets")
	if err != nil || len(entries) == 0 {
		t.Fatalf("no embedded assets: %v", err)
	}
	return "/assets/" + entries[0].Name()
}

func TestHandlerRoutingAndCaching(t *testing.T) {
	h := Handler()
	index, err := fs.ReadFile(assets, "dist/index.html")
	if err != nil {
		t.Fatal(err)
	}

	// The document and every client-side route serve index.html, uncached.
	for _, p := range []string{"/", "/index.html", "/p/my.proj/s/s_0123", "/s/s_0123", "/new", "/p/x/backlog"} {
		rec := get(t, h, http.MethodGet, p)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s: %d", p, rec.Code)
		}
		if rec.Body.String() != string(index) {
			t.Errorf("GET %s: body is not index.html", p)
		}
		if cc := rec.Header().Get("Cache-Control"); cc != "no-cache" {
			t.Errorf("GET %s: Cache-Control %q, want no-cache", p, cc)
		}
		if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
			t.Errorf("GET %s: Content-Type %q", p, ct)
		}
		if csp := rec.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "script-src 'self'") {
			t.Errorf("GET %s: missing CSP, got %q", p, csp)
		}
	}

	// Fingerprinted assets are immutable.
	asset := firstAsset(t)
	rec := get(t, h, http.MethodGet, asset)
	if rec.Code != http.StatusOK || rec.Body.Len() == 0 {
		t.Fatalf("GET %s: %d (%d bytes)", asset, rec.Code, rec.Body.Len())
	}
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "immutable") {
		t.Errorf("GET %s: Cache-Control %q, want immutable", asset, cc)
	}

	// A missing asset is a real 404, not the SPA shell.
	if rec := get(t, h, http.MethodGet, "/assets/missing-0000.js"); rec.Code != http.StatusNotFound {
		t.Errorf("missing asset: %d, want 404", rec.Code)
	}
	if rec := get(t, h, http.MethodPost, "/p/x"); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST: %d, want 405", rec.Code)
	}
}
