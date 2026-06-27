package tools

import (
	"context"
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
)

// Editing returns the file + shell tools (Read, Write, Edit, Bash) without a
// control/finish tool — for open-ended modes (chat) where the agent yields
// naturally rather than declaring the task complete.
func Editing(ws *Workspace) []*gollama.Tool {
	return []*gollama.Tool{readFile(ws), writeFile(ws), editFile(ws), bash(ws)}
}

// Worker returns the standard worker tool set scoped to ws (spec §8): the editing
// tools plus finish, the control tool that ends the agent loop with a report.
func Worker(ws *Workspace) []*gollama.Tool {
	return append(Editing(ws), Finish())
}

func readFile(ws *Workspace) *gollama.Tool {
	return &gollama.Tool{
		Name: "Read",
		Description: "Read a file from the workspace. Returns the contents with line numbers in cat -n format " +
			"(line number, a tab, then the line). file_path should be an absolute path within the workspace (a " +
			"path relative to the workspace root is also accepted). By default up to 2000 lines are returned; use " +
			"offset (1-based start line) and limit to read a specific window of a large file.",
		Params: obj(map[string]any{
			"file_path": strProp("absolute path to the file (or relative to the workspace root)"),
			"offset":    map[string]any{"type": "integer", "description": "1-based line number to start reading from (optional)"},
			"limit":     map[string]any{"type": "integer", "description": "maximum number of lines to read (optional)"},
		}, "file_path"),
		Call: func(ctx context.Context, params any) (*gollama.ToolResult, error) {
			fp, ok := getString(params, "file_path")
			if !ok {
				return errResult("Read: missing 'file_path'"), nil
			}
			abs, err := ws.resolve(fp)
			if err != nil {
				return errResult("Read: %v", err), nil
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
		Description: "Perform an exact string replacement in a file. By default old_string must be unique in the " +
			"file (include enough surrounding context to make it unique); set replace_all to replace every " +
			"occurrence. Fails if old_string is not found, or is not unique and replace_all is false.",
		Params: obj(map[string]any{
			"file_path":   strProp("absolute path to the file (or relative to the workspace root)"),
			"old_string":  strProp("the exact text to replace"),
			"new_string":  strProp("the text to replace it with (must differ from old_string)"),
			"replace_all": map[string]any{"type": "boolean", "description": "replace all occurrences (default false)"},
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
			replaceAll := getBool(params, "replace_all", false)
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
			case count > 1 && !replaceAll:
				return errResult("Edit: old_string is not unique in %s (%d matches); add more surrounding context or set replace_all", fp, count), nil
			}
			reps := 1
			if replaceAll {
				reps = -1
			}
			updated := strings.Replace(string(data), oldStr, newStr, reps)
			if err := os.WriteFile(abs, []byte(updated), 0o644); err != nil {
				return errResult("Edit: %v", err), nil
			}
			if ws.OnWrite != nil {
				ws.OnWrite(abs)
			}
			return okResult(fmt.Sprintf("edited %s (%d replacement(s))", fp, count)), nil
		},
	}
}

func bash(ws *Workspace) *gollama.Tool {
	return &gollama.Tool{
		Name: "Bash",
		Description: "Run a shell command and return its combined stdout+stderr (truncated if large). Each call runs " +
			"in a fresh shell already rooted at the workspace, and shell state (including the working directory) does " +
			"NOT persist between calls — so there is never a need to `cd`; just run the command. Use this to explore " +
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
		Name:        "finish",
		Description: "Call when the task is complete. Provide a concise report of what was done. This ends the session.",
		Params:      obj(map[string]any{"report": strProp("summary of the work performed and its outcome")}, "report"),
		Call: func(ctx context.Context, params any) (*gollama.ToolResult, error) {
			report, _ := getString(params, "report")
			return &gollama.ToolResult{Content: "session finished", Structured: &Control{Stop: true, Report: report}}, nil
		},
	}
}
