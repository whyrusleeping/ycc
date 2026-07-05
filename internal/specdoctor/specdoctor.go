// Package specdoctor implements the DETERMINISTIC pre-pass of the spec-doctor
// flow (task 0100): a reference checker that extracts the concrete file paths,
// package directories, and code symbols a project's design docs mention and
// verifies they still exist in the repository. It finds stale references
// mechanically — with a zero-false-positive discipline (when a code span is
// ambiguous it is SKIPPED, never flagged) — and its markdown report seeds and
// grounds the LLM comparison pass that follows.
//
// The founding principle is "a drifted spec is a bug" (spec §1): this catches
// the mechanically-detectable half of drift (dead references) so the model can
// spend its attention on the semantic half (behavioral drift + coverage gaps).
package specdoctor

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// DocFile is one design document to scan: its workspace-relative slash path (for
// reporting) and its full markdown content.
type DocFile struct {
	Path    string
	Content string
}

// Ref is a single reference extracted from a doc: the inline code span text, the
// doc + 1-based line it appeared on, and how it was classified.
type Ref struct {
	Text string
	Doc  string
	Line int
	Kind string // "path" or "symbol"
}

// Report is the result of a reference check: every classified reference that was
// resolved (Found) or could not be resolved (Missing). Ambiguous spans are not
// represented — they are skipped during extraction.
type Report struct {
	Docs    int // number of docs scanned
	Found   []Ref
	Missing []Ref
}

// knownExts are file extensions that mark a code span as a filesystem path even
// without a slash (e.g. `spec.md`, `go.mod`).
var knownExts = map[string]bool{
	".go": true, ".md": true, ".toml": true, ".proto": true, ".mod": true,
	".sum": true, ".json": true, ".yaml": true, ".yml": true, ".txt": true,
	".sh": true, ".ts": true, ".js": true, ".py": true, ".rs": true,
}

// codeExts are the extensions decisive enough to flag a SINGLE-segment filename
// as stale when it is missing (e.g. `foo.go`). Broader doc/config extensions
// (.md, .json, .toml, ...) as bare filenames are usually illustrative examples,
// so a missing single-segment one of those is skipped, not flagged.
var codeExts = map[string]bool{".go": true, ".proto": true, ".mod": true}

// excludedDirs are never walked when building the symbol-search corpus.
var excludedDirs = map[string]bool{
	".git": true, "vendor": true, "node_modules": true,
}

// identRe matches a Go-ish identifier, optionally dotted (Type.Method, pkg.Func).
var identRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z_][A-Za-z0-9_]*)*$`)

// lowerSegRe matches a lowercase path segment (real repo dirs/files are
// lowercase; slash-joined CamelCase spans like `A/B/C` are symbol lists, not
// paths, and must not be mistaken for stale directories).
var lowerSegRe = regexp.MustCompile(`^[a-z0-9._-]+$`)

// Check runs the deterministic reference pre-pass: it extracts inline code spans
// from every doc, classifies each conservatively as a path or a symbol (skipping
// anything ambiguous), and resolves it against the repo rooted at root. Paths are
// checked with os.Stat (a file OR a directory both resolve); symbols are resolved
// by a word-boundary search across the workspace source files, excluding VCS
// dirs and the docs set itself so a reference never trivially matches its own
// mention.
func Check(root string, docs []DocFile) *Report {
	rep := &Report{Docs: len(docs)}

	// Build the set of doc paths to exclude from the symbol-search corpus so a
	// symbol mentioned only in the docs is not counted as "resolved" by its own
	// mention. backlog/ is excluded for the same reason (task descriptions echo
	// symbol names).
	excludeDoc := map[string]bool{}
	for _, d := range docs {
		excludeDoc[filepath.ToSlash(d.Path)] = true
	}

	var corpus []string // lazily loaded source-file contents for symbol search
	corpusLoaded := false
	loadCorpus := func() {
		if corpusLoaded {
			return
		}
		corpusLoaded = true
		corpus = loadSearchCorpus(root, excludeDoc)
	}

	// Cache resolution decisions so repeated references (very common) are checked
	// once.
	pathSeen := map[string]bool{} // text -> exists
	pathDone := map[string]bool{}
	symSeen := map[string]bool{} // text -> found
	symDone := map[string]bool{}

	for _, d := range docs {
		for _, r := range extractRefs(d) {
			switch r.Kind {
			case "path":
				ok, done := pathSeen[r.Text], pathDone[r.Text]
				if !done {
					ok = pathExists(root, r.Text)
					pathSeen[r.Text], pathDone[r.Text] = ok, true
				}
				if ok {
					rep.Found = append(rep.Found, r)
				} else if flagMissingPath(root, r.Text) {
					rep.Missing = append(rep.Missing, r)
				}
				// else: does not resolve but is not confidently a stale REPO path
				// (illustrative example, external/runtime path) — skip, never flag.
			case "symbol":
				ok, done := symSeen[r.Text], symDone[r.Text]
				if !done {
					loadCorpus()
					ok = symbolFound(corpus, r.Text)
					symSeen[r.Text], symDone[r.Text] = ok, true
				}
				if ok {
					rep.Found = append(rep.Found, r)
				} else {
					rep.Missing = append(rep.Missing, r)
				}
			}
		}
	}
	return rep
}

// extractRefs pulls the classifiable references out of a single doc: inline code
// spans only (fenced code blocks are skipped — they hold illustrative examples
// that would create false positives). Each span is classified conservatively;
// ambiguous spans are dropped.
func extractRefs(d DocFile) []Ref {
	var refs []Ref
	inFence := false
	var fenceMarker string
	lines := strings.Split(d.Content, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		// Fenced code block toggling (``` or ~~~).
		if marker := fenceMarkerOf(trimmed); marker != "" {
			if !inFence {
				inFence, fenceMarker = true, marker
			} else if strings.HasPrefix(trimmed, fenceMarker) {
				inFence, fenceMarker = false, ""
			}
			continue
		}
		if inFence {
			continue
		}
		for _, span := range inlineCodeSpans(line) {
			text, kind := classify(span)
			if kind == "" {
				continue
			}
			refs = append(refs, Ref{Text: text, Doc: d.Path, Line: i + 1, Kind: kind})
		}
	}
	return refs
}

// fenceMarkerOf returns the fence marker ("```" or "~~~") a trimmed line opens
// or closes with, or "" if it is not a fence line.
func fenceMarkerOf(trimmed string) string {
	switch {
	case strings.HasPrefix(trimmed, "```"):
		return "```"
	case strings.HasPrefix(trimmed, "~~~"):
		return "~~~"
	}
	return ""
}

// inlineCodeSpans returns the text inside each inline code span on a line. It
// follows CommonMark's rule that a run of N backticks opens a span that closes at
// the next run of exactly N backticks.
func inlineCodeSpans(line string) []string {
	var spans []string
	runes := []rune(line)
	i := 0
	for i < len(runes) {
		if runes[i] != '`' {
			i++
			continue
		}
		// Count the opening backtick run length.
		n := 0
		for i < len(runes) && runes[i] == '`' {
			n++
			i++
		}
		// Find a closing run of exactly n backticks.
		start := i
		closed := false
		for i < len(runes) {
			if runes[i] == '`' {
				m := 0
				j := i
				for j < len(runes) && runes[j] == '`' {
					m++
					j++
				}
				if m == n {
					spans = append(spans, string(runes[start:i]))
					i = j
					closed = true
					break
				}
				i = j
			} else {
				i++
			}
		}
		if !closed {
			break // unterminated span: stop scanning this line
		}
	}
	return spans
}

// classify inspects an inline code span and returns the reference text and its
// kind ("path", "symbol", or "" to skip). It is deliberately conservative: when
// in doubt, it returns "" so the span is neither resolved nor flagged.
func classify(span string) (string, string) {
	s := strings.TrimSpace(span)
	if s == "" {
		return "", ""
	}
	// Drop an anchor fragment (path#Section) before classifying.
	if idx := strings.IndexByte(s, '#'); idx >= 0 {
		s = strings.TrimSpace(s[:idx])
	}
	if s == "" {
		return "", ""
	}
	// Glob patterns are not references.
	if strings.ContainsAny(s, "*?[") {
		return "", ""
	}
	// Multi-word spans (commands, prose) are not single references.
	if strings.ContainsAny(s, " \t") {
		return "", ""
	}
	// Quotes mark an illustrative/quoted fragment, not a clean reference.
	if strings.ContainsAny(s, "\"'`") {
		return "", ""
	}
	if isPathLike(s) {
		return strings.TrimSuffix(s, "/"), "path"
	}
	if isSymbolLike(s) {
		return s, "symbol"
	}
	return "", ""
}

// isPathLike reports whether s should be treated as a filesystem-path reference.
// It excludes absolute (`/…`) and home (`~/…`) paths (external/runtime, never
// repo-relative) and extension-only tokens (`.proto`, a file-type mention). A
// known file extension is then decisive; otherwise a slash-separated span counts
// as a path only when every segment is a lowercase path token (so `A/B/C` symbol
// lists are excluded — they are handled, if at all, as symbols).
func isPathLike(s string) bool {
	if strings.HasPrefix(s, "/") || strings.HasPrefix(s, "~") {
		return false
	}
	base := strings.TrimSuffix(s, "/")
	// Extension-only token like ".proto" (a file-type mention, not a file).
	name := filepath.Base(base)
	if stem := strings.TrimSuffix(name, filepath.Ext(name)); stem == "" {
		return false
	}
	if knownExts[strings.ToLower(filepath.Ext(base))] {
		return true
	}
	if !strings.Contains(base, "/") {
		return false
	}
	for _, seg := range strings.Split(base, "/") {
		if seg == "" {
			continue
		}
		if !lowerSegRe.MatchString(seg) {
			return false
		}
	}
	return true
}

// isSymbolLike reports whether s should be resolved as a code symbol. It must be
// identifier-shaped AND look distinctly like code (contains '_' or '.', or is
// mixed-case CamelCase) — a bare lowercase or ALL-CAPS word is treated as prose
// and skipped.
func isSymbolLike(s string) bool {
	if !identRe.MatchString(s) {
		return false
	}
	if strings.ContainsAny(s, "_.") {
		return true
	}
	hasUpper, hasLower := false, false
	for _, r := range s {
		switch {
		case r >= 'A' && r <= 'Z':
			hasUpper = true
		case r >= 'a' && r <= 'z':
			hasLower = true
		}
	}
	return hasUpper && hasLower
}

// pathExists reports whether the workspace-relative slash path resolves to a file
// or directory under root.
func pathExists(root, rel string) bool {
	_, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel)))
	return err == nil
}

// flagMissingPath decides whether a path reference that did NOT resolve should be
// reported as stale drift, holding the zero-false-positive line: it is reported
// only when the reference is confidently a REPO path that once existed.
//
//   - backlog/ task files are illustrative (example ids churn constantly) — never
//     flagged.
//   - a single-segment filename is flagged only for a decisive code extension
//     (`foo.go`); a bare `example.md`/`config.toml` is usually illustrative.
//   - a multi-segment path is flagged only when its PARENT directory still exists
//     in the repo — i.e. a file/dir plausibly renamed or removed from a real
//     location. If the parent is absent too, the whole path is likely an example
//     or external reference and is skipped.
func flagMissingPath(root, rel string) bool {
	rel = strings.TrimSuffix(rel, "/")
	if rel == "backlog" || strings.HasPrefix(rel, "backlog/") {
		return false
	}
	// Hidden/dot-directory paths (.ycc/, .github/, ...) are runtime/config
	// surface, and files inside them are frequently optional — not source that
	// "drifts". Never flag them.
	if strings.HasPrefix(rel, ".") {
		return false
	}
	if !strings.Contains(rel, "/") {
		return codeExts[strings.ToLower(filepath.Ext(rel))]
	}
	parent := rel[:strings.LastIndexByte(rel, '/')]
	return dirExists(root, parent)
}

// dirExists reports whether the workspace-relative slash path is a directory.
func dirExists(root, rel string) bool {
	fi, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel)))
	return err == nil && fi.IsDir()
}

// symbolFound reports whether the symbol (its final dotted segment) appears as a
// whole word anywhere in the search corpus.
func symbolFound(corpus []string, sym string) bool {
	name := sym
	if idx := strings.LastIndexByte(sym, '.'); idx >= 0 {
		name = sym[idx+1:]
	}
	if name == "" {
		return false
	}
	re, err := regexp.Compile(`\b` + regexp.QuoteMeta(name) + `\b`)
	if err != nil {
		return false
	}
	for _, c := range corpus {
		if re.MatchString(c) {
			return true
		}
	}
	return false
}

// loadSearchCorpus reads the text of the workspace source files used to resolve
// symbols, excluding VCS/vendor dirs, the docs set, and backlog/ (so a symbol is
// never resolved by its own doc/task mention). Binary and oversized files are
// skipped.
func loadSearchCorpus(root string, excludeDoc map[string]bool) []string {
	const maxFile = 1 << 20 // 1 MiB
	var corpus []string
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if entry.IsDir() {
			if excludedDirs[entry.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if excludeDoc[rel] || rel == "backlog" || strings.HasPrefix(rel, "backlog/") {
			return nil
		}
		info, err := entry.Info()
		if err != nil || info.Size() > maxFile {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil || isBinary(data) {
			return nil
		}
		corpus = append(corpus, string(data))
		return nil
	})
	return corpus
}

// isBinary reports whether data looks like a binary file (a NUL byte in the head).
func isBinary(data []byte) bool {
	head := data
	if len(head) > 8000 {
		head = head[:8000]
	}
	for _, b := range head {
		if b == 0 {
			return true
		}
	}
	return false
}

// Stale reports whether the check found any stale references (docs mentioning a
// path/package or symbol that no longer exists). It is the signal a CI gate keys
// off — `ycc spec-check` exits non-zero exactly when this is true.
func (r *Report) Stale() bool { return len(r.Missing) > 0 }

// Markdown renders the report as a human- and LLM-readable summary: counts, and
// the stale references grouped by doc with line numbers. When nothing is stale it
// states that all references resolve.
func (r *Report) Markdown() string {
	var b strings.Builder
	total := len(r.Found) + len(r.Missing)
	b.WriteString("## Deterministic reference check\n\n")
	b.WriteString("Scanned inline code references (paths, package dirs, symbols) across ")
	b.WriteString(plural(r.Docs, "doc", "docs"))
	b.WriteString("; checked ")
	b.WriteString(plural(total, "reference", "references"))
	b.WriteString(" (ambiguous spans skipped).\n\n")

	if len(r.Missing) == 0 {
		if total == 0 {
			b.WriteString("No checkable references found.\n")
		} else {
			b.WriteString("All ")
			b.WriteString(plural(total, "reference", "references"))
			b.WriteString(" resolve. ✓\n")
		}
		return b.String()
	}

	b.WriteString("### Stale references (")
	b.WriteString(itoa(len(r.Missing)))
	b.WriteString(")\n\n")
	b.WriteString("These docs mention a path/package or symbol that no longer exists in the repo — likely drift:\n\n")

	// Group by doc, stable order, then by line.
	byDoc := map[string][]Ref{}
	var docOrder []string
	for _, ref := range r.Missing {
		if _, ok := byDoc[ref.Doc]; !ok {
			docOrder = append(docOrder, ref.Doc)
		}
		byDoc[ref.Doc] = append(byDoc[ref.Doc], ref)
	}
	sort.Strings(docOrder)
	for _, doc := range docOrder {
		refs := byDoc[doc]
		sort.SliceStable(refs, func(i, j int) bool { return refs[i].Line < refs[j].Line })
		b.WriteString("- **")
		b.WriteString(doc)
		b.WriteString("**\n")
		for _, ref := range refs {
			kind := "symbol not found in code"
			if ref.Kind == "path" {
				kind = "path/dir not found"
			}
			b.WriteString("  - line ")
			b.WriteString(itoa(ref.Line))
			b.WriteString(": `")
			b.WriteString(ref.Text)
			b.WriteString("` — ")
			b.WriteString(kind)
			b.WriteString("\n")
		}
	}
	return b.String()
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return itoa(n) + " " + many
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
