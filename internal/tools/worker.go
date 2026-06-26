package tools

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/whyrusleeping/gollama"
)

const (
	maxReadBytes = 256 * 1024
	maxBashBytes = 64 * 1024
	bashTimeout  = 2 * time.Minute
	maxGrepHits  = 200
)

// Worker returns the standard worker tool set scoped to ws (spec §8). These let
// an agent read and modify the workspace and run commands. The finish tool is a
// control tool that ends the agent loop with a final report.
func Worker(ws *Workspace) []*gollama.Tool {
	return []*gollama.Tool{
		readFile(ws), writeFile(ws), editFile(ws),
		listDir(ws), grep(ws), glob(ws), bash(ws), Finish(),
	}
}

func readFile(ws *Workspace) *gollama.Tool {
	return &gollama.Tool{
		Name:        "read_file",
		Description: "Read a UTF-8 text file from the workspace and return its contents.",
		Params:      obj(map[string]any{"path": strProp("path relative to the workspace root")}, "path"),
		Call: func(ctx context.Context, params any) (*gollama.ToolResult, error) {
			rel, ok := getString(params, "path")
			if !ok {
				return errResult("read_file: missing 'path'"), nil
			}
			abs, err := ws.resolve(rel)
			if err != nil {
				return errResult("read_file: %v", err), nil
			}
			data, err := os.ReadFile(abs)
			if err != nil {
				return errResult("read_file: %v", err), nil
			}
			if len(data) > maxReadBytes {
				return okResult(string(data[:maxReadBytes]) + "\n…[truncated]"), nil
			}
			return okResult(string(data)), nil
		},
	}
}

func writeFile(ws *Workspace) *gollama.Tool {
	return &gollama.Tool{
		Name:        "write_file",
		Description: "Create or overwrite a file in the workspace with the given content. Creates parent directories as needed.",
		Params: obj(map[string]any{
			"path":    strProp("path relative to the workspace root"),
			"content": strProp("full file content to write"),
		}, "path", "content"),
		Call: func(ctx context.Context, params any) (*gollama.ToolResult, error) {
			rel, ok := getString(params, "path")
			if !ok {
				return errResult("write_file: missing 'path'"), nil
			}
			content, _ := getString(params, "content") // empty content is valid
			abs, err := ws.resolve(rel)
			if err != nil {
				return errResult("write_file: %v", err), nil
			}
			if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
				return errResult("write_file: %v", err), nil
			}
			if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
				return errResult("write_file: %v", err), nil
			}
			return okResult(fmt.Sprintf("wrote %d bytes to %s", len(content), rel)), nil
		},
	}
}

func editFile(ws *Workspace) *gollama.Tool {
	return &gollama.Tool{
		Name: "edit_file",
		Description: "Replace an exact, unique occurrence of old_string with new_string in a file. " +
			"Fails if old_string is absent or appears more than once.",
		Params: obj(map[string]any{
			"path":       strProp("path relative to the workspace root"),
			"old_string": strProp("exact text to replace (must be unique in the file)"),
			"new_string": strProp("replacement text"),
		}, "path", "old_string", "new_string"),
		Call: func(ctx context.Context, params any) (*gollama.ToolResult, error) {
			rel, ok := getString(params, "path")
			if !ok {
				return errResult("edit_file: missing 'path'"), nil
			}
			oldStr, ok := getString(params, "old_string")
			if !ok {
				return errResult("edit_file: missing 'old_string'"), nil
			}
			newStr, _ := getString(params, "new_string")
			abs, err := ws.resolve(rel)
			if err != nil {
				return errResult("edit_file: %v", err), nil
			}
			data, err := os.ReadFile(abs)
			if err != nil {
				return errResult("edit_file: %v", err), nil
			}
			switch n := strings.Count(string(data), oldStr); n {
			case 0:
				return errResult("edit_file: old_string not found in %s", rel), nil
			case 1:
				updated := strings.Replace(string(data), oldStr, newStr, 1)
				if err := os.WriteFile(abs, []byte(updated), 0o644); err != nil {
					return errResult("edit_file: %v", err), nil
				}
				return okResult(fmt.Sprintf("edited %s", rel)), nil
			default:
				return errResult("edit_file: old_string is not unique in %s (%d matches); add more context", rel, n), nil
			}
		},
	}
}

func listDir(ws *Workspace) *gollama.Tool {
	return &gollama.Tool{
		Name:        "list_dir",
		Description: "List the entries of a directory in the workspace. Directories are suffixed with '/'.",
		Params:      obj(map[string]any{"path": strProp("directory path relative to the workspace root (default '.')")}),
		Call: func(ctx context.Context, params any) (*gollama.ToolResult, error) {
			rel, _ := getString(params, "path")
			abs, err := ws.resolve(rel)
			if err != nil {
				return errResult("list_dir: %v", err), nil
			}
			entries, err := os.ReadDir(abs)
			if err != nil {
				return errResult("list_dir: %v", err), nil
			}
			names := make([]string, 0, len(entries))
			for _, e := range entries {
				if e.IsDir() {
					names = append(names, e.Name()+"/")
				} else {
					names = append(names, e.Name())
				}
			}
			sort.Strings(names)
			if len(names) == 0 {
				return okResult("(empty)"), nil
			}
			return okResult(strings.Join(names, "\n")), nil
		},
	}
}

func grep(ws *Workspace) *gollama.Tool {
	return &gollama.Tool{
		Name:        "grep",
		Description: "Search workspace files for a regular expression, returning matching path:line:text. Skips .git and hidden dirs.",
		Params: obj(map[string]any{
			"pattern": strProp("RE2 regular expression to search for"),
			"path":    strProp("subdirectory to limit the search to (default '.')"),
		}, "pattern"),
		Call: func(ctx context.Context, params any) (*gollama.ToolResult, error) {
			pat, ok := getString(params, "pattern")
			if !ok {
				return errResult("grep: missing 'pattern'"), nil
			}
			re, err := regexp.Compile(pat)
			if err != nil {
				return errResult("grep: invalid pattern: %v", err), nil
			}
			rel, _ := getString(params, "path")
			root, err := ws.resolve(rel)
			if err != nil {
				return errResult("grep: %v", err), nil
			}
			var hits []string
			walkErr := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
				if err != nil {
					return nil
				}
				if d.IsDir() {
					if d.Name() != "." && strings.HasPrefix(d.Name(), ".") {
						return filepath.SkipDir
					}
					return nil
				}
				f, err := os.Open(p)
				if err != nil {
					return nil
				}
				defer f.Close()
				rp, _ := filepath.Rel(ws.Root, p)
				sc := bufio.NewScanner(f)
				sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
				for ln := 1; sc.Scan(); ln++ {
					if re.MatchString(sc.Text()) {
						hits = append(hits, fmt.Sprintf("%s:%d:%s", rp, ln, strings.TrimSpace(sc.Text())))
						if len(hits) >= maxGrepHits {
							return filepath.SkipAll
						}
					}
				}
				return nil
			})
			if walkErr != nil {
				return errResult("grep: %v", walkErr), nil
			}
			if len(hits) == 0 {
				return okResult("(no matches)"), nil
			}
			return okResult(strings.Join(hits, "\n")), nil
		},
	}
}

func glob(ws *Workspace) *gollama.Tool {
	return &gollama.Tool{
		Name:        "glob",
		Description: "List workspace files matching a shell glob pattern (e.g. 'internal/*/*.go'), relative to the workspace root.",
		Params:      obj(map[string]any{"pattern": strProp("glob pattern relative to the workspace root")}, "pattern"),
		Call: func(ctx context.Context, params any) (*gollama.ToolResult, error) {
			pat, ok := getString(params, "pattern")
			if !ok {
				return errResult("glob: missing 'pattern'"), nil
			}
			matches, err := filepath.Glob(filepath.Join(ws.Root, pat))
			if err != nil {
				return errResult("glob: %v", err), nil
			}
			rels := make([]string, 0, len(matches))
			for _, m := range matches {
				if r, err := filepath.Rel(ws.Root, m); err == nil {
					rels = append(rels, r)
				}
			}
			sort.Strings(rels)
			if len(rels) == 0 {
				return okResult("(no matches)"), nil
			}
			return okResult(strings.Join(rels, "\n")), nil
		},
	}
}

func bash(ws *Workspace) *gollama.Tool {
	return &gollama.Tool{
		Name:        "bash",
		Description: "Run a shell command in the workspace root and return its combined stdout+stderr. Times out after 2 minutes.",
		Params:      obj(map[string]any{"command": strProp("shell command to execute via 'sh -c'")}, "command"),
		Call: func(ctx context.Context, params any) (*gollama.ToolResult, error) {
			cmdStr, ok := getString(params, "command")
			if !ok {
				return errResult("bash: missing 'command'"), nil
			}
			cctx, cancel := context.WithTimeout(ctx, bashTimeout)
			defer cancel()
			cmd := exec.CommandContext(cctx, "sh", "-c", cmdStr)
			cmd.Dir = ws.Root
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
