// This file owns the Bubble Tea messages exchanged by TUI commands and update handlers.
package tui

import (
	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
)

type modesMsg struct {
	modes   []*v1.Mode
	presets []*v1.Preset
}

// menuEntry is a single home-menu row: either a mode (openingPrompt empty) or a
// preset (openingPrompt set). Selecting it starts a session in `mode`; for a
// preset the openingPrompt seeds the session when the user typed nothing.
type menuEntry struct {
	label         string
	description   string
	mode          string
	openingPrompt string
	prominent     bool // surfaced at the top (e.g. onboarding on an un-onboarded workspace)
}

type modelsMsg struct {
	models      []*v1.ModelInfo
	coordinator string
	implementer string
	reviewers   []string
	coordThink  string
	implThink   string
	revThink    string
}

type projectsMsg struct{ projects []*v1.ProjectInfo }

type startedMsg struct{ id, mode string }

// workLoopMsg carries daemon-owned work-loop snapshots from start/stop/get.
// alreadyRunning marks StartWorkLoop's FailedPrecondition fallback so the UI can
// explain that it attached to a loop started by another client.
type workLoopMsg struct {
	info           *v1.WorkLoopInfo
	err            error
	alreadyRunning bool
	openDigest     bool // pure historical read: opens modal without lifecycle changes
	continuePoll   bool
	initiated      bool // start/stop action: applies, advances generation, surfaces completion
	seq            int  // generation captured by a background GetWorkLoop
}

// loopTickMsg polls GetWorkLoop while a loop is running. seq disarms stale timers.
type loopTickMsg struct{ seq int }

type historyMsg struct {
	sessions []*v1.SessionSummary
	err      error
}

// waitingSessionsMsg carries the live sessions that need the user (pending
// question or paused) for the home-menu awareness line (task 0107). It is an
// awareness signal, not a screen: errors are ignored silently so a transient
// RPC hiccup never flashes on the menu.
type waitingSessionsMsg struct {
	sessions []*v1.SessionSummary
	// recent is the most-recent session overall (ListSessionHistory returns
	// most-recent first), used for the "ctrl+l continue last session" affordance
	// (task 0139). nil when there is no session to continue.
	recent *v1.SessionSummary
	err    error
}

// menuRefreshMsg is the modest tick that re-polls waiting sessions while the
// home menu is showing (task 0107), so a question raised in a background
// session surfaces without the user pressing a key. seq disarms stale ticks.
type menuRefreshMsg struct{ seq int }

// menuGitMsg carries the current git branch + dirtiness for the home-menu
// context header (task 0139). An error (non-git workspace, remote daemon, git
// missing) is delivered as an empty branch so the segment simply drops out.
type menuGitMsg struct {
	branch string
	dirty  bool
	err    error
}

// menuSpendMsg carries today's aggregated spend for the home-menu context
// header (task 0139). Errors are ignored silently — the segment drops out.
type menuSpendMsg struct {
	cost   float64
	status string
	err    error
}

// transcriptMsg carries a session's replayed event log for the read-only
// transcript drill-in (spec §18.6), or an error if the fetch failed.
type transcriptMsg struct {
	id     string
	events []*v1.Event
	err    error
}

type evMsg struct{ ev *v1.Event }

type streamClosedMsg struct{}

type errMsg struct{ err error }

// flashClearMsg fires ~5s after a transient error was shown; the handler clears
// flashErr only when seq still matches the current flash so a stale timer never
// wipes a newer error (task 0104).
type flashClearMsg struct{ seq int }

// quitDisarmMsg fires quitGuardWindow after the first ctrl+c armed the quit
// guard; the handler clears quitArmed only when seq still matches so a stale
// timer never disarms a freshly re-armed guard (task 0109).
type quitDisarmMsg struct{ seq int }

type backlogMsg struct{ tasks []*v1.BacklogTaskSummary }

type taskDetailMsg struct{ task *v1.TaskDetail }

// commitDiffMsg carries the result of a GetCommitDiff RPC for the commit-diff
// drill-in overlay (task 0140). sha guards a late reply arriving after the
// overlay was closed or a different commit was opened.
type commitDiffMsg struct {
	sha       string
	diff      string
	truncated bool
	err       error
}

// taskUpdatedMsg carries the result of an UpdateTask grooming RPC (task 0099):
// a refreshed TaskDetail on success, or an error to surface in the browser footer.
type taskUpdatedMsg struct {
	task *v1.TaskDetail
	err  error
}

// editorClosedMsg fires when the external $EDITOR spawned for a task exits (task
// 0099). The browser then reloads the task so hand-edits are reflected.
type editorClosedMsg struct {
	id  string
	err error
}

type plansMsg struct{ plans []*v1.PlanSummary }

type planDetailMsg struct{ plan *v1.GetPlanResponse }

// usageMsg carries the GetUsage breakdown for the cost view (spec §20.5, task 0039).
type usageMsg struct {
	gen       int // request generation; stale task/group responses are ignored
	rows      []*v1.UsageRow
	total     *v1.UsageRow
	workspace string
	accounts  []*v1.SubscriptionUsageAccount
}

// workstreamsMsg carries the ListWorkstreams result for the panel (task 0085),
// or an error to surface in the panel footer.
type workstreamsMsg struct {
	list []*v1.WorkstreamInfo
	err  error
}

// wsSpawnedMsg reports the result of a multi-select "run in parallel" spawn (task
// 0085): count is how many workstreams were created before err (if any).
type wsSpawnedMsg struct {
	count int
	err   error
}

// wsPreviewMsg carries a PreviewMerge result for the merge overlay (task 0085).
type wsPreviewMsg struct {
	id      string
	preview *v1.PreviewMergeResponse
	err     error
}

// wsMergedMsg carries a MergeWorkstream result (task 0085): merged (with commit),
// still-conflicted (paths), or a review-gated needs_accept.
type wsMergedMsg struct {
	id  string
	res *v1.MergeWorkstreamResponse
	err error
}

// wsDiscardedMsg reports the result of a DiscardWorkstream (task 0085).
type wsDiscardedMsg struct {
	id  string
	err error
}

// wsTickMsg is the panel's live-refresh tick (task 0085); seq guards against
// compounding timers across panel visits.
type wsTickMsg struct{ seq int }

// captureEvMsg carries one streamed capture-agent action-log event. A terminal
// event of type "capture_result" carries the outcome of a CaptureBacklogItem RPC
// (task 0016): a created task (task_id/title), a single clarifying question, or
// an error — in its data_json.
type captureEvMsg struct{ ev *v1.Event }

// captureStreamClosedMsg signals the capture stream ended (the goroutine closed
// the channel) without a terminal capture_result event.
type captureStreamClosedMsg struct{}

// captureErrMsg reports a transport/RPC error opening or reading the capture
// stream.
type captureErrMsg struct{ err error }

// mbPrefillMsg carries a model backend's full record loaded via GetModelConfig
// for the edit/duplicate form (task 0044). On error the form is not opened.
type mbPrefillMsg struct {
	cfg  *v1.ModelConfig
	mode int
	err  error
}

// mbWriteMsg is the result of an UpsertModel/RemoveModel RPC (task 0044). On
// success the modal returns to the list and refreshes ListModels; on error the
// message is surfaced inline via mbErr.
type mbWriteMsg struct{ err error }

// mbDiscoverMsg carries the result of a DiscoverModels RPC (spec §13). On success
// the ids populate the connection form's model-id field; note is a human-readable
// status line (e.g. why a curated fallback was used).
type mbDiscoverMsg struct {
	ids     []string
	note    string
	fromNet bool
	err     error
}
