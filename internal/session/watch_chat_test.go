package session

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/whyrusleeping/ycc/internal/orchestrator"
)

func TestChatWatchPreflightAndCorrection(t *testing.T) {
	h := newWatchHarness(t)
	deps := &orchestrator.Deps{}
	h.m.configureChatWatches(h.ws, deps)
	spec := orchestrator.WatchSpec{Name: "live", Brief: "check progress", Interval: time.Hour}
	if err := deps.ValidateWatch(spec, true); err != nil {
		t.Fatal(err)
	}
	if _, err := deps.ScheduleWatch(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	if err := deps.ValidateWatch(spec, true); !errors.Is(err, ErrWatchExists) {
		t.Fatalf("duplicate preflight = %v", err)
	}
	if _, err := deps.ScheduleWatch(context.Background(), spec); !errors.Is(err, ErrWatchExists) {
		t.Fatalf("create after another proposal won the name = %v", err)
	}
	if err := deps.ValidateWatch(spec, false); err != nil {
		t.Fatal(err)
	}
	bad := spec
	bad.Interval = time.Minute
	if err := deps.ValidateWatch(bad, true); !errors.Is(err, ErrInvalidWatch) {
		t.Fatalf("invalid interval = %v", err)
	}
	missing := spec
	missing.Name = "missing"
	if err := deps.ValidateWatch(missing, false); !errors.Is(err, ErrUnknownWatch) {
		t.Fatalf("missing update target = %v", err)
	}

	// Correcting a definition retains the watch's continuity and history.
	h.m.tickWatches()
	waitFor(t, func() bool { return h.runs() == 1 })
	h.report(0, orchestrator.WatchReport{Status: "ok", Summary: "initial check", Notes: "baseline 10"})
	// Existing chat hooks follow the workspace when its project is renamed.
	if _, err := h.m.RenameProject(h.m.projectLabel(h.ws), "renamed"); err != nil {
		t.Fatal(err)
	}
	if err := deps.ValidateWatch(spec, false); err != nil {
		t.Fatal(err)
	}
	spec.Probe = "test -f progress"
	spec.Expires = h.now.Add(4 * time.Hour)
	if _, err := deps.UpdateWatch(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	if _, err := deps.SetWatchPaused("live", true); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Definition WatchDefinition     `json:"definition"`
		Paused     bool                `json:"paused"`
		Notes      string              `json:"notes"`
		Reports    []WatchReportRecord `json:"reports"`
	}
	text, err := deps.GetWatch("live")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(text), &got); err != nil {
		t.Fatal(err)
	}
	if got.Definition.Probe != spec.Probe || !got.Definition.Expires.Equal(spec.Expires) || !got.Paused || got.Notes != "baseline 10" || len(got.Reports) != 1 {
		t.Fatalf("corrected watch = %+v", got)
	}
	text, err = deps.ListWatches()
	if err != nil {
		t.Fatal(err)
	}
	var listed []struct {
		Name   string `json:"name"`
		Paused bool   `json:"paused"`
	}
	if err := json.Unmarshal([]byte(text), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].Name != "live" || !listed[0].Paused {
		t.Fatalf("listed watches = %+v", listed)
	}
	if _, err := deps.SetWatchPaused("live", false); err != nil {
		t.Fatal(err)
	}
	if h.record().Paused {
		t.Fatal("watch remained paused")
	}
	if _, err := deps.GetWatch("../live"); !errors.Is(err, ErrInvalidWatch) {
		t.Fatalf("path name accepted for inspection: %v", err)
	}
	if _, err := deps.SetWatchPaused("../live", true); !errors.Is(err, ErrInvalidWatch) {
		t.Fatalf("path name accepted for pause: %v", err)
	}
}

func TestChatWatchPreflightDoesNotOverwriteUnreadableDefinition(t *testing.T) {
	h := newWatchHarness(t)
	deps := &orchestrator.Deps{}
	h.m.configureChatWatches(h.ws, deps)
	h.create(WatchDefinition{})
	path := filepath.Join(watchDir(h.ws, "live"), watchFileName)
	broken := []byte("not json")
	if err := os.WriteFile(path, broken, 0o644); err != nil {
		t.Fatal(err)
	}
	spec := orchestrator.WatchSpec{Name: "live", Brief: "check", Interval: time.Hour}
	if err := deps.ValidateWatch(spec, true); err == nil {
		t.Fatal("unreadable definition passed preflight")
	}
	if _, err := deps.ScheduleWatch(context.Background(), spec); err == nil {
		t.Fatal("unreadable definition was overwritten")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != string(broken) {
		t.Fatalf("definition changed: %q, %v", data, err)
	}
}
