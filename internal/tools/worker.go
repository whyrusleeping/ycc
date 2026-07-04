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
	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/jobs"
	"github.com/whyrusleeping/ycc/internal/sandbox"
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
// naturally rather than declaring the task complete. When ws.Jobs is set, the
// background-job tools (job_output, wait, kill_job) are included too and Bash
// gains run_in_background (docs/design/async-jobs.md).
func Editing(ws *Workspace) []*gollama.Tool {
	ts := append([]*gollama.Tool{readFile(ws), writeFile(ws), editFile(ws), bash(ws)}, Web()...)
	if ws.Jobs != nil {
		ts = append(ts, JobTools(ws)...)
	}
	return ts
}

// Worker returns the standard worker tool set scoped to ws (spec §8): the editing
// tools plus finish, the control tool that ends the agent loop with a report, and
// report_blocked, the structured escalation control tool for when the agent cannot
// responsibly proceed without a decision that isn't its to make.
func Worker(ws *Workspace) []*gollama.Tool {
	return append(Editing(ws), Finish(), ReportBlocked())
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
	desc := "Run a shell command and return its combined stdout+stderr (truncated if large). Each call runs " +
		"in a fresh shell already rooted at the workspace, and shell state (including the working directory) does " +
		"NOT persist between calls — so there is never a need to `cd` into the workspace root; just run " +
		"the command directly (write `rg 'pattern'`, not `cd <workspace> && rg 'pattern'`). Use this to explore " +
		"and inspect: search with ripgrep (`rg 'pattern'`, `rg --files -g '*.go'`), list with `ls`, and run " +
		"builds/tests. Prefer the Read tool over `cat` for viewing files. Times out after 2 minutes."
	params := map[string]any{"command": strProp("shell command to execute via 'sh -c'")}
	if ws.Jobs != nil {
		// Phase 1: only the coordinator/pm/chat loop drains finished-job
		// notifications at its session Checkpoint, so its background reports are
		// PUSHED automatically. The implementer's loop has no drain hook, so its
		// background jobs are wait-only — the report must be fetched with wait
		// (docs/design/async-jobs.md §3.3). Word the guidance accordingly.
		if bgAutoDelivered(ws) {
			desc += " For a long-running command (a build, full test suite, or a watcher), pass run_in_background: true " +
				"to start it as a background job and get a job_id back immediately instead of blocking (no 2-minute cap). " +
				"Do NOT poll a background job: its report is delivered to you automatically when it finishes, or call " +
				"wait(job_ids) when its result gates your next step. Use job_output to peek at partial output, kill_job to stop it."
			params["run_in_background"] = BoolProp("run the command as a background job and return a job_id immediately (for builds/tests/watchers); do not poll — the report arrives automatically or via wait")
		} else {
			desc += " For a long-running command (a build, full test suite, or a watcher), pass run_in_background: true " +
				"to start it as a background job and get a job_id back immediately instead of blocking (no 2-minute cap). " +
				"Do NOT poll a background job: call wait(job_ids) to retrieve its report when its result gates your next " +
				"step. Use job_output to peek at partial output, kill_job to stop it."
			params["run_in_background"] = BoolProp("run the command as a background job and return a job_id immediately (for builds/tests/watchers); do not poll — retrieve its report with wait(job_ids)")
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
		"builds/tests. Prefer the Read tool over `cat` for viewing files. Times out after 2 minutes."
	if sandbox.Available() != sandbox.None {
		desc += " NOTE: the workspace is mounted READ-ONLY for you — commands that try to write to or delete from " +
			"the workspace will fail. That is expected; you are a reviewer, not an editor."
	}
	return &gollama.Tool{
		Name:        "Bash",
		Description: desc,
		Params:      obj(map[string]any{"command": strProp("shell command to execute via 'sh -c'")}, "command"),
		Call:        bashCall(ws, true),
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
		// Background jobs (docs/design/async-jobs.md): start the command as a job
		// and return its id immediately. Not offered to the sandboxed reviewer Bash.
		if !sandboxed && getBool(params, "run_in_background", false) {
			if ws.Jobs == nil || ws.Emitter == nil {
				return errResult("bash: run_in_background is not available in this session"), nil
			}
			job := startBackgroundBash(ws, cmdStr)
			if bgAutoDelivered(ws) {
				return okResult(fmt.Sprintf("started background job %s: %s\nIt runs in the background — do NOT poll it. "+
					"Its report arrives automatically when it finishes, or call wait([%q]) when you need the result; "+
					"use job_output(%q) to peek at partial output.", job.ID(), cmdStr, job.ID(), job.ID())), nil
			}
			return okResult(fmt.Sprintf("started background job %s: %s\nIt runs in the background — do NOT poll it. "+
				"Call wait([%q]) to retrieve its report when you need the result; "+
				"use job_output(%q) to peek at partial output.", job.ID(), cmdStr, job.ID(), job.ID())), nil
		}
		cctx, cancel := context.WithTimeout(ctx, bashTimeout)
		defer cancel()
		var cmd *exec.Cmd
		if sandboxed {
			cmd, _ = sandbox.Command(cctx, ws.Root, cmdStr)
		} else {
			cmd = exec.CommandContext(cctx, "sh", "-c", cmdStr)
		}
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
	}
}

// bgAutoDelivered reports whether a background job started under ws will have its
// final report PUSHED automatically at a session checkpoint. In phase 1 only the
// coordinator loop (which owns the session Steer/Checkpoint that drains finished
// jobs) gets automatic delivery; the implementer's loop has no drain hook, so its
// background jobs are wait-only (docs/design/async-jobs.md §3.3). Nil emitter ⇒
// treat as not auto-delivered (safe default: tell the caller to wait).
func bgAutoDelivered(ws *Workspace) bool {
	return ws.Emitter != nil && ws.Emitter.Actor() == "coordinator"
}

// startBackgroundBash registers a background job for cmdStr, launches the process
// under the job's context (so kill_job / session end kill the whole process
// tree), streams its combined output into the job buffer, and emits job_started.
// A goroutine waits for exit and, if it is the one that finalized the job (i.e.
// the job was not killed first), emits job_finished exactly once.
func startBackgroundBash(ws *Workspace, cmdStr string) *jobs.Job {
	owner := ws.Emitter.Actor()
	job := ws.Jobs.Start("bash", cmdStr, owner)
	ws.Emitter.EmitAs(owner, event.JobStarted, map[string]any{
		"id": job.ID(), "kind": job.Kind(), "label": cmdStr,
	})

	cmd := exec.CommandContext(job.Context(), "sh", "-c", cmdStr)
	cmd.Dir = ws.Root
	cmd.Stdout = job.Writer()
	cmd.Stderr = job.Writer()
	// Own process group so a kill signals the whole tree (shell + pipeline
	// children), mirroring the foreground bashCall discipline. No 2-minute
	// timeout: background jobs are for long runs and are bounded instead by
	// kill_job or session-end KillAll (which cancels job.Context()).
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 10 * time.Second

	if err := cmd.Start(); err != nil {
		result := "exit: failed to start: " + err.Error()
		if job.Finish(jobs.Failed, result) {
			emitJobFinished(ws.Emitter, owner, job)
		}
		return job
	}
	go func() {
		err := cmd.Wait()
		status := jobs.Done
		exitInfo := "exit 0"
		if err != nil {
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
