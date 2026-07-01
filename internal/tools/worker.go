package tools

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/whyrusleeping/gollama"
)

const (
	maxReadBytes     = 128 * 1024
	maxBashBytes     = 64 * 1024
	bashTimeout      = 2 * time.Minute
	defaultReadLines = 2000
	maxLineChars     = 2000
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
// naturally rather than declaring the task complete.
func Editing(ws *Workspace) []*gollama.Tool {
	return append([]*gollama.Tool{readFile(ws), writeFile(ws), editFile(ws), bash(ws)}, Web()...)
}

// Worker returns the standard worker tool set scoped to ws (spec §8): the editing
// tools plus finish, the control tool that ends the agent loop with a report.
func Worker(ws *Workspace) []*gollama.Tool {
	return append(Editing(ws), Finish())
}

func readFile(ws *Workspace) *gollama.Tool {
	return &gollama.Tool{
		Name: "Read",
		Description: "Read a file from the workspace. Text files are returned with line numbers in cat -n format " +
			"(line number, a tab, then the line). file_path should be an absolute path within the workspace (a " +
			"path relative to the workspace root is also accepted). Files under trusted read-only roots outside the " +
			"workspace (e.g. the Go module cache) are also readable. By default up to 2000 lines are returned; use " +
			"offset (1-based start line) and limit to read a specific window of a large file. Images (PNG, JPEG, " +
			"GIF, WebP) and PDFs are returned to you natively as visual content — just Read them like any other file. " +
			"Passing a directory path lists its immediate entries (subdirectories are shown with a trailing '/').",
		Params: obj(map[string]any{
			"file_path": strProp("absolute path to the file (or relative to the workspace root)"),
			"offset":    map[string]any{"type": "integer", "description": "1-based line number to start reading from (optional; text files only)"},
			"limit":     map[string]any{"type": "integer", "description": "maximum number of lines to read (optional; text files only)"},
		}, "file_path"),
		Call: func(ctx context.Context, params any) (*gollama.ToolResult, error) {
			fp, ok := getString(params, "file_path")
			if !ok {
				return errResult("Read: missing 'file_path'"), nil
			}
			abs, err := ws.resolveRead(fp)
			if err != nil {
				return errResult("Read: %v", err), nil
			}
			// A directory path lists its immediate entries rather than erroring,
			// mirroring Claude Code's Read tool. os.Stat follows symlinks so a
			// symlink to a directory is listed too.
			if info, err := os.Stat(abs); err == nil && info.IsDir() {
				return readDir(abs, fp), nil
			}
			// Images and PDFs are handed to the model as native content blocks
			// (the same affordance Claude Code's Read tool gives) rather than as
			// cat -n text, which would be meaningless binary.
			if res, handled := readMedia(abs, fp); handled {
				return res, nil
			}
			data, err := os.ReadFile(abs)
			if err != nil {
				return errResult("Read: %v", err), nil
			}
			if len(data) == 0 {
				return okResult("(file is empty)"), nil
			}
			lines := strings.Split(string(data), "\n")
			if n := len(lines); n > 0 && lines[n-1] == "" {
				lines = lines[:n-1] // drop phantom final line from trailing newline
			}
			start := getInt(params, "offset", 1)
			if start < 1 {
				start = 1
			}
			if start-1 >= len(lines) {
				return okResult(fmt.Sprintf("(offset %d is past end of file; %d lines total)", start, len(lines))), nil
			}
			limit := getInt(params, "limit", defaultReadLines)
			var b strings.Builder
			shown := 0
			for i := start - 1; i < len(lines) && shown < limit; i++ {
				line := lines[i]
				if len(line) > maxLineChars {
					line = line[:maxLineChars] + "… [line truncated]"
				}
				fmt.Fprintf(&b, "%6d\t%s\n", i+1, line)
				shown++
				if b.Len() > maxReadBytes {
					b.WriteString("… [output truncated; use offset/limit to read more]\n")
					break
				}
			}
			return okResult(b.String()), nil
		},
	}
}

// readMedia classifies abs by extension and, if it is an image or PDF, reads it,
// base64-encodes it, and returns a ToolResult carrying it as a native content
// block (Images for images, Documents for PDFs). The boolean reports whether the
// file was handled as media; false means the caller should read it as text.
//
// fp is the caller-supplied display path used in the text note. Errors (too big,
// unreadable) are returned as media-handled error results so the model gets a
// clear message rather than a binary text dump.
func readMedia(abs, fp string) (*gollama.ToolResult, bool) {
	ext := strings.ToLower(filepath.Ext(abs))
	mediaType, isImage := imageMediaTypes[ext]
	isPDF := ext == ".pdf"
	if !isImage && !isPDF {
		return nil, false
	}
	info, err := os.Stat(abs)
	if err != nil {
		return errResult("Read: %v", err), true
	}
	if info.IsDir() {
		return nil, false
	}
	if info.Size() > maxMediaBytes {
		return errResult("Read: %s is %d bytes, too large to inline (limit %d). Use Bash for metadata, or extract/convert it first.",
			fp, info.Size(), maxMediaBytes), true
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return errResult("Read: %v", err), true
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
				Title:     filepath.Base(abs),
			}},
		}, true
	}
	return &gollama.ToolResult{
		Content: fmt.Sprintf("Read image %s (%d bytes, %s); it is attached.", fp, len(data), mediaType),
		Images:  []string{b64},
	}, true
}

// readDir lists the immediate entries of the directory at abs. Subdirectories
// are shown with a trailing '/' so the model can navigate. The listing is
// prefixed with the display path (fp) for context and capped at maxDirEntries,
// with a clear indication when more entries exist. Entries from os.ReadDir are
// already sorted by name.
func readDir(abs, fp string) *gollama.ToolResult {
	entries, err := os.ReadDir(abs)
	if err != nil {
		return errResult("Read: %v", err)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s/\n", strings.TrimRight(fp, "/"))
	if len(entries) == 0 {
		b.WriteString("(directory is empty)\n")
		return okResult(b.String())
	}
	shown := len(entries)
	if shown > maxDirEntries {
		shown = maxDirEntries
	}
	for _, e := range entries[:shown] {
		name := e.Name()
		isDir := e.IsDir()
		if !isDir && e.Type()&os.ModeSymlink != 0 {
			// Best-effort: resolve symlinks so links to directories are
			// marked too. Ignore stat errors (dangling link) and list bare.
			if info, err := os.Stat(filepath.Join(abs, name)); err == nil && info.IsDir() {
				isDir = true
			}
		}
		if isDir {
			name += "/"
		}
		fmt.Fprintf(&b, "%s\n", name)
	}
	if len(entries) > shown {
		fmt.Fprintf(&b, "… [%d more entries truncated]\n", len(entries)-shown)
	}
	return okResult(b.String())
}

func writeFile(ws *Workspace) *gollama.Tool {
	return &gollama.Tool{
		Name: "Write",
		Description: "Write a file to the workspace, creating it or overwriting it entirely. Creates parent " +
			"directories as needed. file_path may be absolute (within the workspace) or relative to the root.",
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
			abs, err := ws.resolve(fp)
			if err != nil {
				return errResult("Edit: %v", err), nil
			}
			data, err := os.ReadFile(abs)
			if err != nil {
				return errResult("Edit: %v", err), nil
			}
			count := strings.Count(string(data), oldStr)
			switch {
			case count == 0:
				return errResult("Edit: old_string not found in %s", fp), nil
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
	return &gollama.Tool{
		Name: "Bash",
		Description: "Run a shell command and return its combined stdout+stderr (truncated if large). Each call runs " +
			"in a fresh shell already rooted at the workspace, and shell state (including the working directory) does " +
			"NOT persist between calls — so there is never a need to `cd` into the workspace root; just run " +
			"the command directly (write `rg 'pattern'`, not `cd <workspace> && rg 'pattern'`). Use this to explore " +
			"and inspect: search with ripgrep (`rg 'pattern'`, `rg --files -g '*.go'`), list with `ls`, and run " +
			"builds/tests. Prefer the Read tool over `cat` for viewing files. Times out after 2 minutes.",
		Params: obj(map[string]any{"command": strProp("shell command to execute via 'sh -c'")}, "command"),
		Call: func(ctx context.Context, params any) (*gollama.ToolResult, error) {
			cmdStr, ok := getString(params, "command")
			if !ok {
				return errResult("bash: missing 'command'"), nil
			}
			cctx, cancel := context.WithTimeout(ctx, bashTimeout)
			defer cancel()
			cmd := exec.CommandContext(cctx, "sh", "-c", cmdStr)
			cmd.Dir = ws.Root
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
				result += "\n[command timed out after 2m]"
			} else if err != nil {
				result += fmt.Sprintf("\n[exit: %v]", err)
			}
			if strings.TrimSpace(result) == "" {
				result = "(no output)"
			}
			return okResult(result), nil
		},
	}
}

// Finish is a control tool: it ends the agent loop and returns the final report.
func Finish() *gollama.Tool {
	return &gollama.Tool{
		Name: "finish",
		Description: "Call when your assigned work is complete. Provide a concise report of what was done " +
			"and how it was verified. This ends your run and returns the report to whoever is waiting on " +
			"you (the user, or the coordinator that spawned you).",
		Params:      obj(map[string]any{"report": strProp("summary of the work performed and its outcome")}, "report"),
		Call: func(ctx context.Context, params any) (*gollama.ToolResult, error) {
			report, _ := getString(params, "report")
			return &gollama.ToolResult{Content: "session finished", Structured: &Control{Stop: true, Report: report}}, nil
		},
	}
}
