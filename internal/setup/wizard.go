package setup

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// step is the wizard's current screen.
type step int

const (
	stepProvider step = iota // editing the current provider's fields
	stepAddMore              // add another provider or continue
	stepRoles                // assign coordinator/implementer/reviewers
	stepDone                 // terminal
)

// provider field indices (focus order). backend is a focusable non-text field.
const (
	fieldName = iota
	fieldBackend
	fieldBaseURL
	fieldModel
	fieldKeyEnv
	numFields
)

// model is the Bubble Tea wizard. State is exposed at package scope so tests can
// drive transitions with synthetic tea.KeyMsg and assert outcomes.
type model struct {
	step step

	// in-progress provider editor
	inputs     [numFields]textinput.Model // name, base_url, model, key_env (backend slot unused)
	backendIdx int                        // index into backends
	focus      int                        // current field index
	editErr    string

	// collected providers
	providers []provider

	// stepAddMore menu cursor (0 = add another, 1 = continue)
	addMoreCur int

	// stepRoles selections
	coord     string
	impl      string
	reviewers []string
	coordCur  int // index into provider names
	implCur   int
	revCur    int          // cursor in reviewers list
	revSel    map[int]bool // toggled reviewer indices
	roleFocus int          // 0 coord, 1 impl, 2 reviewers

	skipped   bool
	completed bool
}

// newModel builds a fresh wizard pre-filled with the first backend's defaults.
func newModel() model {
	m := model{step: stepProvider}
	for i := range m.inputs {
		ti := textinput.New()
		ti.CharLimit = 200
		ti.Width = 50
		m.inputs[i] = ti
	}
	m.inputs[fieldName].Placeholder = "logical name (e.g. claude)"
	m.inputs[fieldBaseURL].Placeholder = "base url"
	m.inputs[fieldModel].Placeholder = "model id"
	m.inputs[fieldKeyEnv].Placeholder = "API key env var name"
	m.applyBackendDefaults("")
	m.focus = fieldName
	m.inputs[fieldName].Focus()
	m.revSel = map[int]bool{}
	return m
}

// applyBackendDefaults refreshes base_url/model/key_env to the current backend's
// defaults when the field is empty or still equal to the previous backend's
// default (so user edits aren't clobbered). prevBackend is the backend selected
// before the change ("" on initial fill).
func (m *model) applyBackendDefaults(prevBackend string) {
	b := backends[m.backendIdx]
	refresh := func(i int, def func(string) string) {
		cur := m.inputs[i].Value()
		if cur == "" || (prevBackend != "" && cur == def(prevBackend)) {
			m.inputs[i].SetValue(def(b))
		}
	}
	refresh(fieldBaseURL, defaultBaseURL)
	refresh(fieldModel, defaultModel)
	refresh(fieldKeyEnv, defaultKeyEnv)
}

func (m *model) resetProviderEditor() {
	for i := range m.inputs {
		m.inputs[i].SetValue("")
		m.inputs[i].Blur()
	}
	m.backendIdx = 0
	m.applyBackendDefaults("")
	m.focus = fieldName
	m.inputs[fieldName].Focus()
	m.editErr = ""
}

func (m model) Init() tea.Cmd { return nil }

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	switch key.String() {
	case "ctrl+c", "esc":
		m.skipped = true
		return m, tea.Quit
	}
	switch m.step {
	case stepProvider:
		return m.updateProvider(key)
	case stepAddMore:
		return m.updateAddMore(key)
	case stepRoles:
		return m.updateRoles(key)
	}
	return m, nil
}

func (m model) updateProvider(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "tab", "down":
		m.focusField((m.focus + 1) % numFields)
		return m, nil
	case "shift+tab", "up":
		m.focusField((m.focus - 1 + numFields) % numFields)
		return m, nil
	case "left":
		if m.focus == fieldBackend {
			prev := backends[m.backendIdx]
			m.backendIdx = (m.backendIdx - 1 + len(backends)) % len(backends)
			m.applyBackendDefaults(prev)
		}
		return m, nil
	case "right":
		if m.focus == fieldBackend {
			prev := backends[m.backendIdx]
			m.backendIdx = (m.backendIdx + 1) % len(backends)
			m.applyBackendDefaults(prev)
		}
		return m, nil
	case "enter":
		p := provider{
			name:    strings.TrimSpace(m.inputs[fieldName].Value()),
			backend: backends[m.backendIdx],
			baseURL: strings.TrimSpace(m.inputs[fieldBaseURL].Value()),
			model:   strings.TrimSpace(m.inputs[fieldModel].Value()),
			keyEnv:  strings.TrimSpace(m.inputs[fieldKeyEnv].Value()),
		}
		if p.name == "" || p.backend == "" || p.model == "" {
			m.editErr = "name, backend and model are required"
			return m, nil
		}
		for _, e := range m.providers {
			if e.name == p.name {
				m.editErr = fmt.Sprintf("provider %q already added", p.name)
				return m, nil
			}
		}
		m.providers = append(m.providers, p)
		m.editErr = ""
		m.step = stepAddMore
		m.addMoreCur = 1 // default to "continue"
		return m, nil
	}
	// text editing on the focused text field
	if m.focus != fieldBackend {
		var cmd tea.Cmd
		m.inputs[m.focus], cmd = m.inputs[m.focus].Update(key)
		return m, cmd
	}
	return m, nil
}

func (m *model) focusField(i int) {
	for j := range m.inputs {
		m.inputs[j].Blur()
	}
	m.focus = i
	if i != fieldBackend {
		m.inputs[i].Focus()
	}
}

func (m model) updateAddMore(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "up", "k":
		m.addMoreCur = 0
	case "down", "j":
		m.addMoreCur = 1
	case "enter":
		if m.addMoreCur == 0 {
			m.resetProviderEditor()
			m.step = stepProvider
			return m, nil
		}
		// continue to roles
		if len(m.providers) == 0 {
			return m, nil
		}
		m.enterRoles()
		m.step = stepRoles
		return m, nil
	}
	return m, nil
}

// enterRoles initializes the role-assignment step, pre-selecting the first
// provider (and all-reviewers when there is a single provider so the user can
// just press Enter).
func (m *model) enterRoles() {
	m.coordCur = 0
	m.implCur = 0
	m.revCur = 0
	m.roleFocus = 0
	if m.revSel == nil {
		m.revSel = map[int]bool{}
	}
	if len(m.providers) == 1 {
		m.revSel[0] = true
	}
}

func (m model) updateRoles(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	n := len(m.providers)
	switch key.String() {
	case "tab", "down":
		m.roleFocus = (m.roleFocus + 1) % 3
	case "shift+tab", "up":
		m.roleFocus = (m.roleFocus - 1 + 3) % 3
	case "left":
		switch m.roleFocus {
		case 0:
			m.coordCur = (m.coordCur - 1 + n) % n
		case 1:
			m.implCur = (m.implCur - 1 + n) % n
		case 2:
			m.revCur = (m.revCur - 1 + n) % n
		}
	case "right":
		switch m.roleFocus {
		case 0:
			m.coordCur = (m.coordCur + 1) % n
		case 1:
			m.implCur = (m.implCur + 1) % n
		case 2:
			m.revCur = (m.revCur + 1) % n
		}
	case " ":
		if m.roleFocus == 2 {
			m.revSel[m.revCur] = !m.revSel[m.revCur]
		}
	case "enter":
		m.coord = m.providers[m.coordCur].name
		m.impl = m.providers[m.implCur].name
		var revs []string
		for i, p := range m.providers {
			if m.revSel[i] {
				revs = append(revs, p.name)
			}
		}
		if len(revs) == 0 {
			// default reviewers to the first provider (mirrors single-provider
			// auto-default; buildConfig also guards this).
			revs = []string{m.providers[0].name}
		}
		m.reviewers = revs
		m.completed = true
		m.step = stepDone
		return m, tea.Quit
	}
	return m, nil
}

var (
	titleStyle = lipgloss.NewStyle().Bold(true).Underline(true)
	dimStyle   = lipgloss.NewStyle().Faint(true)
	errStyle   = lipgloss.NewStyle().Bold(true)
	selStyle   = lipgloss.NewStyle().Bold(true)
)

func (m model) View() string {
	var b strings.Builder
	switch m.step {
	case stepProvider:
		b.WriteString(titleStyle.Render("ycc first-run setup — configure a model provider"))
		b.WriteString("\n\n")
		labels := [numFields]string{"name", "backend", "base url", "model", "key env"}
		for i := 0; i < numFields; i++ {
			cursor := "  "
			if m.focus == i {
				cursor = "▸ "
			}
			if i == fieldBackend {
				b.WriteString(fmt.Sprintf("%s%-9s ◂ %s ▸\n", cursor, labels[i]+":", backends[m.backendIdx]))
			} else {
				b.WriteString(fmt.Sprintf("%s%-9s %s\n", cursor, labels[i]+":", m.inputs[i].View()))
			}
		}
		if m.editErr != "" {
			b.WriteString("\n" + errStyle.Render(m.editErr) + "\n")
		}
		if len(m.providers) > 0 {
			b.WriteString("\n" + dimStyle.Render(fmt.Sprintf("%d provider(s) added", len(m.providers))) + "\n")
		}
		b.WriteString("\n" + dimStyle.Render("Tab/↑↓ move · ←/→ change backend · Enter next · Esc skip"))
	case stepAddMore:
		b.WriteString(titleStyle.Render("ycc first-run setup — providers"))
		b.WriteString("\n\n")
		opts := []string{"add another provider", "continue to role assignment"}
		for i, o := range opts {
			cursor := "  "
			line := o
			if m.addMoreCur == i {
				cursor = "▸ "
				line = selStyle.Render(o)
			}
			b.WriteString(cursor + line + "\n")
		}
		b.WriteString("\n" + dimStyle.Render(fmt.Sprintf("%d provider(s): %s", len(m.providers), strings.Join(providerNames(m.providers), ", "))))
		b.WriteString("\n\n" + dimStyle.Render("↑↓ move · Enter select · Esc skip"))
	case stepRoles:
		b.WriteString(titleStyle.Render("ycc first-run setup — assign roles"))
		b.WriteString("\n\n")
		names := providerNames(m.providers)
		row := func(focus int, label, val string) {
			cursor := "  "
			if m.roleFocus == focus {
				cursor = "▸ "
			}
			b.WriteString(fmt.Sprintf("%s%-12s ◂ %s ▸\n", cursor, label+":", val))
		}
		row(0, "coordinator", names[m.coordCur])
		row(1, "implementer", names[m.implCur])
		// reviewers multi-select
		cursor := "  "
		if m.roleFocus == 2 {
			cursor = "▸ "
		}
		b.WriteString(fmt.Sprintf("%sreviewers:\n", cursor))
		for i, name := range names {
			mark := "[ ]"
			if m.revSel[i] {
				mark = "[x]"
			}
			pointer := "   "
			if m.roleFocus == 2 && m.revCur == i {
				pointer = " ▸ "
			}
			b.WriteString(fmt.Sprintf("   %s%s %s\n", pointer, mark, name))
		}
		b.WriteString("\n" + dimStyle.Render("Tab/↑↓ move · ←/→ change · space toggle reviewer · Enter finish · Esc skip"))
	}
	return b.String()
}

func providerNames(ps []provider) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.name
	}
	return out
}
