package shareserver

import (
	"bytes"
	"html/template"
	"io"
	"log"
	"mime"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
)

const (
	viewerCSP = "default-src 'none'; style-src 'unsafe-inline'; img-src 'self' data: https:; " +
		"media-src 'self'; frame-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"
	renderCSP = "sandbox allow-scripts allow-popups allow-popups-to-escape-sandbox allow-modals; " +
		"frame-ancestors 'self'"
	rawCSP = "sandbox; default-src 'none'; img-src 'self' data:; style-src 'unsafe-inline'; " +
		"media-src 'self'; frame-ancestors 'self'"

	sniffLen    = 8 << 10 // bytes examined to decide whether content is text
	maxTextView = 2 << 20 // bytes of plain text shown, or Markdown rendered, on a viewer page
	// maxTextLines bounds the rows of a text page: many short lines inflate the
	// page far beyond the bytes shown.
	maxTextLines = 50_000
)

// extTypes take precedence over mime.TypeByExtension, whose answers depend on the host's
// mime.types (which maps .ts to video/mp2t and .rs to an XML type, for example).
var extTypes = map[string]string{
	".md":       "text/markdown; charset=utf-8",
	".markdown": "text/markdown; charset=utf-8",
}

func init() {
	for _, ext := range strings.Fields(`.txt .text .log .go .mod .sum .rs .py .rb .java .kt .kts .scala
		.c .h .cc .cpp .cxx .hh .hpp .m .mm .cs .fs .swift .dart .ts .tsx .jsx .mts .cts .vue .svelte
		.sh .bash .zsh .fish .ps1 .bat .lua .pl .pm .php .r .jl .hs .ml .mli .ex .exs .erl .hrl .clj
		.cljs .el .lisp .scm .zig .nim .v .sql .proto .graphql .gql .toml .yaml .yml .ini .cfg .conf
		.env .properties .gradle .cmake .mk .make .dockerfile .tf .hcl .nix .diff .patch .jsonl
		.ndjson .tex .rst .adoc .org .lock .gitignore .editorconfig`) {
		extTypes[ext] = "text/plain; charset=utf-8"
	}
}

// detectMediaType picks a media type by extension first and by sniffing head second. Text
// types gain charset=utf-8 when the content looks like UTF-8 text.
func detectMediaType(name string, head []byte) string {
	ext := strings.ToLower(path.Ext(name))
	t := extTypes[ext]
	if t == "" && ext != "" {
		t = mime.TypeByExtension(ext)
	}
	if t == "" {
		t = http.DetectContentType(head)
	}
	base, params, err := mime.ParseMediaType(t)
	if err != nil {
		return "application/octet-stream"
	}
	if params["charset"] == "" && textual(base) && looksText(head) {
		params["charset"] = "utf-8"
		if f := mime.FormatMediaType(base, params); f != "" {
			t = f
		}
	}
	return t
}

func baseType(t string) string {
	base, _, err := mime.ParseMediaType(t)
	if err != nil {
		return "application/octet-stream"
	}
	return base
}

// textual reports whether a media type is text that a person might read as source.
func textual(base string) bool {
	switch base {
	case "application/json", "application/xml", "application/javascript", "application/ecmascript",
		"application/x-javascript", "application/x-sh", "application/x-shellscript", "application/toml",
		"application/yaml", "application/x-yaml", "application/sql", "application/x-tex",
		"application/x-latex", "application/x-httpd-php":
		return true
	}
	if strings.HasPrefix(base, "image/") {
		return false
	}
	return strings.HasPrefix(base, "text/") || strings.HasSuffix(base, "+json") || strings.HasSuffix(base, "+xml")
}

// looksText reports whether a content sample is UTF-8 without NUL bytes. A rune cut off at
// the end of the sample is tolerated.
func looksText(b []byte) bool {
	if bytes.IndexByte(b, 0) >= 0 {
		return false
	}
	for j := len(b) - 1; j >= 0 && j >= len(b)-utf8.UTFMax; j-- {
		if utf8.RuneStart(b[j]) {
			if !utf8.FullRune(b[j:]) {
				b = b[:j]
			}
			break
		}
	}
	return utf8.Valid(b)
}

// rawType is the media type served for ?raw=1: types a browser would execute are shown as
// their source instead, and other text (Markdown, YAML, ...) as plain text so browsers display
// rather than download it.
func rawType(t string) string {
	switch base := baseType(t); {
	case strings.HasPrefix(base, "text/"), base == "application/xhtml+xml",
		base == "application/xml", base == "text/javascript", base == "application/javascript",
		base == "application/ecmascript", base == "text/ecmascript", base == "application/x-javascript",
		strings.HasSuffix(base, "+xml") && base != "image/svg+xml":
		return "text/plain; charset=utf-8"
	}
	return t
}

type kind string

const (
	kindMarkdown kind = "markdown"
	kindHTML     kind = "html"
	kindImage    kind = "image"
	kindPDF      kind = "pdf"
	kindAudio    kind = "audio"
	kindVideo    kind = "video"
	kindText     kind = "text"
	kindBinary   kind = "binary"
)

var imageTypes = map[string]bool{
	"image/png": true, "image/jpeg": true, "image/gif": true, "image/webp": true, "image/avif": true,
	"image/svg+xml": true, "image/bmp": true, "image/x-icon": true, "image/vnd.microsoft.icon": true,
}

// extKind decides the kinds that depend on the name alone.
func extKind(name string) kind {
	switch strings.ToLower(path.Ext(name)) {
	case ".md", ".markdown":
		return kindMarkdown
	case ".html", ".htm":
		return kindHTML
	}
	return ""
}

func itemKind(m *meta, head []byte) kind {
	if k := extKind(m.Name); k != "" {
		return k
	}
	base := baseType(m.MediaType)
	switch {
	case imageTypes[base]:
		return kindImage
	case base == "application/pdf":
		return kindPDF
	case strings.HasPrefix(base, "audio/"):
		return kindAudio
	case strings.HasPrefix(base, "video/"):
		return kindVideo
	case textual(base) && looksText(head):
		return kindText
	}
	return kindBinary
}

func (s *Server) handleItem(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	m := s.lookup(id)
	if m == nil {
		http.NotFound(w, r)
		return
	}
	f, err := os.Open(s.blobPath(id))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()

	h := w.Header()
	q := r.URL.Query()
	switch {
	case q.Get("download") == "1":
		s.serveBlob(w, r, f, m, m.MediaType, "attachment")
	case q.Get("raw") == "1":
		s.serveBlob(w, r, f, m, rawType(m.MediaType), "inline")
	case q.Get("render") == "1":
		if extKind(m.Name) != kindHTML {
			http.NotFound(w, r)
			return
		}
		h.Set("Content-Security-Policy", renderCSP)
		s.serveBlob(w, r, f, m, "text/html; charset=utf-8", "")
	default:
		s.serveViewer(w, f, m)
	}
}

func (s *Server) serveBlob(w http.ResponseWriter, r *http.Request, f *os.File, m *meta, ctype, disp string) {
	h := w.Header()
	h.Set("Cache-Control", "private, no-cache")
	h.Set("Content-Type", ctype)
	h.Set("ETag", `"`+m.SHA256+`"`)
	if disp != "" {
		h.Set("Content-Disposition", disposition(disp, m.Name))
		// Browser PDF viewers do not render under a CSP sandbox.
		if baseType(ctype) != "application/pdf" {
			h.Set("Content-Security-Policy", rawCSP)
		} else {
			h.Set("Content-Security-Policy", "frame-ancestors 'self'")
		}
	}
	http.ServeContent(w, r, "", m.CreatedAt, f)
}

type viewData struct {
	Name         string
	Kind         kind
	MediaType    string
	Size         string
	Published    string
	PublishedISO string
	Expires      string
	ExpiresISO   string

	Markdown template.HTML

	Lines     []string
	Digits    int
	Truncated bool
	Shown     string
}

func (s *Server) serveViewer(w http.ResponseWriter, f *os.File, m *meta) {
	head := make([]byte, sniffLen)
	n, _ := io.ReadFull(f, head)
	head = head[:n]
	d := viewData{
		Name:         m.Name,
		Kind:         itemKind(m, head),
		MediaType:    baseType(m.MediaType),
		Size:         humanSize(m.Size),
		Published:    m.CreatedAt.UTC().Format("Jan 2, 2006"),
		PublishedISO: m.CreatedAt.UTC().Format(time.RFC3339),
	}
	if !m.ExpiresAt.IsZero() {
		d.Expires = m.ExpiresAt.UTC().Format("Jan 2, 2006")
		d.ExpiresISO = m.ExpiresAt.UTC().Format(time.RFC3339)
	}

	// Every view renders afresh, so larger Markdown is shown as capped text.
	if d.Kind == kindMarkdown && m.Size > maxTextView {
		d.Kind = kindText
	}
	switch d.Kind {
	case kindMarkdown:
		src, err := readAllFrom(f, 0)
		if err != nil {
			s.viewerError(w, err)
			return
		}
		var buf bytes.Buffer
		if err := markdown.Convert(src, &buf); err != nil {
			s.viewerError(w, err)
			return
		}
		d.Markdown = template.HTML(buf.String())
	case kindText:
		data, err := readAllFrom(f, maxTextView)
		if err != nil {
			s.viewerError(w, err)
			return
		}
		if m.Size > int64(len(data)) {
			d.Truncated = true
			// Show whole lines only.
			if i := bytes.LastIndexByte(data, '\n'); i > 0 {
				data = data[:i+1]
			}
			d.Shown = humanSize(int64(len(data)))
		}
		d.Lines = splitLines(strings.ToValidUTF8(string(data), "\uFFFD"))
		if len(d.Lines) > maxTextLines {
			d.Lines = d.Lines[:maxTextLines]
			d.Truncated = true
			shown := 0
			for _, l := range d.Lines {
				shown += len(l) + 1
			}
			d.Shown = humanSize(int64(shown))
		}
		d.Digits = len(strconv.Itoa(len(d.Lines)))
	}

	h := w.Header()
	h.Set("Content-Security-Policy", viewerCSP)
	h.Set("Cache-Control", "private, no-cache")
	h.Set("Content-Type", "text/html; charset=utf-8")
	var buf bytes.Buffer
	if err := viewerTemplate.Execute(&buf, d); err != nil {
		s.viewerError(w, err)
		return
	}
	w.Write(buf.Bytes())
}

func (s *Server) viewerError(w http.ResponseWriter, err error) {
	log.Printf("ycc-share: viewer: %v", err)
	http.Error(w, "rendering the page failed", http.StatusInternalServerError)
}

// readAllFrom reads f from the start, at most limit bytes when limit > 0.
func readAllFrom(f *os.File, limit int64) ([]byte, error) {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	var r io.Reader = f
	if limit > 0 {
		r = io.LimitReader(f, limit)
	}
	return io.ReadAll(r)
}

func splitLines(s string) []string {
	s = strings.TrimSuffix(s, "\n")
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimSuffix(l, "\r")
	}
	return lines
}

func humanSize(n int64) string {
	if n == 1 {
		return "1 byte"
	}
	if n < 1024 {
		return strconv.FormatInt(n, 10) + " bytes"
	}
	v := float64(n)
	unit := ""
	for _, u := range []string{"KiB", "MiB", "GiB", "TiB"} {
		v /= 1024
		unit = u
		if v < 1024 {
			break
		}
	}
	s := strconv.FormatFloat(v, 'f', 1, 64)
	return strings.TrimSuffix(s, ".0") + " " + unit
}

// markdown renders GitHub-flavoured Markdown. Raw HTML in the source is omitted because
// html.WithUnsafe is not set.
var markdown = goldmark.New(
	goldmark.WithExtensions(extension.GFM),
	goldmark.WithParserOptions(parser.WithAutoHeadingID()),
)

const pageStyle = `
:root {
  color-scheme: light dark;
  --bg: #ffffff; --fg: #1f2328; --muted: #59636e; --border: #d1d9e0;
  --subtle: #f6f8fa; --link: #0969da; --button: #1f883d; --button-fg: #ffffff;
}
@media (prefers-color-scheme: dark) {
  :root {
    --bg: #0d1117; --fg: #e6edf3; --muted: #9198a1; --border: #3d444d;
    --subtle: #151b23; --link: #4493f8; --button: #238636;
  }
}
*, ::before, ::after { box-sizing: border-box; }
html, body { margin: 0; background: var(--bg); color: var(--fg); }
body {
  font: 16px/1.5 -apple-system, BlinkMacSystemFont, "Segoe UI", "Noto Sans", Helvetica, Arial,
    sans-serif, "Apple Color Emoji", "Segoe UI Emoji";
  min-height: 100vh; display: flex; flex-direction: column;
}
a { color: var(--link); text-decoration: none; }
a:hover { text-decoration: underline; }
pre { tab-size: 4; }
code, pre, kbd, samp {
  font-family: ui-monospace, SFMono-Regular, "SF Mono", Menlo, Consolas, "Liberation Mono", monospace;
}
.bar {
  display: flex; flex-wrap: wrap; align-items: center; gap: 8px 24px;
  padding: 12px 20px; border-bottom: 1px solid var(--border); background: var(--subtle);
}
.bar .title { flex: 1 1 auto; min-width: 0; }
.bar h1 {
  margin: 0; font-size: 17px; font-weight: 600; line-height: 1.3; overflow-wrap: anywhere;
}
.bar .meta { margin: 2px 0 0; font-size: 13px; color: var(--muted); }
.bar nav { display: flex; gap: 8px; flex: none; }
.bar nav a {
  font-size: 13px; font-weight: 500; padding: 4px 12px; border: 1px solid var(--border);
  border-radius: 6px; color: var(--fg); background: var(--bg);
}
.bar nav a:hover { text-decoration: none; border-color: var(--muted); }
main { flex: 1 1 auto; display: flex; flex-direction: column; min-height: 0; }

.frame { flex: 1 1 auto; height: 100vh; }
body.fill { height: 100vh; height: 100dvh; min-height: 0; }
body.fill main { overflow: hidden; }
.frame iframe { display: block; width: 100%; height: 100%; border: 0; background: #ffffff; }

.markdown { width: 100%; max-width: 900px; margin: 0 auto; padding: 32px 24px 64px; overflow-wrap: break-word; }
.markdown > :first-child { margin-top: 0; }
.markdown h1, .markdown h2, .markdown h3, .markdown h4, .markdown h5, .markdown h6 {
  margin: 1.5em 0 0.6em; font-weight: 600; line-height: 1.25;
}
.markdown h1 { font-size: 2em; padding-bottom: 0.3em; border-bottom: 1px solid var(--border); }
.markdown h2 { font-size: 1.5em; padding-bottom: 0.3em; border-bottom: 1px solid var(--border); }
.markdown h3 { font-size: 1.25em; }
.markdown h4 { font-size: 1em; }
.markdown h5 { font-size: 0.875em; }
.markdown h6 { font-size: 0.85em; color: var(--muted); }
.markdown p, .markdown ul, .markdown ol, .markdown blockquote, .markdown table, .markdown pre { margin: 0 0 16px; }
.markdown ul, .markdown ol { padding-left: 2em; }
.markdown li + li { margin-top: 0.25em; }
.markdown li > input[type=checkbox] { margin: 0 0.4em 0.2em -1.4em; vertical-align: middle; }
.markdown li:has(> input[type=checkbox]) { list-style: none; }
.markdown blockquote { padding: 0 1em; color: var(--muted); border-left: 0.25em solid var(--border); }
.markdown code { font-size: 85%; padding: 0.2em 0.4em; border-radius: 6px; background: var(--subtle); }
.markdown pre {
  font-size: 85%; line-height: 1.45; padding: 16px; overflow: auto; border-radius: 6px;
  background: var(--subtle); border: 1px solid var(--border);
}
.markdown pre code { font-size: 100%; padding: 0; background: transparent; }
.markdown table { border-collapse: collapse; display: block; max-width: 100%; overflow: auto; }
.markdown th, .markdown td { padding: 6px 13px; border: 1px solid var(--border); }
.markdown th { font-weight: 600; background: var(--subtle); }
.markdown hr { height: 0.25em; margin: 24px 0; border: 0; background: var(--border); }
.markdown img { max-width: 100%; }
.markdown del { color: var(--muted); }

.media { flex: 1 1 auto; display: flex; align-items: center; justify-content: center; padding: 24px; }
.media img, .media video { max-width: 100%; max-height: calc(100vh - 120px); height: auto; }
.media img {
  background-color: #ffffff;
  background-image: conic-gradient(#e8e8e8 25%, transparent 0 50%, #e8e8e8 0 75%, transparent 0);
  background-size: 16px 16px;
}
.media audio { width: min(600px, 100%); }

.note { margin: 0; padding: 8px 20px; font-size: 13px; color: var(--muted); border-bottom: 1px solid var(--border); }
/* Line numbers sit in the left padding (an inline-block with zero net advance), so tab stops
   still count from the start of the code. */
pre.lines {
  margin: 0; padding: 12px 16px 12px calc(var(--gw, 3) * 1ch + 36px);
  font-size: 13px; line-height: 1.5; overflow-x: auto; counter-reset: ln;
}
pre.lines span { counter-increment: ln; }
pre.lines span::before {
  content: counter(ln); display: inline-block; width: calc(var(--gw, 3) * 1ch + 28px);
  margin: 0 8px 0 calc(var(--gw, 3) * -1ch - 36px); padding: 0 12px 0 16px;
  text-align: right; color: var(--muted); opacity: 0.7; user-select: none; -webkit-user-select: none;
}

.empty { flex: 1 1 auto; display: flex; flex-direction: column; align-items: center; justify-content: center; gap: 16px; padding: 48px 24px; text-align: center; }
.empty p { margin: 0; color: var(--muted); }
.button {
  display: inline-block; padding: 8px 20px; border-radius: 6px; font-weight: 600;
  background: var(--button); color: var(--button-fg);
}
.button:hover { text-decoration: none; filter: brightness(1.1); }
`

var viewerTemplate = template.Must(template.New("viewer").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="robots" content="noindex">
<title>{{.Name}}</title>
<style>` + pageStyle + `</style>
</head>
<body class="k-{{.Kind}}{{if or (eq .Kind "html") (eq .Kind "pdf")}} fill{{end}}">
<header class="bar">
  <div class="title">
    <h1>{{.Name}}</h1>
    <p class="meta">{{.Size}} · published <time datetime="{{.PublishedISO}}">{{.Published}}</time>
      {{- if .Expires}} · expires <time datetime="{{.ExpiresISO}}">{{.Expires}}</time>{{end}}</p>
  </div>
  <nav>
    {{- if eq .Kind "pdf"}}<a href="?raw=1" target="_blank" rel="noopener noreferrer">Open PDF</a>{{else}}<a href="?raw=1">Raw</a>{{end -}}
    <a href="?download=1" download>Download</a>
  </nav>
</header>
<main>
{{- if eq .Kind "markdown"}}
<article class="markdown">
{{.Markdown}}
</article>
{{- else if eq .Kind "html"}}
<div class="frame"><iframe src="?render=1" title="{{.Name}}" sandbox="allow-scripts allow-popups allow-popups-to-escape-sandbox allow-modals"></iframe></div>
{{- else if eq .Kind "pdf"}}
<div class="frame"><iframe src="?raw=1" title="{{.Name}}"></iframe></div>
{{- else if eq .Kind "image"}}
<div class="media"><img src="?raw=1" alt="{{.Name}}"></div>
{{- else if eq .Kind "video"}}
<div class="media"><video controls preload="metadata" src="?raw=1"></video></div>
{{- else if eq .Kind "audio"}}
<div class="media"><audio controls preload="metadata" src="?raw=1"></audio></div>
{{- else if eq .Kind "text"}}
{{- if .Truncated}}
<p class="note">Showing the first {{.Shown}} of {{.Size}}. <a href="?raw=1">View the whole file as Raw</a>.</p>
{{- end}}
<pre class="lines" style="--gw: {{.Digits}}">
{{- range .Lines}}<span>{{.}}</span>
{{end -}}
</pre>
{{- else}}
<div class="empty">
  <p>No preview for this file type ({{.MediaType}}).</p>
  <a class="button" href="?download=1" download>Download {{.Size}}</a>
</div>
{{- end}}
</main>
</body>
</html>
`))

var indexTemplate = template.Must(template.New("index").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="robots" content="noindex">
<title>ycc-share</title>
<style>` + pageStyle + `</style>
</head>
<body>
<main><div class="empty"><p>Files shared here are reachable only through their links.</p></div></main>
</body>
</html>
`))
