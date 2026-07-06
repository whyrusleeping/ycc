package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"connectrpc.com/connect"
	cli "github.com/urfave/cli/v3"

	"github.com/whyrusleeping/ycc/internal/daemon"
	"github.com/whyrusleeping/ycc/internal/docs"
	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
	"github.com/whyrusleeping/ycc/proto/ycc/v1/yccv1connect"
)

// taskCommand implements `ycc task <add|list|show>`: backlog capture and
// browsing from the shell (task 0143). It lets you jot a task from anywhere — a
// git hook, another tool, or just the CLI — without opening the TUI.
//
// Daemon resolution (deliberately NOT a.dial(), which would spin up a one-shot
// in-process daemon just to touch backlog files): use the explicit --addr
// daemon if given; else a persistent local daemon if one is already reachable;
// else operate directly on <workspace>/backlog via docs.Store, no daemon
// required. --project targets a registered project and therefore requires a
// daemon.
func (a *app) taskCommand() *cli.Command {
	projectFlag := func() *cli.StringFlag {
		return &cli.StringFlag{Name: "project", Usage: "registered project `name` (requires a running daemon)"}
	}
	return &cli.Command{
		Name:  "task",
		Usage: "capture and browse backlog tasks from the shell",
		Description: "Add, list, and show backlog tasks without opening the TUI.\n" +
			"Uses a running daemon when reachable (or --addr); otherwise operates directly\n" +
			"on <workspace>/backlog with no daemon. --project targets a registered project\n" +
			"and requires a daemon.",
		Action: func(ctx context.Context, cmd *cli.Command) error {
			if cmd.Args().Present() {
				return fmt.Errorf("unknown task command %q (run `ycc task --help`)", cmd.Args().First())
			}
			return fmt.Errorf("usage: ycc task <add|list|show>")
		},
		Commands: []*cli.Command{
			{
				Name:      "add",
				Usage:     "create a backlog task",
				ArgsUsage: "\"title\"",
				Flags: []cli.Flag{
					projectFlag(),
					&cli.IntFlag{Name: "priority", Aliases: []string{"p"}, Usage: "priority 1..5 (default 3)"},
					&cli.StringFlag{Name: "desc", Aliases: []string{"d"}, Usage: "long description; use - to read from stdin"},
					&cli.StringFlag{Name: "depends", Usage: "comma-separated dependency ids, e.g. 0007,0008"},
					&cli.StringSliceFlag{Name: "spec-ref", Usage: "spec reference (repeatable)"},
				},
				Action: func(ctx context.Context, cmd *cli.Command) error {
					title := strings.TrimSpace(cmd.Args().First())
					if title == "" {
						return fmt.Errorf("usage: ycc task add \"title\" [--priority N] [--desc TEXT|-] [--depends 0007,0008] [--spec-ref R]")
					}
					prio := int(cmd.Int("priority"))
					if prio != 0 && (prio < 1 || prio > 5) {
						return fmt.Errorf("priority %d out of range (1..5)", prio)
					}
					be, err := a.taskBackend(cmd.String("project"))
					if err != nil {
						return err
					}
					return runTaskAdd(ctx, be, os.Stdout, os.Stdin, addParams{
						title:    title,
						desc:     cmd.String("desc"),
						priority: prio,
						depends:  splitCSV(cmd.String("depends")),
						specRefs: cmd.StringSlice("spec-ref"),
					})
				},
			},
			{
				Name:  "list",
				Usage: "list backlog tasks with readiness",
				Flags: []cli.Flag{
					projectFlag(),
					&cli.BoolFlag{Name: "all", Aliases: []string{"a"}, Usage: "include completed (done) tasks"},
				},
				Action: func(ctx context.Context, cmd *cli.Command) error {
					be, err := a.taskBackend(cmd.String("project"))
					if err != nil {
						return err
					}
					return runTaskList(ctx, be, os.Stdout, cmd.Bool("all"))
				},
			},
			{
				Name:      "show",
				Usage:     "print a single task in full",
				ArgsUsage: "<id>",
				Flags:     []cli.Flag{projectFlag()},
				Action: func(ctx context.Context, cmd *cli.Command) error {
					id := strings.TrimSpace(cmd.Args().First())
					if id == "" {
						return fmt.Errorf("usage: ycc task show <id>")
					}
					be, err := a.taskBackend(cmd.String("project"))
					if err != nil {
						return err
					}
					return runTaskShow(ctx, be, os.Stdout, id)
				},
			},
		},
	}
}

// taskBackend resolves how `ycc task` reaches the backlog. It prefers a daemon
// (explicit --addr, else an already-running local one) but falls back to a
// direct docs.Store on the workspace so capture works with no daemon at all.
// --project always needs a daemon (registered projects live there).
func (a *app) taskBackend(project string) (backlogBackend, error) {
	if a.addr != "" {
		return rpcBackend{client: daemon.DialClient(a.addr, a.token), project: project}, nil
	}
	if daemon.Reachable(daemon.LocalAddr, "") {
		return rpcBackend{client: daemon.DialClient(daemon.LocalAddr, ""), project: project}, nil
	}
	if project != "" {
		return nil, fmt.Errorf("--project requires a running daemon, but none is reachable (start one with `ycc daemon` or use --addr)")
	}
	return directBackend{store: docs.NewStore(a.workspace)}, nil
}

// taskRow is a backlog summary row shared by the RPC and direct-store paths so
// one renderer serves both.
type taskRow struct {
	ID        string
	Title     string
	Status    string
	Priority  int
	DependsOn []string
	BlockedBy []string // ids of not-yet-done deps; empty => ready
}

// taskDetailRow is a full task shared by both backends for `ycc task show`.
type taskDetailRow struct {
	taskRow
	SpecRefs []string
	Created  string
	Updated  string
	Body     string
	Path     string
}

// backlogBackend abstracts the daemon RPC and the direct docs.Store so the
// command cores are backend-agnostic and unit-testable.
type backlogBackend interface {
	create(ctx context.Context, title, body string, priority int, depends, specRefs []string) (taskDetailRow, error)
	list(ctx context.Context) ([]taskRow, error)
	get(ctx context.Context, id string) (taskDetailRow, error)
}

// directBackend operates on the workspace backlog directly (no daemon).
type directBackend struct{ store *docs.Store }

func (d directBackend) create(_ context.Context, title, body string, priority int, depends, specRefs []string) (taskDetailRow, error) {
	if priority == 0 {
		priority = 3
	}
	t, err := d.store.Create(title, docs.TaskBody(body), priority, depends, specRefs)
	if err != nil {
		return taskDetailRow{}, err
	}
	tasks, err := d.store.List()
	if err != nil {
		return taskDetailRow{}, err
	}
	return detailFromTask(t, docs.BlockingDeps(t, docs.StatusByID(tasks))), nil
}

func (d directBackend) list(_ context.Context) ([]taskRow, error) {
	tasks, err := d.store.List()
	if err != nil {
		return nil, err
	}
	byID := docs.StatusByID(tasks)
	rows := make([]taskRow, 0, len(tasks))
	for _, t := range tasks {
		rows = append(rows, taskRow{
			ID: t.ID, Title: t.Title, Status: string(t.Status), Priority: t.Priority,
			DependsOn: t.DependsOn, BlockedBy: docs.BlockingDeps(t, byID),
		})
	}
	return rows, nil
}

func (d directBackend) get(_ context.Context, id string) (taskDetailRow, error) {
	t, err := d.store.Get(id)
	if err != nil {
		return taskDetailRow{}, err
	}
	tasks, err := d.store.List()
	if err != nil {
		return taskDetailRow{}, err
	}
	return detailFromTask(t, docs.BlockingDeps(t, docs.StatusByID(tasks))), nil
}

func detailFromTask(t *docs.Task, blocking []string) taskDetailRow {
	return taskDetailRow{
		taskRow: taskRow{
			ID: t.ID, Title: t.Title, Status: string(t.Status), Priority: t.Priority,
			DependsOn: t.DependsOn, BlockedBy: blocking,
		},
		SpecRefs: t.SpecRefs, Created: t.Created, Updated: t.Updated, Body: t.Body, Path: t.Path,
	}
}

// rpcBackend talks to a daemon over Connect.
type rpcBackend struct {
	client  yccv1connect.SessionServiceClient
	project string
}

func (r rpcBackend) create(ctx context.Context, title, body string, priority int, depends, specRefs []string) (taskDetailRow, error) {
	resp, err := r.client.CreateTask(ctx, connect.NewRequest(&v1.CreateTaskRequest{
		Project: r.project, Title: title, Body: body, Priority: int32(priority),
		DependsOn: depends, SpecRefs: specRefs,
	}))
	if err != nil {
		return taskDetailRow{}, err
	}
	return detailFromProto(resp.Msg.Task), nil
}

func (r rpcBackend) list(ctx context.Context) ([]taskRow, error) {
	resp, err := r.client.ListBacklog(ctx, connect.NewRequest(&v1.ListBacklogRequest{Project: r.project}))
	if err != nil {
		return nil, err
	}
	rows := make([]taskRow, 0, len(resp.Msg.Tasks))
	for _, t := range resp.Msg.Tasks {
		rows = append(rows, taskRow{
			ID: t.Id, Title: t.Title, Status: t.Status, Priority: int(t.Priority),
			DependsOn: t.DependsOn, BlockedBy: t.BlockedBy,
		})
	}
	return rows, nil
}

func (r rpcBackend) get(ctx context.Context, id string) (taskDetailRow, error) {
	resp, err := r.client.GetTask(ctx, connect.NewRequest(&v1.GetTaskRequest{Project: r.project, Id: id}))
	if err != nil {
		return taskDetailRow{}, err
	}
	return detailFromProto(resp.Msg.Task), nil
}

func detailFromProto(t *v1.TaskDetail) taskDetailRow {
	if t == nil {
		return taskDetailRow{}
	}
	return taskDetailRow{
		taskRow: taskRow{
			ID: t.Id, Title: t.Title, Status: t.Status, Priority: int(t.Priority),
			DependsOn: t.DependsOn, BlockedBy: t.BlockedBy,
		},
		SpecRefs: t.SpecRefs, Created: t.Created, Updated: t.Updated, Body: t.Body, Path: t.Path,
	}
}

// addParams carries the resolved `ycc task add` inputs into the testable core.
type addParams struct {
	title    string
	desc     string // "-" means read the whole description from stdin
	priority int
	depends  []string
	specRefs []string
}

// runTaskAdd is the testable core of `ycc task add`: it resolves the
// description (reading stdin when --desc -), creates the task, and prints its id.
func runTaskAdd(ctx context.Context, be backlogBackend, out io.Writer, in io.Reader, p addParams) error {
	desc := p.desc
	if strings.TrimSpace(desc) == "-" {
		data, err := io.ReadAll(in)
		if err != nil {
			return fmt.Errorf("reading description from stdin: %w", err)
		}
		desc = string(data)
	}
	t, err := be.create(ctx, p.title, desc, p.priority, p.depends, p.specRefs)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "created %s  %s\n", t.ID, t.Title)
	return nil
}

// runTaskList is the testable core of `ycc task list`.
func runTaskList(ctx context.Context, be backlogBackend, out io.Writer, includeDone bool) error {
	rows, err := be.list(ctx)
	if err != nil {
		return err
	}
	renderTaskList(out, rows, includeDone)
	return nil
}

// runTaskShow is the testable core of `ycc task show`.
func runTaskShow(ctx context.Context, be backlogBackend, out io.Writer, id string) error {
	d, err := be.get(ctx, id)
	if err != nil {
		return err
	}
	renderTaskShow(out, d)
	return nil
}

// renderTaskList mirrors the coordinator's list_backlog rendering: one row per
// task with readiness marks for open (todo/blocked) tasks, done hidden unless
// includeDone, and a trailing ready-to-start summary.
func renderTaskList(out io.Writer, rows []taskRow, includeDone bool) {
	var b strings.Builder
	hidden, proposed := 0, 0
	var ready []string
	for _, t := range rows {
		if t.Status == string(docs.StatusDone) && !includeDone {
			hidden++
			continue
		}
		if t.Status == string(docs.StatusProposed) {
			proposed++
		}
		dep := strings.Join(t.DependsOn, ",")
		if dep == "" {
			dep = "-"
		}
		// Readiness only applies to not-yet-started tasks; in_progress/in_review/done are past the gate.
		mark := ""
		if t.Status == string(docs.StatusTodo) || t.Status == string(docs.StatusBlocked) {
			if len(t.BlockedBy) > 0 {
				mark = "  [blocked by " + strings.Join(t.BlockedBy, ",") + "]"
			} else {
				mark = "  [READY]"
				ready = append(ready, t.ID)
			}
		}
		fmt.Fprintf(&b, "%s [%s] p%d  %s  (deps: %s)%s\n", t.ID, t.Status, t.Priority, t.Title, dep, mark)
	}
	if b.Len() == 0 {
		if hidden > 0 {
			fmt.Fprintf(out, "(no open tasks; %d done task(s) hidden — pass --all to show them)\n", hidden)
		} else {
			fmt.Fprintln(out, "(backlog is empty)")
		}
		return
	}
	if len(ready) > 0 {
		fmt.Fprintf(&b, "\nReady to start (all deps done): %s\n", strings.Join(ready, ", "))
	} else {
		fmt.Fprintf(&b, "\n(no tasks are ready to start — open tasks are blocked, in progress, or in review)\n")
	}
	if proposed > 0 {
		fmt.Fprintf(&b, "(%d proposed task(s) — ideas awaiting acceptance; promote to 'todo' when confirmed)\n", proposed)
	}
	if hidden > 0 {
		fmt.Fprintf(&b, "(%d done task(s) hidden — pass --all to show them)\n", hidden)
	}
	fmt.Fprint(out, b.String())
}

// renderTaskShow prints a task's frontmatter fields then its markdown body.
func renderTaskShow(out io.Writer, d taskDetailRow) {
	fmt.Fprintf(out, "id:        %s\n", d.ID)
	fmt.Fprintf(out, "title:     %s\n", d.Title)
	fmt.Fprintf(out, "status:    %s\n", d.Status)
	fmt.Fprintf(out, "priority:  %d\n", d.Priority)
	fmt.Fprintf(out, "depends:   %s\n", orDash(strings.Join(d.DependsOn, ", ")))
	fmt.Fprintf(out, "spec_refs: %s\n", orDash(strings.Join(d.SpecRefs, ", ")))
	if d.Created != "" {
		fmt.Fprintf(out, "created:   %s\n", d.Created)
	}
	if d.Updated != "" {
		fmt.Fprintf(out, "updated:   %s\n", d.Updated)
	}
	if d.Status == string(docs.StatusTodo) || d.Status == string(docs.StatusBlocked) {
		if len(d.BlockedBy) > 0 {
			fmt.Fprintf(out, "readiness: blocked by %s\n", strings.Join(d.BlockedBy, ", "))
		} else {
			fmt.Fprintf(out, "readiness: READY\n")
		}
	}
	if d.Path != "" {
		fmt.Fprintf(out, "path:      %s\n", d.Path)
	}
	body := strings.TrimRight(d.Body, "\n")
	if body != "" {
		fmt.Fprintf(out, "\n%s\n", body)
	}
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}

// splitCSV splits a comma-separated flag value, trimming whitespace and dropping
// empty fields.
func splitCSV(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	var out []string
	for _, part := range strings.Split(s, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}
