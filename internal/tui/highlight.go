// Syntax highlighting for tool-call result *content* in the TUI, gated on
// confident language inference (task 0017). The inference is intentionally a set
// of pure, testable mappings (path/command → chroma lexer name); rendering is a
// thin layer over chroma that never drops or mangles output — on any ambiguity,
// unknown extension, oversized/binary content, or formatter error it falls back
// to the existing plain/dimmed rendering.
package tui

import (
	"path/filepath"
	"regexp"
	"strings"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/formatters"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
)

// maxHighlightBytes caps the content we attempt to highlight. Larger payloads
// (or binary blobs) are rendered unchanged — highlighting them is both slow and
// pointless, and we never want to risk mangling huge output.
const maxHighlightBytes = 256 * 1024

var (
	chromaFormatter = pickFormatter()
	chromaStyle     = pickStyle()
)

func pickFormatter() chroma.Formatter {
	if f := formatters.Get("terminal256"); f != nil {
		return f
	}
	return formatters.Fallback
}

func pickStyle() *chroma.Style {
	if s := styles.Get("monokai"); s != nil {
		return s
	}
	return styles.Fallback
}

// --- pure language inference (path/command → chroma lexer name) ---

// lexerNameForPath infers a chroma lexer name from a file path's name/extension.
// Returns "" when the path is empty or no lexer matches.
func lexerNameForPath(path string) string {
	if strings.TrimSpace(path) == "" {
		return ""
	}
	lexer := lexers.Match(filepath.Base(path))
	if lexer == nil {
		return ""
	}
	return lexer.Config().Name
}

// grepGlobRe matches a single-extension glob value like "*.go" (optionally
// quoted) so we can extract the extension. It deliberately does not match
// multi-segment or brace globs (e.g. "*.{go,py}") — those are ambiguous.
var grepGlobRe = regexp.MustCompile(`^\*\.([A-Za-z0-9_+-]+)$`)

// grepTypeLexers maps ripgrep/grep --type names to chroma lexer names. Only
// unambiguous, common single-language types are listed; anything not here yields
// no highlight.
var grepTypeLexers = map[string]string{
	"go":         "Go",
	"py":         "Python",
	"python":     "Python",
	"js":         "JavaScript",
	"javascript": "JavaScript",
	"ts":         "TypeScript",
	"typescript": "TypeScript",
	"rust":       "Rust",
	"rs":         "Rust",
	"c":          "C",
	"cpp":        "C++",
	"cxx":        "C++",
	"java":       "Java",
	"rb":         "Ruby",
	"ruby":       "Ruby",
	"sh":         "Bash",
	"bash":       "Bash",
	"json":       "JSON",
	"yaml":       "YAML",
	"yml":        "YAML",
	"html":       "HTML",
	"css":        "CSS",
	"md":         "markdown",
	"markdown":   "markdown",
	"toml":       "TOML",
	"php":        "PHP",
	"swift":      "Swift",
	"kotlin":     "Kotlin",
	"kt":         "Kotlin",
	"scala":      "Scala",
	"sql":        "SQL",
	"proto":      "Protocol Buffer",
}

// typeLexerName resolves a --type/-t value to a chroma lexer name, returning ""
// when the type is unknown or the resolved lexer is unavailable.
func typeLexerName(t string) string {
	name, ok := grepTypeLexers[strings.ToLower(strings.TrimSpace(t))]
	if !ok {
		return ""
	}
	if lexers.Get(name) == nil {
		return ""
	}
	return name
}

// lexerNameForCommand detects an *unambiguous* single-language restriction in an
// rg/grep command via its --type/-t and -g/--glob flags. Any ambiguity (multiple
// distinct types/extensions, or none) yields "". Negated globs (`!…`) are ignored
// for inference; a command with only negations yields "".
func lexerNameForCommand(cmd string) string {
	toks := splitArgs(cmd)
	resolved := map[string]bool{} // distinct lexer names implied by the flags
	sawRestriction := false

	getVal := func(i int) (string, int) {
		// supports "--flag val" and the value already split off by splitArgs
		if i+1 < len(toks) {
			return toks[i+1], i + 1
		}
		return "", i
	}

	for i := 0; i < len(toks); i++ {
		tok := toks[i]
		switch {
		case tok == "-t" || tok == "--type":
			v, ni := getVal(i)
			i = ni
			sawRestriction = true
			if name := typeLexerName(v); name != "" {
				resolved[name] = true
			} else {
				// An unknown/known-but-ambiguous type makes the whole command
				// ambiguous.
				return ""
			}
		case strings.HasPrefix(tok, "--type="):
			v := strings.TrimPrefix(tok, "--type=")
			sawRestriction = true
			if name := typeLexerName(v); name != "" {
				resolved[name] = true
			} else {
				return ""
			}
		case tok == "-g" || tok == "--glob":
			v, ni := getVal(i)
			i = ni
			if name, ok := globLexer(v); ok {
				sawRestriction = true
				if name == "" {
					return "" // a glob restriction we can't resolve → ambiguous
				}
				resolved[name] = true
			}
			// negated/non-extension globs are ignored for inference
		case strings.HasPrefix(tok, "--glob="):
			v := strings.TrimPrefix(tok, "--glob=")
			if name, ok := globLexer(v); ok {
				sawRestriction = true
				if name == "" {
					return ""
				}
				resolved[name] = true
			}
		case strings.HasPrefix(tok, "-g") && len(tok) > 2:
			// -g*.go form
			v := tok[2:]
			if name, ok := globLexer(v); ok {
				sawRestriction = true
				if name == "" {
					return ""
				}
				resolved[name] = true
			}
		}
	}

	if !sawRestriction || len(resolved) != 1 {
		return ""
	}
	for name := range resolved {
		return name
	}
	return ""
}

// globLexer interprets a -g/--glob value. The bool reports whether the value is a
// positive single-extension glob worth considering (negations and broad globs are
// reported as not-considered). When considered, the string is the resolved lexer
// name, or "" if the extension has no known lexer (making the command ambiguous).
func globLexer(v string) (string, bool) {
	v = strings.Trim(v, `"'`)
	if v == "" || strings.HasPrefix(v, "!") {
		return "", false // negation: ignore for inference
	}
	// Only consider a clean single-extension glob like *.go; ignore path globs
	// (e.g. "src/**") and brace alternations (ambiguous).
	mm := grepGlobRe.FindStringSubmatch(v)
	if mm == nil {
		return "", false
	}
	return lexerNameForPath("x." + mm[1]), true
}

// grepPrefixRe matches a leading `path:line:` or `path:line:col:` result prefix.
// The path segment may not contain a colon (which keeps the match unambiguous).
var grepPrefixRe = regexp.MustCompile(`^([^:\n]+):(\d+):(?:(\d+):)?`)

// lexerNameForGrepPaths inspects grep/ripgrep output shaped like `path:line:…`
// and returns a lexer name only when every prefixed line shares one extension
// whose lexer is known. Lines without a prefix are ignored for the decision; if
// there are zero prefixed lines, returns "".
func lexerNameForGrepPaths(result string) string {
	name := ""
	any := false
	for _, ln := range strings.Split(result, "\n") {
		mm := grepPrefixRe.FindStringSubmatch(ln)
		if mm == nil {
			continue
		}
		ext := filepath.Ext(mm[1])
		if ext == "" {
			return "" // a prefixed path with no extension → ambiguous
		}
		ln := lexerNameForPath("x" + ext)
		if ln == "" {
			return ""
		}
		if !any {
			name = ln
			any = true
		} else if ln != name {
			return "" // mixed languages → ambiguous
		}
	}
	if !any {
		return ""
	}
	return name
}

// grepLexer picks the lexer for grep/ripgrep output: a confident command-flag
// inference wins; otherwise fall back to a uniform-extension inference over the
// result's `path:line:` prefixes.
func grepLexer(cmd, result string) string {
	if name := lexerNameForCommand(cmd); name != "" {
		return name
	}
	return lexerNameForGrepPaths(result)
}

// splitArgs is a minimal, quote-aware command tokenizer good enough for reading
// flag/value pairs out of rg/grep command strings. It is not a full shell parser.
func splitArgs(s string) []string {
	var out []string
	var cur strings.Builder
	var quote rune
	flush := func() {
		if cur.Len() > 0 {
			out = append(out, cur.String())
			cur.Reset()
		}
	}
	for _, r := range s {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case r == '\'' || r == '"':
			quote = r
		case r == ' ' || r == '\t':
			flush()
		default:
			cur.WriteRune(r)
		}
	}
	flush()
	return out
}

// --- rendering helpers ---

// highlightCode tokenises and colorizes code with the inferred lexer. It returns
// the input unchanged when there is no lexer, the content is oversized, or any
// step errors — never dropping content.
func highlightCode(code, lexerName string) string {
	if lexerName == "" || len(code) > maxHighlightBytes {
		return code
	}
	lexer := lexers.Get(lexerName)
	if lexer == nil {
		return code
	}
	it, err := lexer.Tokenise(nil, code)
	if err != nil {
		return code
	}
	var b strings.Builder
	if err := chromaFormatter.Format(&b, chromaStyle, it); err != nil {
		return code
	}
	return b.String()
}

// highlightCatN highlights `cat -n` style output, preserving (and dimming) the
// line-number gutter and highlighting only the code after it. With no lexer it
// falls back to the existing dimLineNumbers behavior. On any line-count mismatch
// after highlighting it also falls back — it must never drop or misalign content.
func highlightCatN(s, lexerName string) string {
	if lexerName == "" {
		return dimLineNumbers(s)
	}
	lines := strings.Split(s, "\n")
	gutters := make([]string, len(lines)) // "" => not a cat -n line
	contentIdx := make([]int, 0, len(lines))
	var content []string
	for i, ln := range lines {
		if mm := catnRe.FindStringSubmatch(ln); mm != nil {
			gutters[i] = mm[1]
			contentIdx = append(contentIdx, i)
			content = append(content, mm[2])
		}
	}
	if len(content) == 0 {
		return dimLineNumbers(s)
	}
	hl := strings.Split(highlightCode(strings.Join(content, "\n"), lexerName), "\n")
	// chroma may append a single trailing newline for some lexers; tolerate it.
	if len(hl) == len(content)+1 && strings.TrimSpace(stripANSI(hl[len(hl)-1])) == "" {
		hl = hl[:len(content)]
	}
	if len(hl) != len(content) {
		return dimLineNumbers(s) // safety: never mangle
	}
	out := make([]string, len(lines))
	hi := 0
	for i, ln := range lines {
		if gutters[i] != "" {
			out[i] = dimStyle.Render(gutters[i]) + hl[hi]
			hi++
		} else {
			out[i] = ln
		}
	}
	return strings.Join(out, "\n")
}

// highlightGrep highlights grep/ripgrep output: the `path:line:` prefix stays
// readable (dimmed) and only the match text is colorized. Lines without a prefix
// are passed through unchanged. With no lexer the input is returned unchanged.
func highlightGrep(s, lexerName string) string {
	if lexerName == "" {
		return s
	}
	lines := strings.Split(s, "\n")
	for i, ln := range lines {
		loc := grepPrefixRe.FindStringIndex(ln)
		if loc == nil {
			continue
		}
		prefix := ln[:loc[1]]
		rest := ln[loc[1]:]
		hl := strings.TrimRight(highlightCode(rest, lexerName), "\n")
		lines[i] = dimStyle.Render(prefix) + hl
	}
	return strings.Join(lines, "\n")
}

var ansiRe = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func stripANSI(s string) string { return ansiRe.ReplaceAllString(s, "") }
