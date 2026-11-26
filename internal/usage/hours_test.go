package usage

import (
	"reflect"
	"testing"
	"time"

	"github.com/whyrusleeping/ycc/internal/event"
)

func at(clock string) time.Time {
	t, _ := time.Parse(time.RFC3339, "2026-11-20T"+clock+"Z")
	return t
}

func timedTurn(clock, actor string, durMS int64, total int) event.Event {
	return event.Event{TS: at(clock), Actor: actor, Type: event.ModelTurn, Data: map[string]any{
		"model_name": "m", "duration_ms": durMS, "usage": event.Usage{Total: total},
	}}
}

func toolResult(clock, name string, durMS int64) event.Event {
	return event.Event{TS: at(clock), Actor: "coordinator", Type: event.ToolResult, Data: map[string]any{
		"name": name, "duration_ms": durMS,
	}}
}

func hourEntriesOf(t *testing.T, session string, evs []event.Event) []HourEntry {
	r := newReducer(session)
	r.add(jsonRoundTrip(t, evs))
	return r.hourEntries()
}

func TestAggregateHoursBusyTime(t *testing.T) {
	a := hourEntriesOf(t, "a", []event.Event{
		// A task preload's synthetic turn ran no model.
		{TS: at("12:00:05"), Actor: "coordinator", Type: event.ModelTurn, Data: map[string]any{"model_name": "m", "synthetic": true}},
		timedTurn("12:00:30", "coordinator", 20_000, 100),
		// A subagent running at the same time adds tokens and a turn, not busy time.
		timedTurn("12:00:30", "implementer", 20_000, 50),
		// Waiting on the user is not activity.
		toolResult("12:50:00", "ask_user", 40*60_000),
		// A tool call across the hour boundary splits its time between both hours.
		toolResult("13:00:20", "Bash", 40_000),
	})
	// A concurrent session adds its own busy time.
	b := hourEntriesOf(t, "b", []event.Event{timedTurn("12:00:30", "coordinator", 20_000, 7)})

	got := AggregateHours(append(a, b...), Options{})
	want := []HourTotal{
		{Hour: at("12:00:00"), Tokens: 157, Turns: 3, BusySeconds: 20 + 20 + 20},
		{Hour: at("13:00:00"), BusySeconds: 20},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("hours = %+v, want %+v", got, want)
	}
}

func TestAggregateHoursTaskFilterForkAndRange(t *testing.T) {
	entries := hourEntriesOf(t, "s", []event.Event{
		timedTurn("09:00:10", "coordinator", 0, 999), // copied from the fork parent
		{Type: event.SessionForked},
		focus("0001"),
		timedTurn("10:00:20", "coordinator", 20_000, 10),
		focus("0002"),
		// Overlaps the previous turn's slots under another focus.
		timedTurn("10:00:20", "coordinator", 20_000, 5),
	})

	all := AggregateHours(entries, Options{})
	if want := []HourTotal{{Hour: at("10:00:00"), Tokens: 15, Turns: 2, BusySeconds: 20}}; !reflect.DeepEqual(all, want) {
		t.Fatalf("all = %+v, want %+v", all, want)
	}
	one := AggregateHours(entries, Options{Task: "0002"})
	if want := []HourTotal{{Hour: at("10:00:00"), Tokens: 5, Turns: 1, BusySeconds: 20}}; !reflect.DeepEqual(one, want) {
		t.Fatalf("task 0002 = %+v, want %+v", one, want)
	}
	if got := AggregateHours(entries, Options{Since: ts("2026-11-21")}); len(got) != 0 {
		t.Fatalf("after range = %+v, want none", got)
	}
}
