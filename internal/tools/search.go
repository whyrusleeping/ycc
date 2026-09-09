package tools

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/whyrusleeping/gollama"
)

const (
	defaultSearchLimit      = 100
	maxSearchLimit          = 500
	defaultSearchBytes      = 32 * 1024
	maxSearchBytes          = 64 * 1024
	maxSearchContext        = 10
	maxSearchLineBytes      = 4096
	maxSearchJSONEventBytes = 1024 * 1024
	maxSearchErrorBytes     = 16 * 1024
)

var defaultSearchExcludes = []string{
	"!**/.ycc/**",
	"!**/node_modules/**",
	"!**/vendor/**",
	"!**/dist/**",
	"!**/build/**",
}

// search returns bounded, deterministic textual search results from ripgrep.
// It uses argv directly rather than a shell so patterns and globs retain their
// declared literal/regular-expression semantics.
func search(ws *Workspace) *gollama.Tool {
	return &gollama.Tool{
		Name: "Search",
		Description: "Search text with ripgrep using explicit argv (not a shell). pattern is literal by default; set mode=regex for regular expressions. " +
			"Scope with path (default '.') and optional ripgrep globs. output may be matches, files, or count. Match results use stable path:line references and optional nearby context. " +
			"Results are bounded by limit and max_bytes; pass the returned next_offset unchanged to continue. Repository ignore rules and generated/session directory exclusions are enabled by default; overrides are explicit.",
		Params: obj(map[string]any{
			"pattern": strProp("text or regular expression to find"),
			"path":    strProp("file or directory scope, absolute or relative to the workspace root (default '.')"),
			"glob": map[string]any{
				"type":        "array",
				"description": "optional ripgrep include/exclude globs, passed verbatim without shell expansion",
				"items":       map[string]any{"type": "string"},
			},
			"mode":              map[string]any{"type": "string", "enum": []string{"literal", "regex"}, "description": "pattern interpretation (default literal)"},
			"output":            map[string]any{"type": "string", "enum": []string{"matches", "files", "count"}, "description": "result form (default matches)"},
			"context":           map[string]any{"type": "integer", "minimum": 0, "maximum": maxSearchContext, "description": "nearby lines before and after each match; matches output only (default 0)"},
			"limit":             map[string]any{"type": "integer", "minimum": 1, "maximum": maxSearchLimit, "description": "maximum result records in this page (default 100, maximum 500)"},
			"max_bytes":         map[string]any{"type": "integer", "minimum": 1024, "maximum": maxSearchBytes, "description": "maximum bytes of result records, excluding metadata (default 32768, maximum 65536)"},
			"offset":            map[string]any{"type": "integer", "minimum": 0, "description": "result-record offset returned as next_offset by a truncated page (default 0)"},
			"include_ignored":   BoolProp("search files excluded by repository ignore rules (default false)"),
			"include_hidden":    BoolProp("search hidden files and directories (default false)"),
			"include_generated": BoolProp("disable default exclusions for .ycc, node_modules, vendor, dist, and build directories (default false; hidden paths still require include_hidden)"),
		}, "pattern"),
		Call: searchCall(ws),
	}
}

type rgJSONText struct {
	Text  string `json:"text"`
	Bytes string `json:"bytes"`
}

type rgJSONEvent struct {
	Type string `json:"type"`
	Data struct {
		Path       rgJSONText `json:"path"`
		Lines      rgJSONText `json:"lines"`
		LineNumber *int       `json:"line_number"`
		Stats      struct {
			Matches int `json:"matches"`
		} `json:"stats"`
	} `json:"data"`
}

func (t rgJSONText) value() (string, error) {
	if t.Text != "" {
		return t.Text, nil
	}
	if t.Bytes == "" {
		return "", nil
	}
	data, err := base64.StdEncoding.DecodeString(t.Bytes)
	if err != nil {
		return "", fmt.Errorf("decode ripgrep JSON bytes: %w", err)
	}
	return strings.ToValidUTF8(string(data), "�"), nil
}

type searchPage struct {
	offset, limit, maxBytes int
	seen, resultBytes       int
	records                 []string
	truncated               bool
}

type limitedSearchBuffer struct {
	bytes.Buffer
	limit     int
	truncated bool
}

func (b *limitedSearchBuffer) Write(p []byte) (int, error) {
	originalLen := len(p)
	remaining := b.limit - b.Len()
	if remaining <= 0 {
		b.truncated = true
		return originalLen, nil
	}
	if len(p) > remaining {
		p = p[:remaining]
		b.truncated = true
	}
	_, _ = b.Buffer.Write(p)
	return originalLen, nil
}

func (p *searchPage) add(record string) bool {
	if p.seen < p.offset {
		p.seen++
		return false
	}
	if len(p.records) >= p.limit {
		p.truncated = true
		return true
	}
	record = truncateSearchRecord(record, maxSearchLineBytes)
	remaining := p.maxBytes - p.resultBytes
	if len(record)+1 > remaining {
		if len(p.records) > 0 {
			p.truncated = true
			return true
		}
		record = truncateSearchRecord(record, remaining-1)
		p.truncated = true
	}
	p.records = append(p.records, record)
	p.resultBytes += len(record) + 1
	p.seen++
	return p.truncated
}

func truncateSearchRecord(s string, max int) string {
	if max < 1 {
		return ""
	}
	if len(s) <= max {
		return s
	}
	const marker = "… [truncated]"
	if max <= len(marker) {
		return marker[:max]
	}
	cut := max - len(marker)
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + marker
}

func searchCall(ws *Workspace) func(context.Context, any) (*gollama.ToolResult, error) {
	return func(ctx context.Context, params any) (*gollama.ToolResult, error) {
		pattern, ok := getString(params, "pattern")
		if !ok {
			return errResult("Search: pattern must be a non-empty string"), nil
		}
		if err := ctx.Err(); err != nil {
			return errResult("Search: canceled: %v", err), nil
		}
		mode, err := optionalSearchString(params, "mode", "literal")
		if err != nil {
			return errResult("Search: %v", err), nil
		}
		if mode != "literal" && mode != "regex" {
			return errResult("Search: mode must be literal or regex, got %q", mode), nil
		}
		output, err := optionalSearchString(params, "output", "matches")
		if err != nil {
			return errResult("Search: %v", err), nil
		}
		if output != "matches" && output != "files" && output != "count" {
			return errResult("Search: output must be matches, files, or count, got %q", output), nil
		}
		contextLines := getInt(params, "context", 0)
		if contextLines < 0 || contextLines > maxSearchContext {
			return errResult("Search: context must be between 0 and %d", maxSearchContext), nil
		}
		if contextLines != 0 && output != "matches" {
			return errResult("Search: context is only valid with output=matches"), nil
		}
		limit := getInt(params, "limit", defaultSearchLimit)
		if limit < 1 || limit > maxSearchLimit {
			return errResult("Search: limit must be between 1 and %d", maxSearchLimit), nil
		}
		maxBytes := getInt(params, "max_bytes", defaultSearchBytes)
		if maxBytes < 1024 || maxBytes > maxSearchBytes {
			return errResult("Search: max_bytes must be between 1024 and %d", maxSearchBytes), nil
		}
		offset, err := searchOffset(params)
		if err != nil {
			return errResult("Search: %v", err), nil
		}
		scope, err := optionalSearchString(params, "path", ".")
		if err != nil {
			return errResult("Search: %v", err), nil
		}
		resolved, err := ws.resolveRead(scope)
		if err != nil {
			return errResult("Search: invalid path %q: %v", scope, err), nil
		}
		rg, err := exec.LookPath("rg")
		if err != nil {
			return errResult("Search: ripgrep executable 'rg' is unavailable; install ripgrep or use Bash with another explicit search backend"), nil
		}

		args := []string{"--json", "--sort", "path", "--color", "never", "--max-columns", "2000", "--max-columns-preview"}
		if mode == "literal" {
			args = append(args, "--fixed-strings")
		}
		if contextLines > 0 {
			args = append(args, "--context", strconv.Itoa(contextLines))
		}
		if getBool(params, "include_ignored", false) {
			args = append(args, "--no-ignore")
		}
		if getBool(params, "include_hidden", false) {
			args = append(args, "--hidden")
		}
		globs, err := searchGlobs(params)
		if err != nil {
			return errResult("Search: %v", err), nil
		}
		for _, glob := range globs {
			args = append(args, "--glob", glob)
		}
		if !getBool(params, "include_generated", false) {
			for _, glob := range defaultSearchExcludes {
				args = append(args, "--glob", glob)
			}
		}
		args = append(args, "--", pattern, resolved)

		commandCtx, stop := context.WithCancel(ctx)
		defer stop()
		cmd := exec.CommandContext(commandCtx, rg, args...)
		cmd.Dir = ws.Root
		// A host ripgrep config must not silently alter the declared Search
		// semantics. An empty config path disables RIPGREP_CONFIG_PATH even when it
		// is inherited from the daemon or included in Workspace.Env.
		cmd.Env = append(append(os.Environ(), ws.Env...), "RIPGREP_CONFIG_PATH=")
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			return errResult("Search: start ripgrep: %v", err), nil
		}
		stderr := &limitedSearchBuffer{limit: maxSearchErrorBytes}
		cmd.Stderr = stderr
		if err := cmd.Start(); err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return errResult("Search: canceled: %v", ctxErr), nil
			}
			return errResult("Search: start ripgrep: %v", err), nil
		}

		page := &searchPage{offset: offset, limit: limit, maxBytes: maxBytes}
		matched := false
		stopped := false
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 64*1024), maxSearchJSONEventBytes)
		for scanner.Scan() {
			var event rgJSONEvent
			if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
				stop()
				_ = cmd.Wait()
				return errResult("Search: parse ripgrep output: %v", err), nil
			}
			shouldStop, eventMatched, eventErr := addSearchEvent(page, output, ws.Root, event)
			if eventErr != nil {
				stop()
				_ = cmd.Wait()
				return errResult("Search: parse ripgrep output: %v", eventErr), nil
			}
			matched = matched || eventMatched
			if shouldStop {
				stopped = true
				stop()
				break
			}
		}
		scanErr := scanner.Err()
		if scanErr != nil {
			stop()
		}
		waitErr := cmd.Wait()
		if err := ctx.Err(); err != nil {
			return errResult("Search: canceled: %v", err), nil
		}
		if scanErr != nil && !stopped {
			return errResult("Search: ripgrep produced an oversized or invalid JSON event: %v; narrow the path/glob scope or pattern", scanErr), nil
		}
		if waitErr != nil && !stopped {
			var exitErr *exec.ExitError
			if errors.As(waitErr, &exitErr) && exitErr.ExitCode() == 1 {
				// ripgrep uses exit status 1 for a successful search with no matches.
			} else {
				detail := strings.TrimSpace(strings.ToValidUTF8(stderr.String(), "�"))
				if detail == "" {
					detail = waitErr.Error()
				}
				if stderr.truncated {
					detail += "\n[stderr truncated]"
				}
				return errResult("Search: ripgrep failed: %s", detail), nil
			}
		}
		return formatSearchResult(scope, mode, output, matched, page), nil
	}
}

func searchOffset(params any) (int, error) {
	values, ok := params.(map[string]any)
	if !ok {
		return 0, nil
	}
	raw, exists := values["offset"]
	if !exists {
		return 0, nil
	}
	switch value := raw.(type) {
	case int:
		if value < 0 {
			return 0, fmt.Errorf("offset must be nonnegative")
		}
		return value, nil
	case float64:
		if value < 0 {
			return 0, fmt.Errorf("offset must be nonnegative")
		}
		if math.IsNaN(value) || math.IsInf(value, 0) || math.Trunc(value) != value {
			return 0, fmt.Errorf("offset must be an integer")
		}
		if value >= math.Ldexp(1, strconv.IntSize-1) {
			return 0, fmt.Errorf("offset is too large for this host")
		}
		return int(value), nil
	default:
		return 0, fmt.Errorf("offset must be an integer")
	}
}

func optionalSearchString(params any, key, fallback string) (string, error) {
	values, ok := params.(map[string]any)
	if !ok {
		return fallback, nil
	}
	raw, exists := values[key]
	if !exists {
		return fallback, nil
	}
	value, ok := raw.(string)
	if !ok || value == "" {
		return "", fmt.Errorf("%s must be a non-empty string when provided", key)
	}
	return value, nil
}

func searchGlobs(params any) ([]string, error) {
	values, ok := params.(map[string]any)
	if !ok {
		return nil, nil
	}
	raw, exists := values["glob"]
	if !exists {
		return nil, nil
	}
	items, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("glob must be an array of non-empty strings")
	}
	globs := make([]string, len(items))
	for i, item := range items {
		glob, ok := item.(string)
		if !ok || glob == "" {
			return nil, fmt.Errorf("glob[%d] must be a non-empty string", i)
		}
		globs[i] = glob
	}
	return globs, nil
}

func addSearchEvent(page *searchPage, output, root string, event rgJSONEvent) (stop, matched bool, err error) {
	path, err := event.Data.Path.value()
	if err != nil {
		return false, false, err
	}
	path = searchDisplayPath(root, path)
	switch event.Type {
	case "begin":
		if output == "files" {
			return page.add(path), true, nil
		}
	case "match":
		matched = true
		if output == "matches" {
			return addSearchLines(page, path, event.Data.LineNumber, event.Data.Lines, true)
		}
	case "context":
		// A context event is only emitted around a match. Mark the search matched
		// even if the byte budget stops the page before the match event itself.
		matched = true
		if output == "matches" {
			shouldStop, _, lineErr := addSearchLines(page, path, event.Data.LineNumber, event.Data.Lines, false)
			return shouldStop, matched, lineErr
		}
	case "end":
		if event.Data.Stats.Matches > 0 {
			matched = true
		}
		if output == "count" && event.Data.Stats.Matches > 0 {
			return page.add(fmt.Sprintf("%s:%d", path, event.Data.Stats.Matches)), true, nil
		}
	}
	return false, matched, nil
}

func addSearchLines(page *searchPage, path string, first *int, encoded rgJSONText, match bool) (bool, bool, error) {
	text, err := encoded.value()
	if err != nil {
		return false, match, err
	}
	lineNumber := 0
	if first != nil {
		lineNumber = *first
	}
	text = strings.TrimSuffix(text, "\n")
	lines := strings.Split(text, "\n")
	separator := "-"
	if match {
		separator = ":"
	}
	for i, line := range lines {
		line = strings.TrimSuffix(line, "\r")
		if page.add(fmt.Sprintf("%s%s%d%s%s", path, separator, lineNumber+i, separator, line)) {
			return true, match, nil
		}
	}
	return false, match, nil
}

func searchDisplayPath(root, path string) string {
	if rel, err := filepath.Rel(root, path); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return filepath.ToSlash(rel)
	}
	return filepath.ToSlash(path)
}

func formatSearchResult(scope, mode, output string, matched bool, page *searchPage) *gollama.ToolResult {
	status := "ok"
	if !matched {
		status = "no_match"
	} else if len(page.records) == 0 && !page.truncated {
		status = "end"
	}
	var out strings.Builder
	fmt.Fprintf(&out, "status=%s mode=%s output=%s path=%q offset=%d returned=%d truncated=%t", status, mode, output, scope, page.offset, len(page.records), page.truncated)
	if page.truncated {
		fmt.Fprintf(&out, " next_offset=%d", page.offset+len(page.records))
	}
	out.WriteByte('\n')
	for _, record := range page.records {
		out.WriteString(record)
		out.WriteByte('\n')
	}
	return okResult(out.String())
}
