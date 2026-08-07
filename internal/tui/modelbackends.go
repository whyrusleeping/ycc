// This file owns model-backend list, forms, discovery, and persistence.
package tui

import (
	"fmt"
	"strconv"
	"strings"

	"connectrpc.com/connect"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"

	"github.com/whyrusleeping/ycc/internal/codex"
)

// form mode for the add/edit/duplicate form.
const (
	mbAdd = iota
	mbEdit
	mbDuplicate
)

// form field indices (focus order). backend/thinking/effort/display/persist are
// focusable non-text fields cycled with ←/→; the rest are text inputs.
const (
	mbFieldName = iota
	mbFieldBackend
	mbFieldBaseURL
	mbFieldModel
	mbFieldKeyEnv
	mbFieldAuth
	mbFieldThinking
	mbFieldEffort
	mbFieldDisplay
	mbFieldPriceIn
	mbFieldPriceOut
	mbFieldPriceCacheRead
	mbFieldPriceCacheWrite
	mbNumFields
)

var (
	mbBackendList  = []string{"anthropic", "openai", "ollama"}
	mbThinkingList = []string{"", "adaptive", "off"}
	mbEffortList   = []string{"", "low", "medium", "high", "xhigh", "max"}
	mbDisplayList  = []string{"", "summarized", "omitted"}
	// mbAuthList mirrors config.Model.Auth: "" (the api-key default) or
	// "oauth" (Claude/ChatGPT subscription; anthropic and openai backends
	// only, spec §13). An explicit "api-key" value loaded from config maps
	// onto "" — the two are equivalent.
	mbAuthList = []string{"", "oauth"}

	// mbModelPresets offers a small built-in list of common model ids per backend
	// as suggestions in the model field (spec §13, task 0042). They are
	// suggestions only — free-text entry is always retained, so any id works. The
	// model field stays a normal text input; ctrl+n/ctrl+p just fill it with the
	// next/previous preset for the current backend.
	mbModelPresets = map[string][]string{
		"anthropic": {"claude-opus-4-8", "claude-sonnet-4-5", "claude-haiku-4-5", "claude-fable-5"},
		"openai":    {"gpt-5.5", "gpt-5-mini", "gpt-4o", "o3"},
		"ollama":    {"qwen2.5-coder", "llama3.3", "deepseek-r1"},
	}
)

func mbIsText(i int) bool {
	switch i {
	case mbFieldName, mbFieldBaseURL, mbFieldModel, mbFieldKeyEnv,
		mbFieldPriceIn, mbFieldPriceOut, mbFieldPriceCacheRead, mbFieldPriceCacheWrite:
		return true
	}
	return false
}

func mbLabel(i int) string {
	switch i {
	case mbFieldName:
		return "name"
	case mbFieldBackend:
		return "backend"
	case mbFieldBaseURL:
		return "base url"
	case mbFieldModel:
		return "model id(s)"
	case mbFieldKeyEnv:
		return "key env"
	case mbFieldAuth:
		return "auth"
	case mbFieldThinking:
		return "thinking"
	case mbFieldEffort:
		return "effort"
	case mbFieldDisplay:
		return "thinking disp"
	case mbFieldPriceIn:
		return "price in"
	case mbFieldPriceOut:
		return "price out"
	case mbFieldPriceCacheRead:
		return "price c.read"
	case mbFieldPriceCacheWrite:
		return "price c.write"
	}
	return ""
}

// mbNewInputs (re)initializes the form's text inputs with the wizard's
// CharLimit/Width so the form reads consistently with first-run setup.
func (m *model) mbNewInputs() {
	for i := range m.mbInputs {
		ti := textinput.New()
		ti.CharLimit = 200
		ti.SetWidth(50)
		m.mbInputs[i] = ti
	}
	m.mbInputs[mbFieldName].Placeholder = "logical name (optional; defaults to model id)"
	m.mbInputs[mbFieldBaseURL].Placeholder = "base url"
	m.mbInputs[mbFieldModel].Placeholder = "model id(s), space-separated (ctrl+f to fetch)"
	// The model field holds one or more space/comma-separated ids, so it needs a
	// larger char limit than the other single-value inputs.
	m.mbInputs[mbFieldModel].CharLimit = 800
	m.mbInputs[mbFieldModel].SetWidth(60)
	m.mbInputs[mbFieldKeyEnv].Placeholder = "API key env var name (never the key)"
	m.mbInputs[mbFieldPriceIn].Placeholder = "$/Mtok (optional)"
	m.mbInputs[mbFieldPriceOut].Placeholder = "$/Mtok (optional)"
	m.mbInputs[mbFieldPriceCacheRead].Placeholder = "$/Mtok (optional)"
	m.mbInputs[mbFieldPriceCacheWrite].Placeholder = "$/Mtok (optional)"
}

// mbStartAdd opens a blank "add connection" form. The backend defaults to
// anthropic and the model field is prefilled with that backend's curated ids, so
// a single connection produces sibling logical models out of the box (spec §13).
func (m *model) mbStartAdd() {
	m.mbNewInputs()
	m.mbBackends = append([]string(nil), mbBackendList...)
	m.mbBackendIdx = 0
	m.mbThinkIdx, m.mbEffortIdx, m.mbDisplayIdx = 0, 0, 0
	m.mbPresetIdx = -1
	m.mbFormMode = mbAdd
	m.mbOrigName = ""
	m.mbAuthIdx = 0
	m.mbErr, m.mbInfo = "", ""
	m.mbApplyCuratedIDs()
	m.mbFocus = mbFieldName
	m.mbView = 1
	m.mbFocusInputs()
}

// mbApplyCuratedIDs fills the model-id field with the current backend's curated
// default ids (space-separated). Used when opening the add form and when the
// backend (or, for openai, the auth mechanism) is changed in add mode, so
// switching re-seeds sensible ids. A ChatGPT-subscription (oauth) openai
// connection is seeded with the codex backend's catalog — the platform ids do
// not apply there (spec §13).
func (m *model) mbApplyCuratedIDs() {
	presets := mbModelPresets[m.mbBackends[m.mbBackendIdx]]
	if m.mbBackends[m.mbBackendIdx] == "openai" && mbAuthList[m.mbAuthIdx] == "oauth" {
		presets = codex.Models
	}
	if len(presets) > 0 {
		m.mbInputs[mbFieldModel].SetValue(strings.Join(presets, " "))
		m.mbInputs[mbFieldModel].CursorEnd()
	} else {
		m.mbInputs[mbFieldModel].SetValue("")
	}
}

// mbPrefill fills the form from a loaded ModelConfig for edit/duplicate.
func (m *model) mbPrefill(cfg *v1.ModelConfig, mode int) {
	m.mbNewInputs()
	m.mbFormMode = mode
	m.mbOrigName = cfg.Name
	m.mbOrigModel = cfg.Model
	m.mbAuthIdx = mbIndexOf(mbAuthList, cfg.Auth)
	name := cfg.Name
	if mode == mbDuplicate {
		name = cfg.Name + "-copy"
	}
	m.mbInputs[mbFieldName].SetValue(name)
	m.mbInputs[mbFieldBaseURL].SetValue(cfg.BaseUrl)
	m.mbInputs[mbFieldModel].SetValue(cfg.Model)
	m.mbInputs[mbFieldKeyEnv].SetValue(cfg.KeyEnv)
	m.mbInputs[mbFieldPriceIn].SetValue(fmtPrice(cfg.PriceInput))
	m.mbInputs[mbFieldPriceOut].SetValue(fmtPrice(cfg.PriceOutput))
	m.mbInputs[mbFieldPriceCacheRead].SetValue(fmtPrice(cfg.PriceCacheRead))
	m.mbInputs[mbFieldPriceCacheWrite].SetValue(fmtPrice(cfg.PriceCacheWrite))
	// Preserve a loaded backend that isn't one of the built-in choices (e.g. "glm").
	m.mbBackends = append([]string(nil), mbBackendList...)
	m.mbBackendIdx = mbIndexOrAppend(&m.mbBackends, cfg.Backend)
	m.mbThinkIdx = mbIndexOf(mbThinkingList, cfg.Thinking)
	m.mbEffortIdx = mbIndexOf(mbEffortList, cfg.Effort)
	m.mbDisplayIdx = mbIndexOf(mbDisplayList, cfg.ThinkingDisplay)
	m.mbPresetIdx = -1
	m.mbErr, m.mbInfo = "", ""
	m.mbView = 1
	if mode == mbEdit {
		// The name is read-only in edit mode (rename via duplicate+remove).
		m.mbFocus = mbFieldBackend
	} else {
		m.mbFocus = mbFieldName
	}
	m.mbFocusInputs()
}

func fmtPrice(p *float64) string {
	if p == nil {
		return ""
	}
	return strconv.FormatFloat(*p, 'f', -1, 64)
}

func mbIndexOf(vals []string, cur string) int {
	for i, v := range vals {
		if v == cur {
			return i
		}
	}
	return 0
}

func mbIndexOrAppend(vals *[]string, cur string) int {
	for i, v := range *vals {
		if v == cur {
			return i
		}
	}
	*vals = append(*vals, cur)
	return len(*vals) - 1
}

func parsePrice(s string) (*float64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return nil, err
	}
	return &f, nil
}

func (m *model) mbFocusInputs() {
	for j := range m.mbInputs {
		m.mbInputs[j].Blur()
	}
	if mbIsText(m.mbFocus) {
		m.mbInputs[m.mbFocus].Focus()
	}
}

// mbMoveFocus advances the form focus, skipping the read-only name field in edit
// mode so renaming is only possible via duplicate+remove.
func (m *model) mbMoveFocus(dir int) {
	for i := 0; i < mbNumFields; i++ {
		m.mbFocus = (m.mbFocus + dir + mbNumFields) % mbNumFields
		if m.mbFormMode == mbEdit && m.mbFocus == mbFieldName {
			continue
		}
		break
	}
	m.mbFocusInputs()
}

// mbCycleFocused cycles the focused non-text field (backend/thinking/effort/
// display) or toggles persist with ←/→.
func (m *model) mbCycleFocused(d int) {
	switch m.mbFocus {
	case mbFieldBackend:
		m.mbBackendIdx = (m.mbBackendIdx + d + len(m.mbBackends)) % len(m.mbBackends)
		m.mbPresetIdx = -1
		// Subscription auth is anthropic/openai-only (spec §13): leaving those
		// backends silently resets the auth picker to the api-key default.
		if !mbOAuthBackend(m.mbBackends[m.mbBackendIdx]) {
			m.mbAuthIdx = 0
		}
		// In add mode, re-seed the model-id field with the new backend's curated
		// defaults so switching backend offers sensible ids (spec §13).
		if m.mbFormMode == mbAdd {
			m.mbApplyCuratedIDs()
		}
	case mbFieldAuth:
		// Cycling to oauth only makes sense on backends with subscription
		// support; elsewhere the field is pinned to api-key (the row renders
		// the restriction).
		if mbOAuthBackend(m.mbBackends[m.mbBackendIdx]) {
			m.mbAuthIdx = (m.mbAuthIdx + d + len(mbAuthList)) % len(mbAuthList)
			// The ChatGPT-subscription (codex) backend serves a different model
			// catalog than the platform API: in add mode, re-seed a curated
			// model-id field to match the selected auth (free text is kept).
			if m.mbBackends[m.mbBackendIdx] == "openai" && m.mbFormMode == mbAdd {
				m.mbApplyCuratedIDs()
			}
		}
	case mbFieldThinking:
		m.mbThinkIdx = (m.mbThinkIdx + d + len(mbThinkingList)) % len(mbThinkingList)
	case mbFieldEffort:
		m.mbEffortIdx = (m.mbEffortIdx + d + len(mbEffortList)) % len(mbEffortList)
	case mbFieldDisplay:
		m.mbDisplayIdx = (m.mbDisplayIdx + d + len(mbDisplayList)) % len(mbDisplayList)
	}
}

// updateModelBackends handles input while the model-backends modal is open.
func (m model) updateModelBackends(msg tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		if m.mbView == 1 && mbIsText(m.mbFocus) {
			var cmd tea.Cmd
			m.mbInputs[m.mbFocus], cmd = m.mbInputs[m.mbFocus].Update(msg)
			return m, cmd
		}
		return m, nil
	}
	switch m.mbView {
	case 1:
		return m.mbUpdateForm(key)
	case 2:
		return m.mbUpdateConfirm(key)
	default:
		return m.mbUpdateList(key)
	}
}

func (m model) mbUpdateList(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "ctrl+c":
		return m.confirmQuit()
	case "esc":
		// Back to the settings overlay.
		m.mbOpen = false
		m.overlay = true
		return m, nil
	case "up":
		if m.mbCursor > 0 {
			m.mbCursor--
		}
		return m, nil
	case "down":
		if m.mbCursor < len(m.models)-1 {
			m.mbCursor++
		}
		return m, nil
	case "a":
		m.mbStartAdd()
		return m, nil
	case "e", "enter":
		if m.mbCursor >= len(m.models) {
			return m, nil
		}
		m.mbErr = ""
		return m, m.mbFetchConfig(m.models[m.mbCursor].Name, mbEdit)
	case "d":
		if m.mbCursor >= len(m.models) {
			return m, nil
		}
		m.mbErr = ""
		return m, m.mbFetchConfig(m.models[m.mbCursor].Name, mbDuplicate)
	case "x":
		if m.mbCursor >= len(m.models) {
			return m, nil
		}
		m.mbErr = ""
		m.mbView = 2
		return m, nil
	}
	return m, nil
}

func (m model) mbUpdateForm(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "ctrl+c":
		return m.confirmQuit()
	case "esc":
		m.mbView = 0
		m.mbErr, m.mbInfo = "", ""
		return m, nil
	case "tab", "down":
		m.mbMoveFocus(1)
		return m, nil
	case "shift+tab", "up":
		m.mbMoveFocus(-1)
		return m, nil
	case "left":
		m.mbCycleFocused(-1)
		return m, nil
	case "right":
		m.mbCycleFocused(1)
		return m, nil
	case "enter":
		return m.mbSubmitForm()
	case "ctrl+f":
		// Fetch the connection's available model ids into the model-id field.
		m.mbBusy = true
		m.mbErr = ""
		m.mbInfo = "fetching available models…"
		return m, m.mbDiscover()
	case "ctrl+n":
		// On the model field, ctrl+n/ctrl+p cycle the backend's id presets while
		// keeping the field free-text. Elsewhere they fall through unchanged.
		if m.mbFocus == mbFieldModel {
			m.mbCyclePreset(1)
			return m, nil
		}
	case "ctrl+p":
		if m.mbFocus == mbFieldModel {
			m.mbCyclePreset(-1)
			return m, nil
		}
	}
	if mbIsText(m.mbFocus) {
		var cmd tea.Cmd
		m.mbInputs[m.mbFocus], cmd = m.mbInputs[m.mbFocus].Update(key)
		return m, cmd
	}
	return m, nil
}

// mbCyclePreset fills the model field with the next/previous built-in id preset
// for the current backend (task 0042). It is a convenience over free text — the
// field remains a normal text input the user can overtype.
func (m *model) mbCyclePreset(d int) {
	presets := mbModelPresets[m.mbBackends[m.mbBackendIdx]]
	if len(presets) == 0 {
		return
	}
	m.mbPresetIdx = (m.mbPresetIdx + d + len(presets)) % len(presets)
	m.mbInputs[mbFieldModel].SetValue(presets[m.mbPresetIdx])
	m.mbInputs[mbFieldModel].CursorEnd()
}

// parseModelIDs splits the model-id field into individual ids on whitespace and
// commas, trimming and de-duplicating while preserving order.
func parseModelIDs(s string) []string {
	fields := strings.FieldsFunc(s, func(r rune) bool {
		return r == ' ' || r == '\t' || r == '\n' || r == ','
	})
	seen := make(map[string]bool, len(fields))
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		f = strings.TrimSpace(f)
		if f == "" || seen[f] {
			continue
		}
		seen[f] = true
		out = append(out, f)
	}
	return out
}

// mbSubmitForm validates the connection form and issues UpsertModel for each
// model id entered (spec §13, §18.2). A single connection (backend + base_url +
// key_env + reasoning/pricing) with N model ids becomes N sibling logical models,
// each named after its model id (so the role pickers can select opus vs sonnet vs
// fable within one connection). With a single id an explicit name is honored. In
// edit mode the edited model keeps its logical name for its own model id; any
// extra ids become new siblings on the same connection.
func (m model) mbSubmitForm() (tea.Model, tea.Cmd) {
	explicitName := strings.TrimSpace(m.mbInputs[mbFieldName].Value())
	backend := m.mbBackends[m.mbBackendIdx]
	ids := parseModelIDs(m.mbInputs[mbFieldModel].Value())
	if backend == "" || len(ids) == 0 {
		m.mbErr = "backend and at least one model id are required"
		return m, nil
	}

	// Shared connection fields for every sibling.
	backendURL := strings.TrimSpace(m.mbInputs[mbFieldBaseURL].Value())
	keyEnv := strings.TrimSpace(m.mbInputs[mbFieldKeyEnv].Value())
	auth := mbAuthList[m.mbAuthIdx]
	if auth == "oauth" && !mbOAuthBackend(backend) {
		// Normally unreachable (the picker pins unsupported backends to
		// api-key), but guard against stale state anyway.
		m.mbErr = "auth \"oauth\" is only supported by the anthropic and openai backends"
		return m, nil
	}
	thinking := mbThinkingList[m.mbThinkIdx]
	effort := mbEffortList[m.mbEffortIdx]
	display := mbDisplayList[m.mbDisplayIdx]

	var priceIn, priceOut, priceCacheRead, priceCacheWrite *float64
	prices := []struct {
		idx   int
		dst   **float64
		label string
	}{
		{mbFieldPriceIn, &priceIn, "price in"},
		{mbFieldPriceOut, &priceOut, "price out"},
		{mbFieldPriceCacheRead, &priceCacheRead, "price cache read"},
		{mbFieldPriceCacheWrite, &priceCacheWrite, "price cache write"},
	}
	for _, p := range prices {
		v, err := parsePrice(m.mbInputs[p.idx].Value())
		if err != nil {
			m.mbErr = p.label + " must be a number"
			return m, nil
		}
		*p.dst = v
	}

	// Compute the logical name for each id. By default the name is the id itself.
	names := mbModelNames(ids, m.mbFormMode, m.mbOrigName, m.mbOrigModel, explicitName)

	var cfgs []*v1.ModelConfig
	for i, id := range ids {
		cfgs = append(cfgs, &v1.ModelConfig{
			Name:            names[i],
			Backend:         backend,
			BaseUrl:         backendURL,
			Model:           id,
			KeyEnv:          keyEnv,
			Auth:            auth,
			Thinking:        thinking,
			Effort:          effort,
			ThinkingDisplay: display,
			PriceInput:      priceIn,
			PriceOutput:     priceOut,
			PriceCacheRead:  priceCacheRead,
			PriceCacheWrite: priceCacheWrite,
		})
	}
	m.mbErr, m.mbInfo = "", ""
	return m, m.mbUpsertMany(cfgs)
}

// mbModelNames assigns a logical name to each model id. Names default to the id
// itself so a connection's model ids become self-named sibling models. Two
// special cases preserve an existing logical name:
//   - add/duplicate with exactly one id and an explicit name → use that name;
//   - edit mode → the edited model keeps its name (origName) for its own id
//     (origModel), or, if that id is gone, for the first id (an id rename). All
//     other ids are new siblings named after themselves.
func mbModelNames(ids []string, formMode int, origName, origModel, explicitName string) []string {
	names := make([]string, len(ids))
	for i, id := range ids {
		names[i] = id
	}
	if formMode == mbEdit && origName != "" {
		keep := -1
		for i, id := range ids {
			if id == origModel {
				keep = i
				break
			}
		}
		if keep == -1 {
			keep = 0 // original id was changed → treat the first id as the rename
		}
		names[keep] = origName
	} else if len(ids) == 1 && explicitName != "" {
		names[0] = explicitName
	}
	return names
}

func (m model) mbUpdateConfirm(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "ctrl+c":
		return m.confirmQuit()
	case "esc":
		m.mbView = 0
		return m, nil
	case "enter":
		if m.mbCursor >= len(m.models) {
			m.mbView = 0
			return m, nil
		}
		return m, m.mbRemove(m.models[m.mbCursor].Name)
	}
	return m, nil
}

// mbFetchConfig loads a model backend's full record for the edit/duplicate form.
func (m model) mbFetchConfig(name string, mode int) tea.Cmd {
	return func() tea.Msg {
		resp, err := m.client.GetModelConfig(m.ctx, connect.NewRequest(&v1.GetModelConfigRequest{Name: name}))
		if err != nil {
			return mbPrefillMsg{err: err, mode: mode}
		}
		return mbPrefillMsg{cfg: resp.Msg.Model, mode: mode}
	}
}

// mbUpsert adds or replaces a logical model backend. The change always takes
// effect at runtime and is written back to ycc.toml by the daemon.
func (m model) mbUpsert(cfg *v1.ModelConfig) tea.Cmd {
	return func() tea.Msg {
		if _, err := m.client.UpsertModel(m.ctx, connect.NewRequest(&v1.UpsertModelRequest{
			Model: cfg, Persist: true,
		})); err != nil {
			return mbWriteMsg{err: err}
		}
		return mbWriteMsg{}
	}
}

// mbUpsertMany upserts several sibling logical models (one per model id) that
// share a connection (spec §13). Any failure aborts and is surfaced inline; the
// models upserted before the failure remain (idempotent re-submit fixes it).
func (m model) mbUpsertMany(cfgs []*v1.ModelConfig) tea.Cmd {
	return func() tea.Msg {
		for _, cfg := range cfgs {
			if _, err := m.client.UpsertModel(m.ctx, connect.NewRequest(&v1.UpsertModelRequest{
				Model: cfg, Persist: true,
			})); err != nil {
				return mbWriteMsg{err: fmt.Errorf("%s: %w", cfg.Name, err)}
			}
		}
		return mbWriteMsg{}
	}
}

// mbDiscover queries the backend connection currently entered in the form for its
// available model ids (spec §13). The result populates the model-id field.
func (m model) mbDiscover() tea.Cmd {
	backend := m.mbBackends[m.mbBackendIdx]
	baseURL := strings.TrimSpace(m.mbInputs[mbFieldBaseURL].Value())
	keyEnv := strings.TrimSpace(m.mbInputs[mbFieldKeyEnv].Value())
	return func() tea.Msg {
		resp, err := m.client.DiscoverModels(m.ctx, connect.NewRequest(&v1.DiscoverModelsRequest{
			Backend: backend, BaseUrl: baseURL, KeyEnv: keyEnv,
		}))
		if err != nil {
			return mbDiscoverMsg{err: err}
		}
		return mbDiscoverMsg{ids: resp.Msg.ModelIds, note: resp.Msg.Note, fromNet: resp.Msg.FromNetwork}
	}
}

// mbRemove deletes a logical model backend; rejected if a role still references it.
func (m model) mbRemove(name string) tea.Cmd {
	return func() tea.Msg {
		if _, err := m.client.RemoveModel(m.ctx, connect.NewRequest(&v1.RemoveModelRequest{
			Name: name, Persist: true,
		})); err != nil {
			return mbWriteMsg{err: err}
		}
		return mbWriteMsg{}
	}
}

func (m model) modelBackendsView() string {
	switch m.mbView {
	case 1:
		return m.mbFormView()
	case 2:
		return m.mbConfirmView()
	default:
		return m.mbListView()
	}
}

func (m model) mbListView() string {
	var b strings.Builder
	if len(m.models) == 0 {
		b.WriteString(dimStyle.Render("(no model backends configured)") + "\n")
	}
	// Window the rows around the cursor so the card never overruns the terminal
	// vertically (mirrors browserCard). Fixed chrome: modalCard's 6 non-content
	// rows plus the trailing note (2) and the error block (2) when present.
	hint := "a add · e/enter edit · d duplicate · x remove · esc back"
	budget := len(m.models)
	if m.h > 0 {
		chrome := 6 + 2
		if m.mbErr != "" {
			chrome += 2
		}
		budget = m.h - chrome
		if budget < 1 {
			budget = 1
		}
	}
	start, end := listWindow(m.mbCursor, len(m.models), budget)
	if start > 0 || end < len(m.models) {
		hint = fmt.Sprintf("%s · %d–%d/%d", hint, start+1, end, len(m.models))
	}
	for i, mm := range m.models[start:end] {
		cursor := "  "
		row := fmt.Sprintf("%-16s %-12s %s", mm.Name, mm.Backend, mm.Model)
		if start+i == m.mbCursor {
			cursor = selStyle.Render("▸ ")
			row = selStyle.Render(row)
		}
		b.WriteString(cursor + row + "\n")
	}
	if m.mbErr != "" {
		b.WriteString("\n" + errStyle.Render(m.mbErr) + "\n")
	}
	b.WriteString("\n" + dimStyle.Render("changes are saved to ycc.toml automatically"))
	return m.modalCard(" model backends ", strings.TrimRight(b.String(), "\n"), hint)
}

func (m model) mbFormView() string {
	var b strings.Builder
	title := "add model backend"
	switch m.mbFormMode {
	case mbEdit:
		title = "edit model backend"
	case mbDuplicate:
		title = "duplicate model backend"
	}
	order := []int{
		mbFieldName, mbFieldBackend, mbFieldBaseURL, mbFieldModel, mbFieldKeyEnv, mbFieldAuth,
		mbFieldThinking, mbFieldEffort, mbFieldDisplay,
		mbFieldPriceIn, mbFieldPriceOut, mbFieldPriceCacheRead, mbFieldPriceCacheWrite,
	}
	for _, f := range order {
		cursor := "  "
		if m.mbFocus == f {
			cursor = selStyle.Render("▸ ")
		}
		label := fmt.Sprintf("%-14s", mbLabel(f)+":")
		var val string
		switch f {
		case mbFieldName:
			if m.mbFormMode == mbEdit {
				val = dimStyle.Render(m.mbInputs[mbFieldName].Value() + "  (rename via duplicate)")
			} else {
				val = m.mbInputs[f].View()
			}
		case mbFieldBackend:
			val = "◂ " + m.mbBackends[m.mbBackendIdx] + " ▸"
		case mbFieldAuth:
			if mbOAuthBackend(m.mbBackends[m.mbBackendIdx]) {
				val = "◂ " + mbShowAuth(mbAuthList[m.mbAuthIdx]) + " ▸"
			} else {
				val = dimStyle.Render("api key  (oauth is anthropic/openai-only)")
			}
		case mbFieldThinking:
			val = "◂ " + mbShow(mbThinkingList[m.mbThinkIdx]) + " ▸"
		case mbFieldEffort:
			val = "◂ " + mbShow(mbEffortList[m.mbEffortIdx]) + " ▸"
		case mbFieldDisplay:
			val = "◂ " + mbShow(mbDisplayList[m.mbDisplayIdx]) + " ▸"
		default:
			val = m.mbInputs[f].View()
		}
		b.WriteString(cursor + label + " " + val + "\n")
		// Under the focused auth field, explain the choice: oauth means a
		// Claude (Pro/Max) or ChatGPT (Plus/Pro) subscription set up via
		// `ycc login <backend>`, and key env is unused.
		if f == mbFieldAuth && m.mbFocus == mbFieldAuth && mbOAuthBackend(m.mbBackends[m.mbBackendIdx]) {
			backend := m.mbBackends[m.mbBackendIdx]
			sub := "Claude subscription (Pro/Max)"
			if backend == "openai" {
				sub = "ChatGPT subscription (Plus/Pro)"
			}
			if mbAuthList[m.mbAuthIdx] == "oauth" {
				b.WriteString("    " + dimStyle.Render(sub+" — run `ycc login "+backend+"` once; key env is ignored") + "\n")
			} else {
				b.WriteString("    " + dimStyle.Render("api key from key env · ←/→ for oauth ("+sub+")") + "\n")
			}
		}
		// Under the focused model field, hint that multiple ids are allowed and how
		// to fetch/cycle them. Free text always works.
		if f == mbFieldModel && m.mbFocus == mbFieldModel {
			b.WriteString("    " + dimStyle.Render("space-separated ids · ctrl+f fetch from backend · ctrl+n/p cycle presets") + "\n")
			if presets := mbModelPresets[m.mbBackends[m.mbBackendIdx]]; len(presets) > 0 {
				b.WriteString("    " + dimStyle.Render("presets: "+strings.Join(presets, " · ")) + "\n")
			}
		}
	}
	if m.mbInfo != "" {
		b.WriteString("\n" + dimStyle.Render(m.mbInfo) + "\n")
	}
	if m.mbErr != "" {
		b.WriteString("\n" + errStyle.Render(m.mbErr) + "\n")
	}
	b.WriteString("\n" + dimStyle.Render("(keys are env-var references only — never paste a secret)"))
	return m.modalCard(" "+title+" ", strings.TrimRight(b.String(), "\n"),
		"Tab/↑↓ move · ←/→ change · ctrl+f fetch models · enter save · esc back")
}

func (m model) mbConfirmView() string {
	var b strings.Builder
	name := ""
	if m.mbCursor < len(m.models) {
		name = m.models[m.mbCursor].Name
	}
	b.WriteString("remove " + selStyle.Render(name) + "?\n")
	b.WriteString("\n" + dimStyle.Render("this is saved to ycc.toml"))
	if m.mbErr != "" {
		b.WriteString("\n\n" + errStyle.Render(m.mbErr) + "\n")
	}
	return m.modalCard(" remove model backend ", strings.TrimRight(b.String(), "\n"),
		"enter confirm · esc cancel")
}

// mbShow renders an empty cycle value as "(none)" for readability.
func mbShow(s string) string {
	if s == "" {
		return "(none)"
	}
	return s
}

// mbShowAuth renders the auth cycle's values with friendlier names: the empty
// default is api-key auth, and oauth is labeled for what it is.
func mbShowAuth(s string) string {
	if s == "oauth" {
		return "oauth (subscription)"
	}
	return "api key"
}

// mbOAuthBackend reports whether a backend supports subscription (oauth)
// auth: anthropic (Claude Pro/Max) and openai (ChatGPT Plus/Pro via the codex
// backend). Mirrors config.Model.validateAuth.
func mbOAuthBackend(b string) bool {
	return b == "anthropic" || b == "openai"
}
