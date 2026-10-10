package session

import (
	"github.com/whyrusleeping/ycc/internal/config"
	"github.com/whyrusleeping/ycc/internal/event"
)

// A session keeps its tool/prompt shape across config changes and reopen. Legacy
// logs lack the setting; actual implementer calls establish delegated work, while
// otherwise the current setting supplies the first recorded choice on reopen.
func replayWorkImplementation(events []event.Event, fallback string) string {
	delegated := false
	for _, ev := range events {
		if ev.Type == event.SessionStarted || ev.Type == event.SessionReopened {
			if value, _ := ev.Data["work_implementation"].(string); value == config.ImplementationDirect || value == config.ImplementationDelegate {
				return value
			}
		}
		if ev.Type == event.ToolCall && ev.Actor == "coordinator" {
			name, _ := ev.Data["name"].(string)
			delegated = delegated || name == "spawn_implementer" || name == "send_to_implementer"
		}
	}
	if delegated {
		return config.ImplementationDelegate
	}
	return fallback
}
