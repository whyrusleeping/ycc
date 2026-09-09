package tools

import (
	"bufio"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/whyrusleeping/gollama"
	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/jobs"
	"github.com/whyrusleeping/ycc/internal/sandbox"
	"github.com/whyrusleeping/ycc/internal/workspacelease"
)

const (
	maxReadBytes          = 128 * 1024
	maxReadSourceBytes    = 8 * 1024 * 1024
	maxReadLineBytes      = 64 * 1024
	maxReadLines          = 2000
	maxReadLineChars      = 2000
	maxReadNoticeBytes    = 256
	binarySampleBytes     = 8 * 1024
	maxBashBytes          = 64 * 1024
	defaultBashTimeout    = 2 * time.Minute
	maxBashTimeoutSeconds = 3600
	// maxDirEntries caps how many entries the Read tool lists when given a
	// directory path, mirroring the line-limit approach for files.
	maxDirEntries = 1000
	// maxMediaBytes caps the raw (pre-base64) size of an image/PDF the Read tool
	// will inline as a native content block. Providers reject very large media
	// (Anthropic ~5MB/image, ~32MB/PDF); we use a conservative shared limit and
	// tell the model to fall back to other tools past it.
	maxMediaBytes = 12 * 1024 * 1024
)

// imageMediaTypes maps a lower-case file extension to the image media type the
// Read tool returns it as. These are the formats the major LLM APIs accept
// natively as image content blocks.
var imageMediaTypes = map[string]string{
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".gif":  "image/gif",
	".webp": "image/webp",
}

// Editing returns the file + shell tools (Read, Write, Edit, Bash) without a
// control/finish tool — for open-ended modes (chat) where the agent yields
// naturally rather than declaring the task complete. When ws.Jobs is set, the
// background-job tools (job_output, wait, kill_job) are included too and Bash
// gains run_in_background.
func Editing(ws *Workspace) []*gollama.Tool {
	ts := append([]*gollama.Tool{readFile(ws), writeFile(ws), editFile(ws), bash(ws)}, Web()...)
	if ws.Jobs != nil {
		ts = append(ts, JobTools(ws)...)
	}
	return ts
}

// Worker returns the standard worker tool set scoped to ws: the editing
// tools plus finish, the control tool that ends the agent loop with a report, and
// report_blocked, the structured escalation control tool for when the agent cannot
// responsibly proceed without a decision that isn't its to make.
func Worker(ws *Workspace) []*gollama.Tool {
	return append(Editing(ws), Finish(), ReportBlocked())
}

func readFile(ws *Workspace) *gollama.Tool {
	return &gollama.Tool{
		Name: "Read",
		Description: "Read a file. Text files are returned with line numbers in cat -n format " +
			"(line number, a tab, then the line). file_path may be any absolute path — including files outside " +
			"the workspace such as sibling projects or dependency source (e.g. the Go module cache) — or a path " +
			"relative to the workspace root. Text reads scan at most 8 MiB, retain at most 64 KiB per source line, " +
			"render at most 2000 Unicode code points per line, and return at most 2000 lines or 128 KiB; use " +
			"offset (1-based start line) and limit (maximum 2000) to " +
			"read a specific window. Images (PNG, JPEG, GIF, WebP) and PDFs are returned natively as visual content. " +
			"Passing a directory path lists up to 1000 immediate entries (subdirectories have a trailing '/').",
		Params: obj(map[string]any{
			"file_path": strProp("absolute path to the file (or relative to the workspace root)"),
			"offset":    map[string]any{"type": "integer", "minimum": 1, "description": "1-based line number to start reading from (optional; text files only)"},
			"limit":     map[string]any{"type": "integer", "minimum": 1, "maximum": maxReadLines, "description": "maximum number of lines to read (optional; text files only; maximum 2000)"},
		}, "file_path"),
		Call: func(ctx context.Context, params any) (*gollama.ToolResult, error) {
			fp, ok := getString(params, "file_path")
			if !ok {
				return errResult("Read: missing 'file_path'"), nil
			}
			if err := ctx.Err(); err != nil {
				return errResult("Read: %v", err), nil
			}
			abs, err := ws.resolveRead(fp)
			if err != nil {
				return errResult("Read: %v", err), nil
			}

			// Reject known special files before opening them. This is only an
			// optimization: the descriptor is opened nonblocking and validated
			// again because the path can be replaced between Stat and OpenFile.
			preInfo, err := os.Stat(abs)
			if err != nil {
				return errResult("Read: %v", err), nil
			}
			if !preInfo.Mode().IsRegular() && !preInfo.IsDir() {
				return unsupportedReadFile(fp, preInfo), nil
			}
			f, err := os.OpenFile(abs, os.O_RDONLY|syscall.O_NONBLOCK, 0)
			if err != nil {
				return errResult("Read: %v", err), nil
			}
			defer f.Close()
			info, err := f.Stat()
			if err != nil {
				return errResult("Read: %v", err), nil
			}
			if info.IsDir() {
				return readDir(ctx, f, fp), nil
			}
			if !info.Mode().IsRegular() {
				return unsupportedReadFile(fp, info), nil
			}

			// Images and PDFs are handed to the model as native content blocks
			// rather than as numbered text, which would be meaningless binary.
			if res, handled := readMedia(ctx, f, info, fp); handled {
				return res, nil
			}
			start := getInt(params, "offset", 1)
			limit := getInt(params, "limit", maxReadLines)
			return readTextWindow(ctx, f, info.Size(), fp, start, limit), nil
		},
	}
}

// readMedia classifies the opened file by extension and, if it is an image or PDF,
// reads and base64-encodes it within maxMediaBytes, returning a native content
// block (Images for images, Documents for PDFs). The boolean reports whether the
// file was handled as media; false means the caller should read it as text.
//
// fp is the caller-supplied display path used in the text note. Errors (too big,
// unreadable) are returned as media-handled error results so the model gets a
// clear message rather than a binary text dump.
func readMedia(ctx context.Context, f *os.File, info os.FileInfo, fp string) (*gollama.ToolResult, bool) {
	ext := strings.ToLower(filepath.Ext(f.Name()))
	mediaType, isImage := imageMediaTypes[ext]
	isPDF := ext == ".pdf"
	if !isImage && !isPDF {
		return nil, false
	}
	if info.Size() > maxMediaBytes {
		return errResult("Read: %s is %d bytes, too large to inline (limit %d). Use Bash for metadata, or extract/convert it first.",
			fp, info.Size(), maxMediaBytes), true
	}
	data, err := io.ReadAll(io.LimitReader(&contextReader{ctx: ctx, r: f}, maxMediaBytes+1))
	if err != nil {
		return errResult("Read: %v", err), true
	}
	if len(data) > maxMediaBytes {
		return errResult("Read: %s grew beyond the %d-byte inline limit. Use Bash for metadata, or extract/convert it first.",
			fp, maxMediaBytes), true
	}
	if len(data) == 0 {
		return okResult("(file is empty)"), true
	}
	b64 := base64.StdEncoding.EncodeToString(data)
	if isPDF {
		return &gollama.ToolResult{
			Content: fmt.Sprintf("Read PDF %s (%d bytes); its pages are attached as a document.", fp, len(data)),
			Documents: []gollama.Document{{
				Base64:    b64,
				MediaType: "application/pdf",
				Title:     filepath.Base(f.Name()),
			}},
		}, true
	}
	return &gollama.ToolResult{
		Content: fmt.Sprintf("Read image %s (%d bytes, %s); it is attached.", fp, len(data), mediaType),
		Images:  []string{b64},
	}, true
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r *contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := r.r.Read(p)
	if ctxErr := r.ctx.Err(); ctxErr != nil {
		return n, ctxErr
	}
	return n, err
}

type binaryDetector struct {
	pending []byte
	binary  bool
}

func (d *binaryDetector) add(p []byte) {
	if d.binary || len(p) == 0 {
		return
	}
	data := make([]byte, 0, len(d.pending)+len(p))
	data = append(data, d.pending...)
	data = append(data, p...)
	d.pending = d.pending[:0]
	for len(data) > 0 {
		if data[0] == 0 {
			d.binary = true
			return
		}
		if data[0] < utf8.RuneSelf {
			// Tabs, newlines, carriage returns, form feeds, escape codes,
			// and ordinary printable ASCII are all useful in text/log files.
			if data[0] < 0x20 && data[0] != '\t' && data[0] != '\n' &&
				data[0] != '\r' && data[0] != '\f' && data[0] != 0x1b {
				d.binary = true
				return
			}
			data = data[1:]
			continue
		}
		if !utf8.FullRune(data) {
			d.pending = append(d.pending, data...)
			return
		}
		_, size := utf8.DecodeRune(data)
		if size == 1 {
			d.binary = true
			return
		}
		data = data[size:]
	}
}

func (d *binaryDetector) finish() bool {
	return d.binary || len(d.pending) != 0
}

func readTextWindow(ctx context.Context, source io.Reader, size int64, fp string, start, limit int) *gollama.ToolResult {
	if start < 1 {
		start = 1
	}
	if limit < 1 {
		limit = 1
	}
	if limit > maxReadLines {
		limit = maxReadLines
	}
	lr := &io.LimitedReader{R: &contextReader{ctx: ctx, r: source}, N: maxReadSourceBytes}
	reader := bufio.NewReaderSize(lr, binarySampleBytes)
	sample, peekErr := reader.Peek(binarySampleBytes)
	if peekErr != nil && !errors.Is(peekErr, io.EOF) && !errors.Is(peekErr, bufio.ErrBufferFull) {
		return errResult("Read: %v", peekErr)
	}
	sample = append([]byte(nil), sample...) // Peek's buffer is reused while scanning lines.
	var sampleDetector binaryDetector
	sampleDetector.add(sample)
	if sampleDetector.binary {
		return binaryReadResult(fp, size, sample)
	}
	if err := ctx.Err(); err != nil {
		return errResult("Read: %v", err)
	}

	var (
		out           strings.Builder
		line          []byte
		lineNumber    = 1
		shown         int
		lineHasBytes  bool
		lineTruncated bool
		reachedEOF    bool
		sourceLimited bool
		detector      binaryDetector
	)
	for shown < limit {
		fragment, err := reader.ReadSlice('\n')
		detector.add(fragment)
		if detector.binary {
			return binaryReadResult(fp, size, sample)
		}

		payload := fragment
		complete := !errors.Is(err, bufio.ErrBufferFull)
		terminated := complete && len(payload) > 0 && payload[len(payload)-1] == '\n'
		if terminated {
			payload = payload[:len(payload)-1]
		}
		if len(payload) > 0 {
			lineHasBytes = true
		}
		if lineNumber >= start && len(line) < maxReadLineBytes {
			remaining := maxReadLineBytes - len(line)
			if len(payload) > remaining {
				line = append(line, payload[:remaining]...)
				lineTruncated = true
			} else {
				line = append(line, payload...)
			}
		} else if lineNumber >= start && len(payload) > 0 {
			lineTruncated = true
		}

		if errors.Is(err, io.EOF) && lr.N == 0 {
			sourceLimited = true
			lineTruncated = true
		}
		if complete && (lineHasBytes || terminated) {
			if lineNumber >= start {
				if !appendReadLine(&out, lineNumber, line, lineTruncated) {
					break
				}
				shown++
			}
			line = line[:0]
			lineHasBytes = false
			lineTruncated = false
			lineNumber++
		}

		if err != nil && !errors.Is(err, bufio.ErrBufferFull) {
			if !errors.Is(err, io.EOF) {
				return errResult("Read: %v", err)
			}
			reachedEOF = lr.N > 0
			break
		}
	}
	if reachedEOF && detector.finish() {
		return binaryReadResult(fp, size, sample)
	}
	if err := ctx.Err(); err != nil {
		return errResult("Read: %v", err)
	}
	if shown == 0 && reachedEOF {
		if lineNumber == 1 {
			return okResult("(file is empty)")
		}
		return okResult(fmt.Sprintf("(offset %d is past end of file; %d lines total)", start, lineNumber-1))
	}
	if sourceLimited {
		note := fmt.Sprintf("… [source scan stopped at %d bytes; use Bash or a smaller offset/window to inspect this file]\n", maxReadSourceBytes)
		if out.Len()+len(note) <= maxReadBytes {
			out.WriteString(note)
		}
	}
	return okResult(out.String())
}

func appendReadLine(out *strings.Builder, number int, line []byte, truncated bool) bool {
	// A byte retention boundary can split a final UTF-8 rune. The complete source
	// line was validated above, so trim only that incomplete retained suffix.
	for len(line) > 0 && !utf8.Valid(line) {
		line = line[:len(line)-1]
		truncated = true
	}
	text := string(line)
	if utf8.RuneCountInString(text) > maxReadLineChars {
		runes := []rune(text)
		text = string(runes[:maxReadLineChars])
		truncated = true
	}
	if truncated {
		text += "… [line truncated]"
	}
	formatted := fmt.Sprintf("%6d\t%s\n", number, text)
	if out.Len()+len(formatted)+maxReadNoticeBytes > maxReadBytes {
		note := "… [output truncated at 128 KiB; use offset/limit to read a narrower window]\n"
		out.WriteString(note)
		return false
	}
	out.WriteString(formatted)
	return true
}

func binaryReadResult(fp string, size int64, sample []byte) *gollama.ToolResult {
	mediaType := http.DetectContentType(sample)
	if strings.HasPrefix(mediaType, "text/") {
		mediaType = "application/octet-stream"
	}
	return okResult(fmt.Sprintf(
		"Read: %s contains binary data (size %d bytes, detected type %s); text was not returned. Use Bash with file(1) for metadata or xxd -l 256 for a bounded hex preview.",
		fp, size, mediaType))
}

func unsupportedReadFile(fp string, info os.FileInfo) *gollama.ToolResult {
	return errResult(
		"Read: %s is not a regular file or directory (mode %s); refusing to read from a pipe, device, or socket. Use Bash with stat or file(1) to inspect metadata.",
		fp, info.Mode())
}

// readDir lists a bounded number of immediate entries from an already-opened
// directory. Subdirectories are shown with a trailing '/' so the model can
// navigate. Reading maxDirEntries+1 avoids allocating an unbounded entry slice.
func readDir(ctx context.Context, dir *os.File, fp string) *gollama.ToolResult {
	if err := ctx.Err(); err != nil {
		return errResult("Read: %v", err)
	}
	entries, err := dir.ReadDir(maxDirEntries + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return errResult("Read: %v", err)
	}
	if err := ctx.Err(); err != nil {
		return errResult("Read: %v", err)
	}
	truncated := len(entries) > maxDirEntries
	if truncated {
		entries = entries[:maxDirEntries]
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })

	var b strings.Builder
	fmt.Fprintf(&b, "%s/\n", strings.TrimRight(fp, "/"))
	if len(entries) == 0 {
		b.WriteString("(directory is empty)\n")
		return okResult(b.String())
	}
	truncationNote := fmt.Sprintf("… [listing truncated after at most %d entries]\n", maxDirEntries)
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return errResult("Read: %v", err)
		}
		name := e.Name()
		isDir := e.IsDir()
		if !isDir && e.Type()&os.ModeSymlink != 0 {
			// Preserve the directory marker for symlinks without following any
			// target for content. This metadata lookup is bounded by the entry cap.
			if info, err := os.Stat(filepath.Join(dir.Name(), name)); err == nil {
				isDir = info.IsDir()
			}
		}
		if isDir {
			name += "/"
		}
		if b.Len()+len(name)+1+len(truncationNote) > maxReadBytes {
			truncated = true
			break
		}
		fmt.Fprintf(&b, "%s\n", name)
	}
	if err := ctx.Err(); err != nil {
		return errResult("Read: %v", err)
	}
	if truncated {
		b.WriteString(truncationNote)
	}
	return okResult(b.String())
}

func writeFile(ws *Workspace) *gollama.Tool {
	return &gollama.Tool{
		Name: "Write",
		Description: "Write a file to the workspace, creating it or overwriting it entirely. Creates parent " +
			"directories as needed. file_path may be absolute (within the workspace or a configured extra " +
			"writable root) or relative to the workspace root.",
		Params: obj(map[string]any{
			"file_path": strProp("absolute path to the file (or relative to the workspace root)"),
			"content":   strProp("the full content to write to the file"),
		}, "file_path", "content"),
		Call: func(ctx context.Context, params any) (*gollama.ToolResult, error) {
			fp, ok := getString(params, "file_path")
			if !ok {
				return errResult("Write: missing 'file_path'"), nil
			}
			content, _ := getString(params, "content") // empty content is valid
			abs, err := ws.resolve(fp)
			if err != nil {
				return errResult("Write: %v", err), nil
			}
			lease, err := ws.acquirePathMutation(abs)
			if err != nil {
				return errResult("Write: %v", err), nil
			}
			defer lease.Release()
			if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
				return errResult("Write: %v", err), nil
			}
			if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
				return errResult("Write: %v", err), nil
			}
			if ws.OnWrite != nil {
				ws.OnWrite(abs)
			}
			return okResult(fmt.Sprintf("wrote %d bytes to %s", len(content), fp)), nil
		},
	}
}

func editFile(ws *Workspace) *gollama.Tool {
	return &gollama.Tool{
		Name: "Edit",
		Description: "Perform an exact string replacement in a file. old_string must match exactly once in the " +
			"file (include enough surrounding context to make it unique). Fails if old_string is not found, or if " +
			"it matches more than once.",
		Params: obj(map[string]any{
			"file_path":  strProp("absolute path to the file (or relative to the workspace root)"),
			"old_string": strProp("the exact text to replace"),
			"new_string": strProp("the text to replace it with (must differ from old_string)"),
		}, "file_path", "old_string", "new_string"),
		Call: func(ctx context.Context, params any) (*gollama.ToolResult, error) {
			fp, ok := getString(params, "file_path")
			if !ok {
				return errResult("Edit: missing 'file_path'"), nil
			}
			oldStr, ok := getString(params, "old_string")
			if !ok {
				return errResult("Edit: missing 'old_string'"), nil
			}
			newStr, _ := getString(params, "new_string")
			if newStr == oldStr {
				return errResult("Edit: old_string and new_string are identical — nothing would change"), nil
			}
			abs, err := ws.resolve(fp)
			if err != nil {
				return errResult("Edit: %v", err), nil
			}
			lease, err := ws.acquirePathMutation(abs)
			if err != nil {
				return errResult("Edit: %v", err), nil
			}
			defer lease.Release()
			data, err := os.ReadFile(abs)
			if err != nil {
				return errResult("Edit: %v", err), nil
			}
			count := strings.Count(string(data), oldStr)
			switch {
			case count == 0:
				return errResult("Edit: old_string not found in %s. %s", fp, editNotFoundHint(string(data), oldStr)), nil
			case count > 1:
				return errResult("Edit: old_string is not unique in %s (found %d matches); the search text must match exactly once — add more surrounding context to disambiguate", fp, count), nil
			}
			updated := strings.Replace(string(data), oldStr, newStr, 1)
			if err := os.WriteFile(abs, []byte(updated), 0o644); err != nil {
				return errResult("Edit: %v", err), nil
			}
			if ws.OnWrite != nil {
				ws.OnWrite(abs)
			}
			return okResult(fmt.Sprintf("edited %s", fp)), nil
		},
	}
}

func bash(ws *Workspace) *gollama.Tool {
	desc := "Run a shell command and return its combined stdout+stderr (truncated if large). Each call runs " +
		"in a fresh shell already rooted at the workspace, and shell state (including the working directory) does " +
		"NOT persist between calls — so there is never a need to `cd` into the workspace root; just run " +
		"the command directly (write `rg 'pattern'`, not `cd <workspace> && rg 'pattern'`). Use this to explore " +
		"and inspect: search with ripgrep (`rg 'pattern'`, `rg --files -g '*.go'`), list with `ls`, and run " +
		"builds/tests. Prefer the Read tool over `cat` for viewing files. Foreground commands time out after 2 minutes " +
		"by default; set timeout_s to change the runtime limit (maximum 3600 seconds). Background commands have no " +
		"runtime limit by default; for them, timeout_s sets a total job-runtime limit."
	params := map[string]any{
		"command": strProp("shell command to execute via 'sh -c'"),
		"timeout_s": map[string]any{
			"type": "integer", "minimum": 1, "maximum": maxBashTimeoutSeconds,
			"description": "maximum command runtime in seconds (foreground default 120; background commands have no runtime limit when omitted; maximum 3600)",
		},
	}
	if ws.Jobs != nil {
		desc += " Use run_in_background only to overlap the command with meaningful independent work or to leave a " +
			"watcher running. If you need the result before doing anything else, keep it foreground and increase timeout_s " +
			"instead; do not start one background job and immediately wait for it. Do NOT poll a background job. "
		// Only the coordinator/pm/chat loop drains finished-job notifications at its
		// session Checkpoint, so its background reports are pushed automatically.
		// The implementer's loop has no drain hook, so its background jobs are
		// wait-only — the report must be fetched with wait.
		if bgAutoDelivered(ws) {
			desc += "Its report is delivered automatically when it finishes, or call wait(job_ids) after your independent " +
				"work when its result gates the next step. Use job_output to peek at partial output, kill_job to stop it."
			params["run_in_background"] = BoolProp("run the command as a background job and return a job_id immediately; use only to overlap meaningful independent work or leave a watcher running, never to immediately call wait")
		} else {
			desc += "After doing independent work, call wait(job_ids) when the result gates the next step. Use job_output " +
				"to peek at partial output, kill_job to stop it."
			params["run_in_background"] = BoolProp("run the command as a background job and return a job_id immediately; use only to overlap meaningful independent work or leave a watcher running, never to immediately call wait")
		}
	}
	return &gollama.Tool{
		Name:        "Bash",
		Description: desc,
		Params:      obj(params, "command"),
		Call:        bashCall(ws, false),
	}
}

// sandboxedBash is the reviewer's Bash tool: identical to bash() but its command
// runs inside a sandbox (see internal/sandbox) that makes the workspace read-only
// so a reviewer cannot mutate the change under review. Read-only inspection (git
// diff, cat, grep, ls, builds) still works. When no sandbox mechanism is
// available on the host, it degrades to the same unconfined behavior as bash()
// (reviewer non-mutation is then prompt-enforced only).
func sandboxedBash(ws *Workspace) *gollama.Tool {
	desc := "Run a shell command and return its combined stdout+stderr (truncated if large). Each call runs " +
		"in a fresh shell already rooted at the workspace, and shell state (including the working directory) does " +
		"NOT persist between calls — so there is never a need to `cd` into the workspace root; just run " +
		"the command directly (write `rg 'pattern'`, not `cd <workspace> && rg 'pattern'`). Use this to inspect " +
		"the change: run `git diff`, search with ripgrep (`rg 'pattern'`), list with `ls`, read files, and run " +
		"builds/tests. Prefer the Read tool over `cat` for viewing files. Commands time out after 2 minutes by " +
		"default; set timeout_s when a command needs longer (maximum 3600 seconds)."
	if sandbox.Available() != sandbox.None {
		desc += " NOTE: the workspace is mounted READ-ONLY for you — commands that try to write to or delete from " +
			"the workspace will fail. That is expected; you are a reviewer, not an editor."
	}
	return &gollama.Tool{
		Name:        "Bash",
		Description: desc,
		Params: obj(map[string]any{
			"command":   strProp("shell command to execute via 'sh -c'"),
			"timeout_s": map[string]any{"type": "integer", "minimum": 1, "maximum": maxBashTimeoutSeconds, "description": "timeout in seconds (default 120, maximum 3600)"},
		}, "command"),
		Call: bashCall(ws, true),
	}
}

// bashCall builds the Call closure shared by bash() and sandboxedBash(). When
// sandboxed is true the command is wrapped by sandbox.Command so the workspace is
// read-only; otherwise it runs as a plain `sh -c`. The process-group/timeout/
// truncation handling is identical in both cases.
func bashCall(ws *Workspace, sandboxed bool) func(context.Context, any) (*gollama.ToolResult, error) {
	return func(ctx context.Context, params any) (*gollama.ToolResult, error) {
		cmdStr, ok := getString(params, "command")
		if !ok {
			return errResult("bash: missing 'command'"), nil
		}
		// Start a background command as a job and return its id immediately. Not offered to the sandboxed reviewer Bash.
		if !sandboxed && getBool(params, "run_in_background", false) {
			if ws.Jobs == nil || ws.Emitter == nil {
				return errResult("bash: run_in_background is not available in this session"), nil
			}
			var timeout time.Duration
			if hasParam(params, "timeout_s") {
				timeoutSeconds := getInt(params, "timeout_s", 0)
				if timeoutSeconds < 1 || timeoutSeconds > maxBashTimeoutSeconds {
					return errResult("bash: timeout_s must be between 1 and %d seconds", maxBashTimeoutSeconds), nil
				}
				timeout = time.Duration(timeoutSeconds) * time.Second
			}
			lease, err := ws.acquireChildMutation(fmt.Sprintf("%s background Bash %q", ws.MutationToken.Owner(), cmdStr))
			if err != nil {
				return errResult("bash: %v", err), nil
			}
			job := startBackgroundBash(ws, cmdStr, timeout, lease)
			if bgAutoDelivered(ws) {
				return okResult(fmt.Sprintf("started background job %s: %s\nIt runs in the background — do NOT poll it. "+
					"Its report arrives automatically when it finishes, or call wait([%q]) when you need the result; "+
					"use job_output(%q) to peek at partial output.", job.ID(), cmdStr, job.ID(), job.ID())), nil
			}
			return okResult(fmt.Sprintf("started background job %s: %s\nIt runs in the background — do NOT poll it. "+
				"Call wait([%q]) to retrieve its report when you need the result; "+
				"use job_output(%q) to peek at partial output.", job.ID(), cmdStr, job.ID(), job.ID())), nil
		}
		timeout := defaultBashTimeout
		if hasParam(params, "timeout_s") {
			timeoutSeconds := getInt(params, "timeout_s", 0)
			if timeoutSeconds < 1 || timeoutSeconds > maxBashTimeoutSeconds {
				return errResult("bash: timeout_s must be between 1 and %d seconds", maxBashTimeoutSeconds), nil
			}
			timeout = time.Duration(timeoutSeconds) * time.Second
		}
		var lease *workspacelease.Lease
		// Reviewer Bash is genuinely read-only only when the OS sandbox is available.
		if !sandboxed || sandbox.Available() == sandbox.None {
			var err error
			lease, err = ws.acquireMutation()
			if err != nil {
				return errResult("bash: %v", err), nil
			}
			defer lease.Release()
		}
		cctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		var cmd *exec.Cmd
		if sandboxed {
			cmd, _ = sandbox.Command(cctx, ws.Root, cmdStr)
		} else {
			cmd = exec.CommandContext(cctx, "sh", "-c", cmdStr)
		}
		cmd.Dir = ws.Root
		if len(ws.Env) > 0 {
			cmd.Env = append(os.Environ(), ws.Env...)
		}
		// Run the command in its own process group so a timeout kills the whole
		// tree (the shell plus every pipeline child), not just the direct `sh`
		// child — exec's default cancel only signals the leader, leaving
		// grandchildren alive.
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		cmd.Cancel = func() error {
			// Negative pid => signal the entire process group.
			return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		// A grandchild that escapes the kill (e.g. a daemon that calls setsid)
		// can inherit and hold the output pipe open, so CombinedOutput's read
		// never reaches EOF and blocks forever despite the timeout
		// (golang/go#23019). WaitDelay bounds that wait: once the process has
		// exited, Wait force-closes the pipe after this delay and returns.
		cmd.WaitDelay = 10 * time.Second
		out, err := cmd.CombinedOutput()
		if len(out) > maxBashBytes {
			out = append(out[:maxBashBytes], []byte("\n…[truncated]")...)
		}
		result := string(out)
		if cctx.Err() == context.DeadlineExceeded {
			result += fmt.Sprintf("\n[command timed out after %s]", timeout)
		} else if err != nil {
			result += fmt.Sprintf("\n[exit: %v]", err)
		}
		if strings.TrimSpace(result) == "" {
			result = "(no output)"
		}
		return okResult(result), nil
	}
}

// bgAutoDelivered reports whether a background job's final report is pushed at a
// session checkpoint. Only the coordinator loop owns the Steer/Checkpoint that
// drains finished jobs; implementer background jobs are wait-only. A nil emitter
// is not auto-delivered, so callers are told to wait.
func bgAutoDelivered(ws *Workspace) bool {
	return ws.Emitter != nil && ws.Emitter.Actor() == "coordinator"
}

// startBackgroundBash registers a background job for cmdStr, launches the process
// under the job's context (so kill_job / session end kill the whole process
// tree), streams its combined output into the job buffer, and emits job_started.
// A positive timeout bounds the command's total runtime; zero leaves it unbounded
// until kill_job or session end. A goroutine waits for exit and, if it is the one
// that finalized the job (i.e. the job was not killed first), emits job_finished
// exactly once.
func startBackgroundBash(ws *Workspace, cmdStr string, timeout time.Duration, lease *workspacelease.Lease) *jobs.Job {
	owner := ws.Emitter.Actor()
	// Unsandboxed background bash may write to the worktree, so it counts as a
	// mutating job for the single-writer guard: a background
	// implementer is refused while one is live. Conservative — a read-only
	// command is still marked mutating — but safe.
	job := ws.Jobs.StartMutating("bash", cmdStr, owner)
	ws.Emitter.EmitAs(owner, event.JobStarted, map[string]any{
		"id": job.ID(), "kind": job.Kind(), "label": cmdStr,
	})

	cmdCtx := job.Context()
	cancelTimeout := func() {}
	if timeout > 0 {
		cmdCtx, cancelTimeout = context.WithTimeout(cmdCtx, timeout)
	}
	cmd := exec.CommandContext(cmdCtx, "sh", "-c", cmdStr)
	cmd.Dir = ws.Root
	if len(ws.Env) > 0 {
		cmd.Env = append(os.Environ(), ws.Env...)
	}
	cmd.Stdout = job.Writer()
	cmd.Stderr = job.Writer()
	// Own process group so a kill or runtime timeout signals the whole tree
	// (shell + pipeline children), mirroring the foreground bashCall discipline.
	// There is no implicit 2-minute limit for background work: it is bounded only
	// when timeout_s was supplied, or by kill_job/session-end cancellation.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 10 * time.Second

	if err := cmd.Start(); err != nil {
		cancelTimeout()
		lease.Release()
		result := "exit: failed to start: " + err.Error()
		if job.Finish(jobs.Failed, result) {
			emitJobFinished(ws.Emitter, owner, job)
		}
		return job
	}
	go func() {
		defer cancelTimeout()
		defer lease.Release()
		err := cmd.Wait()
		status := jobs.Done
		exitInfo := "exit 0"
		if err != nil && timeout > 0 && cmdCtx.Err() == context.DeadlineExceeded {
			status = jobs.Failed
			exitInfo = fmt.Sprintf("command timed out after %s", timeout)
		} else if err != nil {
			status = jobs.Failed
			if ee, ok := err.(*exec.ExitError); ok {
				exitInfo = fmt.Sprintf("exit %d", ee.ExitCode())
			} else {
				exitInfo = "exit error: " + err.Error()
			}
		}
		tail := job.Tail(20)
		result := exitInfo
		if strings.TrimSpace(tail) != "" {
			result += "\n" + tail
		}
		// Finish returns false if the job was already killed (kill_job / session
		// end), in which case that path owns the job_finished emission.
		if job.Finish(status, result) {
			emitJobFinished(ws.Emitter, owner, job)
		}
	}()
	return job
}

// emitJobFinished records a job_finished event for job tagged with the owner
// actor, carrying its final status and report tail.
func emitJobFinished(em *event.Emitter, owner string, job *jobs.Job) {
	rep := job.Report()
	em.EmitAs(owner, event.JobFinished, map[string]any{
		"id": rep.ID, "kind": rep.Kind, "label": rep.Label,
		"status": string(rep.Status), "tail": rep.Result,
	})
}

// Finish is a control tool: it ends the agent loop and returns the final report.
func Finish() *gollama.Tool {
	return &gollama.Tool{
		Name: "finish",
		Description: "Call when your assigned work is complete. Provide a concise report of what was done " +
			"and how it was verified. This ends your run and returns the report to whoever is waiting on " +
			"you (the user, or the coordinator that spawned you).",
		Params: obj(map[string]any{"report": strProp("summary of the work performed and its outcome")}, "report"),
		Call: func(ctx context.Context, params any) (*gollama.ToolResult, error) {
			report, _ := getString(params, "report")
			return &gollama.ToolResult{Content: "session finished", Structured: &Control{Stop: true, Report: report}}, nil
		},
	}
}

// RequestIntegration is the integrate agent's successful control tool. It ends
// the run and asks the daemon to independently re-verify and advance the base.
func RequestIntegration() *gollama.Tool {
	return &gollama.Tool{
		Name:        "request_integration",
		Description: "Call when the workstream branch is rebased onto the base branch, conflicts are resolved or the verify failure is fixed, the result is committed, and the verify command is green. This ends the run and asks the daemon to independently re-verify and integrate.",
		Params:      obj(map[string]any{"report": strProp("summary of the resolution or fix and verification performed")}, "report"),
		Call: func(ctx context.Context, params any) (*gollama.ToolResult, error) {
			report, _ := getString(params, "report")
			return &gollama.ToolResult{Content: "integration requested", Structured: &Control{Stop: true, Report: report}}, nil
		},
	}
}

// ReportBlocked is a control tool: it ends the agent loop and escalates a
// blocking decision to whoever spawned the agent, distinct from a normal finish.
func ReportBlocked() *gollama.Tool {
	return &gollama.Tool{
		Name: "report_blocked",
		Description: "Call INSTEAD of finish when you cannot responsibly proceed without a decision that is not " +
			"yours to make — an unresolved design choice, conflicting requirements, or a hard-to-reverse call. " +
			"State the specific decision needed and why. This ends your run and escalates to the coordinator, which " +
			"may resolve it and resume you with an answer. Do NOT use this for ordinary implementation judgement " +
			"calls you can reasonably make yourself.",
		Params: obj(map[string]any{"reason": strProp("the specific decision needed and why you cannot proceed without it")}, "reason"),
		Call: func(ctx context.Context, params any) (*gollama.ToolResult, error) {
			reason, ok := getString(params, "reason")
			if !ok {
				return errResult("report_blocked: missing 'reason' — state the specific decision needed and why"), nil
			}
			return &gollama.ToolResult{Content: "blocked; escalating", Structured: &Control{Stop: true, Blocked: true, Report: reason}}, nil
		},
	}
}
