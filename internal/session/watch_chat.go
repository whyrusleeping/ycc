package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/whyrusleeping/ycc/internal/orchestrator"
)

func watchDefinitionFromSpec(spec orchestrator.WatchSpec) WatchDefinition {
	return WatchDefinition{
		Name: spec.Name, Brief: spec.Brief, Interval: Duration(spec.Interval), Model: spec.Model,
		Probe: spec.Probe, ProbeAlways: spec.ProbeAlways, Expires: spec.Expires, AllowedActions: spec.AllowedActions,
	}
}

// Preflight does not reserve the name: the actual create/update checks again
// after confirmation, since another session may change the watch meanwhile.
func (m *Manager) validateChatWatch(absWS string, spec orchestrator.WatchSpec, creating bool) error {
	def := watchDefinitionFromSpec(spec)
	if err := m.validateWatchDefinition(&def, creating); err != nil {
		return err
	}
	primary := m.primaryTreeFor(filepath.Clean(absWS))
	m.watchMu.Lock()
	defer m.watchMu.Unlock()
	_, err := loadWatch(primary, def.Name)
	if creating {
		if err == nil {
			return fmt.Errorf("%w %q; inspect it with get_watch and use update_watch to correct it instead of creating a companion", ErrWatchExists, def.Name)
		}
		if errors.Is(err, ErrUnknownWatch) {
			return nil
		}
	}
	return err
}

func chatWatchName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if !watchNameRE.MatchString(name) {
		return "", fmt.Errorf("%w: invalid watch name", ErrInvalidWatch)
	}
	return name, nil
}

func (m *Manager) configureChatWatches(absWS string, deps *orchestrator.Deps) {
	primary := m.primaryTreeFor(filepath.Clean(absWS))
	deps.ValidateWatch = func(spec orchestrator.WatchSpec, creating bool) error {
		return m.validateChatWatch(primary, spec, creating)
	}
	deps.ScheduleWatch = func(_ context.Context, spec orchestrator.WatchSpec) (string, error) {
		return m.scheduleWatchFromChat(primary, spec)
	}
	deps.ListWatches = func() (string, error) {
		infos, err := m.ListWatches(m.projectLabel(primary))
		if err != nil {
			return "", err
		}
		type summary struct {
			Name         string             `json:"name"`
			Brief        string             `json:"brief"`
			Interval     Duration           `json:"interval"`
			Paused       bool               `json:"paused"`
			PausedReason string             `json:"paused_reason,omitempty"`
			Expires      time.Time          `json:"expires,omitzero"`
			NextDue      time.Time          `json:"next_due"`
			Last         *WatchReportRecord `json:"last,omitempty"`
		}
		out := make([]summary, 0, len(infos))
		for _, info := range infos {
			out = append(out, summary{
				Name: info.Definition.Name, Brief: clipText(firstLine(info.Definition.Brief), 240),
				Interval: info.Definition.Interval, Paused: info.Paused, PausedReason: info.PausedReason,
				Expires: info.Definition.Expires, NextDue: info.NextDue, Last: info.Last,
			})
		}
		data, err := json.MarshalIndent(out, "", "  ")
		return string(data), err
	}
	deps.GetWatch = func(name string) (string, error) {
		name, err := chatWatchName(name)
		if err != nil {
			return "", err
		}
		info, err := m.GetWatch(m.projectLabel(primary), name)
		if err != nil {
			return "", err
		}
		reports, err := m.WatchReports(m.projectLabel(primary), name, watchRecentRuns)
		if err != nil {
			return "", err
		}
		data, err := json.MarshalIndent(map[string]any{
			"definition": info.Definition, "paused": info.Paused, "paused_reason": info.PausedReason,
			"next_due": info.NextDue, "notes": info.Notes, "decisions": info.Decisions,
			"last": info.Last, "running": info.Running, "parked_sessions": info.ParkedSessions,
			"reports": reports,
		}, "", "  ")
		return string(data), err
	}
	deps.UpdateWatch = func(_ context.Context, spec orchestrator.WatchSpec) (string, error) {
		info, err := m.UpdateWatch(m.projectLabel(primary), watchDefinitionFromSpec(spec))
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("Watch %q updated; notes, decisions, and report history preserved. Paused: %t.", info.Definition.Name, info.Paused), nil
	}
	deps.SetWatchPaused = func(name string, paused bool) (string, error) {
		name, err := chatWatchName(name)
		if err != nil {
			return "", err
		}
		info, err := m.SetWatchPaused(m.projectLabel(primary), name, paused)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("Watch %q paused: %t. Existing runs are not stopped.", info.Definition.Name, info.Paused), nil
	}
}
