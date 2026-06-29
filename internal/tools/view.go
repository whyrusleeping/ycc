package tools

import "github.com/whyrusleeping/gollama"

// ResultView is an optional, structured rendering of a tool result for rich UI
// display — the LSP-style connector tree (see the TUI's tool cards). A tool
// attaches it via gollama.ToolResult.Structured; the engine serializes it into
// the tool_result event's data under "view", and the TUI renders it as a tree
// when present, falling back to the raw text Content otherwise.
//
// It is display-only: the model always sees the textual Content, never this. A
// tool result carries EITHER a *Control (control tools) OR a *ResultView (display
// tools) in Structured — they are never both, so ControlOf and ViewOf stay
// unambiguous.
type ResultView struct {
	// Summary is the headline shown above the tree (e.g. "2/3 reviewers accept").
	Summary string `json:"summary,omitempty"`
	// Status tints the summary glyph: "ok" (green ✓), "warn" (amber !), "error"
	// (red ✗), or "" (neutral). Defaults to ok.
	Status string `json:"status,omitempty"`
	// Nodes are the top-level rows of the connector tree.
	Nodes []ViewNode `json:"nodes,omitempty"`
}

// ViewNode is one row in a ResultView tree.
type ViewNode struct {
	// Label is the primary text (e.g. a file path or a reviewer name).
	Label string `json:"label"`
	// Detail is dim secondary text shown after the label (e.g. "2 references").
	Detail string `json:"detail,omitempty"`
	// Kind hints at coloring: "path", "ok", "warn", "error", "muted", or "".
	Kind string `json:"kind,omitempty"`
	// Children are nested rows rendered beneath this one with tree connectors.
	Children []ViewNode `json:"children,omitempty"`
}

// ViewOf returns the *ResultView a display tool attached to its result, or nil.
func ViewOf(res *gollama.ToolResult) *ResultView {
	if res == nil {
		return nil
	}
	if v, ok := res.Structured.(*ResultView); ok {
		return v
	}
	return nil
}

// OkResultView builds a successful tool result carrying both the textual content
// the model reads and a structured ResultView the UI renders.
func OkResultView(content string, view *ResultView) *gollama.ToolResult {
	return &gollama.ToolResult{Content: content, Structured: view}
}
