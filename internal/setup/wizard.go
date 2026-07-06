package setup

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/whyrusleeping/ycc/internal/config"
	"github.com/whyrusleeping/ycc/internal/secrets"
)

// step is the wizard's current screen.
type step int

const (
	stepProvider step = iota // editing the current provider's fields
	stepVerify               // testing the just-entered provider's connection
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
	fieldKey
	numFields
)

// verifyMsg carries the result of a connection check (stepVerify).
type verifyMsg struct{ err error }

// discoverMsg carries the result of a ctrl+f model discovery in the editor.
type discoverMsg struct {
	ids []string
	err error
}

// model is the Bubble Tea wizard. State is exposed at package scope so tests can
// drive transitions with synthetic tea.KeyMsg and assert outcomes.
type model struct {
	step step

	// in-progress provider editor
	inputs      [numFields]textinput.Model // name, base_url, model, key_env, key (backend slot unused)
	backendIdx  int                        // index into backends
	focus       int                        // current field index
	editErr     string
	editInfo    string   // inline info line (e.g. "N models fetched")
	fetchedIDs  []string // ids from the last successful ctrl+f discovery
	cycleIdx    int      // cursor into the id cycle source (ctrl+n/p)
	discovering bool     // a ctrl+f discovery is in flight

	// verify step
	candidate  provider                           // provider awaiting verification
	verify     func(p provider) error             // injectable connection check
	discover   func(p provider) ([]string, error) // injectable model discovery
	verifying  bool
	verifyDone bool
	verifyErr  error

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
		ti.SetWidth(50)
		m.inputs[i] = ti
	}
	m.inputs[fieldName].Placeholder = "logical name (e.g. claude)"
	m.inputs[fieldBaseURL].Placeholder = "base url"
	m.inputs[fieldModel].Placeholder = "model id"
	m.inputs[fieldKeyEnv].Placeholder = "API key env var name"
	m.inputs[fieldKey].Placeholder = "paste API key now (optional, stored locally)"
	m.inputs[fieldKey].EchoMode = textinput.EchoPassword
	m.inputs[fieldKey].CharLimit = 500
	m.cycleIdx = -1
	m.verify = realVerify
	m.discover = realDiscover
	m.applyBackendDefaults("")
	m.focus = fieldName
	m.inputs[fieldName].Focus()
	m.revSel = map[int]bool{}
	return m
}

// resolveKey picks the API credential to use for a provider: the freshly-pasted
// value wins, then the env var, then the machine-local secrets store, then "".
func resolveKey(p provider) string {
	if p.key != "" {
		return p.key
	}
	if p.keyEnv != "" {
		if v := os.Getenv(p.keyEnv); v != "" {
			return v
		}
		if v, ok := secrets.Lookup(p.keyEnv); ok {
			return v
		}
	}
	return ""
}

// realVerify tests a provider's connection by listing its models with a short
// timeout. A nil error means the credentials + base_url reach the backend.
func realVerify(p provider) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := config.DiscoverModels(ctx, p.backend, p.baseURL, resolveKey(p))
	return err
}

// realDiscover lists model ids for a provider (ctrl+f in the editor).
func realDiscover(p provider) ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return config.DiscoverModels(ctx, p.backend, p.baseURL, resolveKey(p))
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
	// A backend change invalidates any previously-fetched id list.
	m.fetchedIDs = nil
	m.cycleIdx = -1
}

func (m *model) resetProviderEditor() {
	for i := range m.inputs {
		m.inputs[i].SetValue("")
		m.inputs[i].Blur()
	}
	m.backendIdx = 0
	m.fetchedIDs = nil
	m.cycleIdx = -1
	m.applyBackendDefaults("")
	m.focus = fieldName
	m.inputs[fieldName].Focus()
	m.editErr = ""
	m.editInfo = ""
}

// currentProvider snapshots the editor's fields into a provider value.
func (m model) currentProvider() provider {
	return provider{
		name:    strings.TrimSpace(m.inputs[fieldName].Value()),
		backend: backends[m.backendIdx],
		baseURL: strings.TrimSpace(m.inputs[fieldBaseURL].Value()),
		model:   strings.TrimSpace(m.inputs[fieldModel].Value()),
		keyEnv:  strings.TrimSpace(m.inputs[fieldKeyEnv].Value()),
		key:     strings.TrimSpace(m.inputs[fieldKey].Value()),
	}
}

func (m model) verifyCmd(p provider) tea.Cmd {
	verify := m.verify
	return func() tea.Msg { return verifyMsg{err: verify(p)} }
}

func (m model) discoverCmd(p provider) tea.Cmd {
	discover := m.discover
	return func() tea.Msg {
		ids, err := discover(p)
		return discoverMsg{ids: ids, err: err}
	}
}

func (m model) Init() tea.Cmd { return nil }

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case verifyMsg:
		m.verifying = false
		m.verifyDone = true
		m.verifyErr = msg.err
		return m, nil
	case discoverMsg:
		m.discovering = false
		if msg.err != nil {
			m.editErr = "discover: " + msg.err.Error()
			m.editInfo = ""
		} else {
			m.fetchedIDs = msg.ids
			m.cycleIdx = -1
			m.editErr = ""
			m.editInfo = fmt.Sprintf("%d models fetched — ctrl+n/p to cycle", len(msg.ids))
		}
		return m, nil
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "esc":
			m.skipped = true
			return m, tea.Quit
		}
		switch m.step {
		case stepProvider:
			return m.updateProvider(msg)
		case stepVerify:
			return m.updateVerify(msg)
		case stepAddMore:
			return m.updateAddMore(msg)
		case stepRoles:
			return m.updateRoles(msg)
		}
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
	case "ctrl+f":
		p := m.currentProvider()
		if p.backend == "" || p.baseURL == "" {
			m.editErr = "backend and base url are required to fetch models"
			return m, nil
		}
		m.discovering = true
		m.editErr = ""
		m.editInfo = "fetching models…"
		return m, m.discoverCmd(p)
	case "ctrl+n":
		m.cyclePreset(1)
		return m, nil
	case "ctrl+p":
		m.cyclePreset(-1)
		return m, nil
	case "enter":
		p := m.currentProvider()
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
		m.editErr = ""
		m.candidate = p
		m.step = stepVerify
		m.verifying = true
		m.verifyDone = false
		m.verifyErr = nil
		return m, m.verifyCmd(p)
	}
	// text editing on the focused text field
	if m.focus != fieldBackend {
		var cmd tea.Cmd
		m.inputs[m.focus], cmd = m.inputs[m.focus].Update(key)
		return m, cmd
	}
	return m, nil
}

// cyclePreset fills the model field with the next/previous id from the cycle
// source: the last ctrl+f discovery when present, otherwise the backend's
// curated defaults. The field remains a normal text input the user can overtype.
func (m *model) cyclePreset(d int) {
	src := m.fetchedIDs
	if len(src) == 0 {
		src = config.CuratedModelIDs(backends[m.backendIdx])
	}
	if len(src) == 0 {
		return
	}
	m.cycleIdx = (m.cycleIdx + d + len(src)) % len(src)
	m.inputs[fieldModel].SetValue(src[m.cycleIdx])
	m.inputs[fieldModel].CursorEnd()
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

// updateVerify drives the verification screen: while the check runs keys are
// ignored (esc still skips, handled globally). Once done, Enter accepts (on pass
// it just continues; on failure it accepts anyway), [r] retries, [e] returns to
// the editor with all values (including the pasted key) retained.
func (m model) updateVerify(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.verifying {
		return m, nil
	}
	switch key.String() {
	case "e":
		// Return to the editor (offered on pass and fail alike; the footer
		// advertises it in both states). All field values are retained.
		m.step = stepProvider
		m.editErr = ""
		m.focusField(fieldName)
		return m, nil
	case "r":
		m.verifying = true
		m.verifyDone = false
		m.verifyErr = nil
		return m, m.verifyCmd(m.candidate)
	case "enter":
		m.providers = append(m.providers, m.candidate)
		m.step = stepAddMore
		m.addMoreCur = 1 // default to "continue"
		return m, nil
	}
	return m, nil
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
	okStyle    = lipgloss.NewStyle().Bold(true)
	selStyle   = lipgloss.NewStyle().Bold(true)
)

// View renders the wizard. AltScreen is declared here (a v2 View property)
// rather than via a NewProgram option.
func (m model) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	return v
}

func (m model) render() string {
	var b strings.Builder
	switch m.step {
	case stepProvider:
		b.WriteString(titleStyle.Render("ycc first-run setup — configure a model provider"))
		b.WriteString("\n\n")
		labels := [numFields]string{"name", "backend", "base url", "model", "key env", "api key"}
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
		} else if m.editInfo != "" {
			b.WriteString("\n" + dimStyle.Render(m.editInfo) + "\n")
		}
		if len(m.providers) > 0 {
			b.WriteString("\n" + dimStyle.Render(fmt.Sprintf("%d provider(s) added", len(m.providers))) + "\n")
		}
		b.WriteString("\n" + dimStyle.Render("Tab/↑↓ move · ←/→ backend · ctrl+f fetch models · ctrl+n/p cycle ids · Enter verify · Esc skip"))
	case stepVerify:
		b.WriteString(titleStyle.Render("ycc first-run setup — verify connection"))
		b.WriteString("\n\n")
		b.WriteString(fmt.Sprintf("provider: %s  (backend %s)\n", m.candidate.name, m.candidate.backend))
		b.WriteString(fmt.Sprintf("base url: %s\n\n", m.candidate.baseURL))
		switch {
		case m.verifying:
			b.WriteString(dimStyle.Render("verifying connection…"))
		case m.verifyErr != nil:
			b.WriteString(errStyle.Render("✗ verification failed: "+m.verifyErr.Error()) + "\n")
			b.WriteString("\n" + dimStyle.Render("[e] edit  ·  [r] retry  ·  [enter] accept anyway  ·  Esc skip"))
		default:
			b.WriteString(okStyle.Render("✓ connection ok") + "\n")
			b.WriteString("\n" + dimStyle.Render("Enter continue · [e] edit · Esc skip"))
		}
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
