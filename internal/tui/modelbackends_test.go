package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/whyrusleeping/ycc/internal/codex"
	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
)

func TestModelBackendsAdd(t *testing.T) {
	f := newFakeClient(&v1.ModelConfig{Name: "claude", Backend: "anthropic", Model: "claude-3"})
	m := newBackendsModel(f)
	m = runCmds(t, m, m.fetchModels) // populate the list
	if len(m.models) != 1 {
		t.Fatalf("expected 1 model, got %d", len(m.models))
	}
	// Open a blank add form.
	m = drive(t, m, "a")
	if m.mbView != 1 || m.mbFormMode != mbAdd {
		t.Fatalf("after 'a' mbView=%d mbFormMode=%d", m.mbView, m.mbFormMode)
	}
	// name (focused) -> backend default anthropic; type name.
	m = typeText(t, m, "gpt")
	// move to backend, cycle to openai.
	m = drive(t, m, "tab")
	m = drive(t, m, "right") // anthropic -> openai
	// move to model field (backend->base_url->model) and type.
	m = drive(t, m, "tab") // base_url
	m = drive(t, m, "tab") // model
	// The add form seeds the model field with the backend's curated ids; clear it
	// and enter a single id so this exercises the single-model path.
	m.mbInputs[mbFieldModel].SetValue("")
	m = typeText(t, m, "gpt-5")
	// move to key_env and type.
	m = drive(t, m, "tab")
	m = typeText(t, m, "OPENAI_API_KEY")
	// Submit. Backend edits always persist to ycc.toml (no opt-out).
	m = drive(t, m, "enter")
	if f.lastUpsert == nil {
		t.Fatal("UpsertModel was not called")
	}
	if f.lastUpsert.Name != "gpt" || f.lastUpsert.Backend != "openai" || f.lastUpsert.Model != "gpt-5" {
		t.Fatalf("UpsertModel got name=%q backend=%q model=%q", f.lastUpsert.Name, f.lastUpsert.Backend, f.lastUpsert.Model)
	}
	if f.lastUpsert.KeyEnv != "OPENAI_API_KEY" {
		t.Fatalf("UpsertModel key_env=%q", f.lastUpsert.KeyEnv)
	}
	if !f.lastPersist {
		t.Fatal("expected persist=true")
	}
	if m.mbView != 0 {
		t.Fatalf("after submit mbView=%d, want 0 (list)", m.mbView)
	}
	// The list refreshed so role pickers see the new model.
	if len(m.models) != 2 {
		t.Fatalf("after add list has %d models, want 2", len(m.models))
	}
}

func TestModelBackendsEdit(t *testing.T) {
	f := newFakeClient(&v1.ModelConfig{Name: "claude", Backend: "anthropic", Model: "claude-3", KeyEnv: "ANTHROPIC_API_KEY"})
	m := newBackendsModel(f)
	m = runCmds(t, m, m.fetchModels)
	// Edit the selected (only) model: GetModelConfig prefill, then change model id.
	m = drive(t, m, "e")
	if m.mbView != 1 || m.mbFormMode != mbEdit {
		t.Fatalf("after 'e' mbView=%d mbFormMode=%d", m.mbView, m.mbFormMode)
	}
	if got := m.mbInputs[mbFieldName].Value(); got != "claude" {
		t.Fatalf("prefill name=%q, want claude", got)
	}
	if got := m.mbInputs[mbFieldModel].Value(); got != "claude-3" {
		t.Fatalf("prefill model=%q, want claude-3", got)
	}
	// Focus starts on backend (name is read-only in edit). Move to model and edit.
	m = drive(t, m, "tab") // base_url
	m = drive(t, m, "tab") // model
	m = typeText(t, m, "-opus")
	m = drive(t, m, "enter")
	if f.lastUpsert == nil || f.lastUpsert.Name != "claude" {
		t.Fatalf("edit UpsertModel name=%v", f.lastUpsert)
	}
	if f.lastUpsert.Model != "claude-3-opus" {
		t.Fatalf("edit UpsertModel model=%q, want claude-3-opus", f.lastUpsert.Model)
	}
}

func TestModelBackendsDuplicate(t *testing.T) {
	f := newFakeClient(&v1.ModelConfig{Name: "claude", Backend: "anthropic", Model: "claude-3", KeyEnv: "ANTHROPIC_API_KEY"})
	m := newBackendsModel(f)
	m = runCmds(t, m, m.fetchModels)
	m = drive(t, m, "d")
	if m.mbView != 1 || m.mbFormMode != mbDuplicate {
		t.Fatalf("after 'd' mbView=%d mbFormMode=%d", m.mbView, m.mbFormMode)
	}
	if got := m.mbInputs[mbFieldName].Value(); got != "claude-copy" {
		t.Fatalf("duplicate name=%q, want claude-copy", got)
	}
	if got := m.mbInputs[mbFieldModel].Value(); got != "claude-3" {
		t.Fatalf("duplicate kept model=%q, want claude-3", got)
	}
	m = drive(t, m, "enter")
	if f.lastUpsert == nil || f.lastUpsert.Name != "claude-copy" {
		t.Fatalf("duplicate UpsertModel name=%v", f.lastUpsert)
	}
	if f.lastUpsert.Model != "claude-3" || f.lastUpsert.Backend != "anthropic" {
		t.Fatalf("duplicate kept fields: model=%q backend=%q", f.lastUpsert.Model, f.lastUpsert.Backend)
	}
	if len(m.models) != 2 {
		t.Fatalf("after duplicate list has %d models, want 2", len(m.models))
	}
}

// TestModelBackendsDuplicatePricing strengthens duplicate coverage: a model with
// pricing pointers is duplicated, and the resulting UpsertModel carries the same
// pricing values plus the shared base_url/key_env under a new name (task 0042 —
// a credential-sharing sibling that differs only in name + model id).
func TestModelBackendsDuplicatePricing(t *testing.T) {
	pi, po, cr, cw := 3.0, 15.0, 0.3, 3.75
	f := newFakeClient(&v1.ModelConfig{
		Name: "claude", Backend: "anthropic",
		BaseUrl: "https://api.anthropic.com", Model: "claude-opus-4-8",
		KeyEnv:     "ANTHROPIC_API_KEY",
		PriceInput: &pi, PriceOutput: &po, PriceCacheRead: &cr, PriceCacheWrite: &cw,
	})
	m := newBackendsModel(f)
	m = runCmds(t, m, m.fetchModels)
	m = drive(t, m, "d")
	if m.mbView != 1 || m.mbFormMode != mbDuplicate {
		t.Fatalf("after 'd' mbView=%d mbFormMode=%d", m.mbView, m.mbFormMode)
	}
	// Change only the model id to a sibling (sonnet) — credentials are untouched.
	m.mbInputs[mbFieldModel].SetValue("claude-sonnet-4-5")
	m = drive(t, m, "enter")
	u := f.lastUpsert
	if u == nil {
		t.Fatal("duplicate UpsertModel not called")
	}
	if u.Name != "claude-copy" || u.Model != "claude-sonnet-4-5" {
		t.Fatalf("sibling name=%q model=%q, want claude-copy / claude-sonnet-4-5", u.Name, u.Model)
	}
	// Shared credential/endpoint carried over without re-entry.
	if u.BaseUrl != "https://api.anthropic.com" || u.KeyEnv != "ANTHROPIC_API_KEY" || u.Backend != "anthropic" {
		t.Fatalf("sibling did not inherit credentials: base=%q key=%q backend=%q", u.BaseUrl, u.KeyEnv, u.Backend)
	}
	// Pricing pointers carried over identically.
	if u.PriceInput == nil || *u.PriceInput != pi ||
		u.PriceOutput == nil || *u.PriceOutput != po ||
		u.PriceCacheRead == nil || *u.PriceCacheRead != cr ||
		u.PriceCacheWrite == nil || *u.PriceCacheWrite != cw {
		t.Fatalf("sibling pricing mismatch: in=%v out=%v cr=%v cw=%v",
			u.PriceInput, u.PriceOutput, u.PriceCacheRead, u.PriceCacheWrite)
	}
}

// TestModelBackendsAddConnectionSiblings exercises the connection-centric add
// flow (spec §13): the anthropic add form seeds the model field with curated ids,
// and submitting creates one sibling logical model per id, each named after its
// model id and sharing the connection's credentials.
func TestModelBackendsAddConnectionSiblings(t *testing.T) {
	f := newFakeClient()
	m := newBackendsModel(f)
	m = runCmds(t, m, m.fetchModels)
	m = drive(t, m, "a") // add form, backend defaults to anthropic
	// The model field is prefilled with anthropic curated ids.
	if got := m.mbInputs[mbFieldModel].Value(); got == "" {
		t.Fatal("expected model field prefilled with curated ids")
	}
	// Set an explicit connection + a specific set of ids.
	m.mbInputs[mbFieldBaseURL].SetValue("https://api.anthropic.com")
	m.mbInputs[mbFieldKeyEnv].SetValue("ANTHROPIC_API_KEY")
	m.mbInputs[mbFieldModel].SetValue("claude-opus-4-8 claude-sonnet-4-5 claude-fable-5")
	m = drive(t, m, "enter")

	if len(f.upserts) != 3 {
		t.Fatalf("expected 3 sibling upserts, got %d", len(f.upserts))
	}
	want := map[string]bool{"claude-opus-4-8": true, "claude-sonnet-4-5": true, "claude-fable-5": true}
	for _, u := range f.upserts {
		if u.Name != u.Model {
			t.Errorf("sibling name=%q should equal model id=%q", u.Name, u.Model)
		}
		if !want[u.Model] {
			t.Errorf("unexpected model %q", u.Model)
		}
		if u.Backend != "anthropic" || u.BaseUrl != "https://api.anthropic.com" || u.KeyEnv != "ANTHROPIC_API_KEY" {
			t.Errorf("sibling %q did not share connection: backend=%q base=%q key=%q", u.Model, u.Backend, u.BaseUrl, u.KeyEnv)
		}
	}
}

// TestModelBackendsFetchModels exercises ctrl+f discovery: the fetched ids
// populate the model-id field so the whole connection's models become siblings.
func TestModelBackendsFetchModels(t *testing.T) {
	f := newFakeClient()
	f.discoverIDs = []string{"gpt-5.5", "gpt-4o", "o3"}
	f.discoverNote = "3 models from openai"
	m := newBackendsModel(f)
	m = runCmds(t, m, m.fetchModels)
	m = drive(t, m, "a")
	// Move focus to the model field and fetch.
	m.mbFocus = mbFieldModel
	m = drive(t, m, "ctrl+f")
	if m.lastDiscoverBackend(f) == "" {
		t.Fatal("DiscoverModels was not called")
	}
	if got := m.mbInputs[mbFieldModel].Value(); got != "gpt-5.5 gpt-4o o3" {
		t.Fatalf("model field after fetch = %q, want the discovered ids", got)
	}
	if m.mbInfo != "3 models from openai" {
		t.Fatalf("mbInfo = %q, want the discovery note", m.mbInfo)
	}
}

// TestModelBackendsEditMultiID covers editing a model and entering (or fetching)
// multiple ids: the edited model keeps its logical name for its own id, and any
// extra ids become new siblings on the same connection — instead of erroring.
func TestModelBackendsEditMultiID(t *testing.T) {
	f := newFakeClient(&v1.ModelConfig{
		Name: "claude", Backend: "anthropic",
		BaseUrl: "https://api.anthropic.com", Model: "claude-opus-4-8", KeyEnv: "ANTHROPIC_API_KEY",
	})
	m := newBackendsModel(f)
	m = runCmds(t, m, m.fetchModels)
	m = drive(t, m, "e")
	if m.mbFormMode != mbEdit {
		t.Fatalf("mbFormMode=%d, want mbEdit", m.mbFormMode)
	}
	// Simulate fetching/typing several ids (the original id plus two more).
	m.mbInputs[mbFieldModel].SetValue("claude-opus-4-8 claude-sonnet-4-5 claude-fable-5")
	m = drive(t, m, "enter")
	if m.mbErr != "" {
		t.Fatalf("unexpected error: %q", m.mbErr)
	}
	if len(f.upserts) != 3 {
		t.Fatalf("expected 3 upserts, got %d", len(f.upserts))
	}
	names := map[string]string{} // model id -> logical name
	for _, u := range f.upserts {
		names[u.Model] = u.Name
		if u.BaseUrl != "https://api.anthropic.com" || u.KeyEnv != "ANTHROPIC_API_KEY" {
			t.Errorf("sibling %q lost connection: base=%q key=%q", u.Model, u.BaseUrl, u.KeyEnv)
		}
	}
	// The edited model keeps its name for its own id; the extras are self-named.
	if names["claude-opus-4-8"] != "claude" {
		t.Errorf("edited model name = %q, want claude", names["claude-opus-4-8"])
	}
	if names["claude-sonnet-4-5"] != "claude-sonnet-4-5" || names["claude-fable-5"] != "claude-fable-5" {
		t.Errorf("extra siblings not self-named: %v", names)
	}
}

// TestModelBackendsModelPresets exercises the per-backend model-id presets
// (task 0042 nice-to-have): ctrl+n/ctrl+p cycle the suggestions on the model
// field while free-text entry is retained.
func TestModelBackendsModelPresets(t *testing.T) {
	f := newFakeClient(&v1.ModelConfig{Name: "claude", Backend: "anthropic", Model: "claude-3"})
	m := newBackendsModel(f)
	m = runCmds(t, m, m.fetchModels)
	// Blank add form: backend defaults to anthropic.
	m = drive(t, m, "a")
	// Focus the model field (name -> backend -> base_url -> model).
	m = drive(t, m, "tab")
	m = drive(t, m, "tab")
	m = drive(t, m, "tab")
	if m.mbFocus != mbFieldModel {
		t.Fatalf("focus=%d, want mbFieldModel(%d)", m.mbFocus, mbFieldModel)
	}
	anthropic := mbModelPresets["anthropic"]
	// ctrl+n selects the first preset.
	m = drive(t, m, "ctrl+n")
	if got := m.mbInputs[mbFieldModel].Value(); got != anthropic[0] {
		t.Fatalf("after ctrl+n model=%q, want %q", got, anthropic[0])
	}
	// ctrl+n again advances to the second.
	m = drive(t, m, "ctrl+n")
	if got := m.mbInputs[mbFieldModel].Value(); got != anthropic[1] {
		t.Fatalf("after 2x ctrl+n model=%q, want %q", got, anthropic[1])
	}
	// ctrl+p steps back to the first.
	m = drive(t, m, "ctrl+p")
	if got := m.mbInputs[mbFieldModel].Value(); got != anthropic[0] {
		t.Fatalf("after ctrl+p model=%q, want %q", got, anthropic[0])
	}
	// Free text is still accepted: clear and type a custom id.
	m.mbInputs[mbFieldModel].SetValue("")
	m = typeText(t, m, "my-custom-model")
	if got := m.mbInputs[mbFieldModel].Value(); got != "my-custom-model" {
		t.Fatalf("free text not retained: model=%q", got)
	}
}

// TestModelBackendsAuthPicker switches an anthropic model to subscription
// (oauth) auth via the form's auth field and verifies the choice round-trips
// (spec §13, task 0194).
func TestModelBackendsAuthPicker(t *testing.T) {
	f := newFakeClient(&v1.ModelConfig{Name: "claude", Backend: "anthropic", Model: "claude-3", KeyEnv: "ANTHROPIC_API_KEY"})
	m := newBackendsModel(f)
	m = runCmds(t, m, m.fetchModels)
	m = drive(t, m, "e") // edit; focus starts on backend
	// Move to the auth field (backend -> base_url -> model -> key_env -> auth).
	for i := 0; i < 4; i++ {
		m = drive(t, m, "tab")
	}
	if m.mbFocus != mbFieldAuth {
		t.Fatalf("focus=%d, want mbFieldAuth(%d)", m.mbFocus, mbFieldAuth)
	}
	m = drive(t, m, "right") // api key -> oauth
	if got := mbAuthList[m.mbAuthIdx]; got != "oauth" {
		t.Fatalf("after right auth=%q, want oauth", got)
	}
	m = drive(t, m, "enter")
	if f.lastUpsert == nil || f.lastUpsert.Auth != "oauth" {
		t.Fatalf("UpsertModel auth=%v, want oauth", f.lastUpsert)
	}
}

// TestModelBackendsAuthPrefillAndEditPreserved: an oauth model loads with the
// picker on oauth, and an unrelated edit keeps the auth value (no silent
// downgrade to api-key).
func TestModelBackendsAuthPrefillAndEditPreserved(t *testing.T) {
	f := newFakeClient(&v1.ModelConfig{Name: "claude", Backend: "anthropic", Model: "claude-3", Auth: "oauth"})
	m := newBackendsModel(f)
	m = runCmds(t, m, m.fetchModels)
	m = drive(t, m, "e")
	if got := mbAuthList[m.mbAuthIdx]; got != "oauth" {
		t.Fatalf("prefill auth=%q, want oauth", got)
	}
	// Edit only the model id, then save: auth must survive.
	m = drive(t, m, "tab") // base_url
	m = drive(t, m, "tab") // model
	m = typeText(t, m, "-opus")
	m = drive(t, m, "enter")
	if f.lastUpsert == nil || f.lastUpsert.Auth != "oauth" {
		t.Fatalf("UpsertModel auth=%v, want oauth preserved", f.lastUpsert)
	}
}

// TestModelBackendsAuthCodexPresets: in add mode, selecting oauth on an
// openai connection re-seeds the curated model ids with the codex catalog
// (the platform ids don't exist on the subscription backend), and switching
// back restores the platform set.
func TestModelBackendsAuthCodexPresets(t *testing.T) {
	f := newFakeClient(&v1.ModelConfig{Name: "claude", Backend: "anthropic", Model: "claude-3"})
	m := newBackendsModel(f)
	m = runCmds(t, m, m.fetchModels)
	m = drive(t, m, "a")
	m = drive(t, m, "tab")   // -> backend
	m = drive(t, m, "right") // anthropic -> openai (reseeds platform ids)
	if got := m.mbInputs[mbFieldModel].Value(); got != strings.Join(mbModelPresets["openai"], " ") {
		t.Fatalf("model ids = %q, want openai presets", got)
	}
	for i := 0; i < 4; i++ { // backend -> base_url -> model -> key_env -> auth
		m = drive(t, m, "tab")
	}
	m = drive(t, m, "right") // api key -> oauth: codex catalog
	if got := m.mbInputs[mbFieldModel].Value(); got != strings.Join(codex.Models, " ") {
		t.Fatalf("model ids = %q, want codex catalog", got)
	}
	m = drive(t, m, "left") // back to api key: platform ids restored
	if got := m.mbInputs[mbFieldModel].Value(); got != strings.Join(mbModelPresets["openai"], " ") {
		t.Fatalf("model ids = %q, want openai presets restored", got)
	}
}

// TestModelBackendsAuthSupportedBackendsOnly: the auth picker is pinned to
// api-key on backends without subscription support (ollama), and switching
// the backend to one of those resets a selected oauth back to api-key.
// Anthropic → openai keeps the selection (both support oauth).
func TestModelBackendsAuthSupportedBackendsOnly(t *testing.T) {
	f := newFakeClient(&v1.ModelConfig{Name: "claude", Backend: "anthropic", Model: "claude-3"})
	m := newBackendsModel(f)
	m = runCmds(t, m, m.fetchModels)
	m = drive(t, m, "a") // add form, backend defaults to anthropic, focus on name
	// Select oauth on the anthropic backend.
	for i := 0; i < 5; i++ { // name -> backend -> base_url -> model -> key_env -> auth
		m = drive(t, m, "tab")
	}
	if m.mbFocus != mbFieldAuth {
		t.Fatalf("focus=%d, want mbFieldAuth(%d)", m.mbFocus, mbFieldAuth)
	}
	m = drive(t, m, "right")
	if got := mbAuthList[m.mbAuthIdx]; got != "oauth" {
		t.Fatalf("auth=%q, want oauth", got)
	}
	// Back to the backend field.
	for i := 0; i < 4; i++ {
		m = drive(t, m, "shift+tab")
	}
	if m.mbFocus != mbFieldBackend {
		t.Fatalf("focus=%d, want mbFieldBackend(%d)", m.mbFocus, mbFieldBackend)
	}
	// anthropic -> openai keeps oauth (both subscription-capable).
	m = drive(t, m, "right")
	if got := mbAuthList[m.mbAuthIdx]; got != "oauth" {
		t.Fatalf("after anthropic->openai auth=%q, want oauth kept", got)
	}
	// openai -> ollama resets to the api-key default.
	m = drive(t, m, "right")
	if got := mbAuthList[m.mbAuthIdx]; got != "" {
		t.Fatalf("after switch to ollama auth=%q, want api-key default", got)
	}
	// And the picker no longer cycles while on ollama.
	for i := 0; i < 4; i++ {
		m = drive(t, m, "tab")
	}
	if m.mbFocus != mbFieldAuth {
		t.Fatalf("focus=%d, want mbFieldAuth(%d)", m.mbFocus, mbFieldAuth)
	}
	m = drive(t, m, "right")
	if got := mbAuthList[m.mbAuthIdx]; got != "" {
		t.Fatalf("auth cycled on ollama backend: %q", got)
	}
}

func TestModelBackendsRemoveValidationError(t *testing.T) {
	f := newFakeClient(
		&v1.ModelConfig{Name: "claude", Backend: "anthropic", Model: "claude-3"},
		&v1.ModelConfig{Name: "gpt", Backend: "openai", Model: "gpt-5"},
	)
	f.referenced["claude"] = true // a role still references it
	m := newBackendsModel(f)
	m = runCmds(t, m, m.fetchModels)
	// Remove the first (referenced) model.
	m.mbCursor = 0
	m = drive(t, m, "x")
	if m.mbView != 2 {
		t.Fatalf("after 'x' mbView=%d, want 2 (confirm)", m.mbView)
	}
	m = drive(t, m, "enter")
	if f.lastRemove != "claude" {
		t.Fatalf("RemoveModel got %q, want claude", f.lastRemove)
	}
	if m.mbErr == "" || !strings.Contains(m.mbErr, "referenced") {
		t.Fatalf("expected an inline validation error, got mbErr=%q", m.mbErr)
	}
	// The model is still present because removal was rejected.
	if len(m.models) != 2 {
		t.Fatalf("after rejected remove list has %d models, want 2", len(m.models))
	}

	// Removing an unreferenced model succeeds and refreshes the list.
	m.mbCursor = 1 // gpt
	m = drive(t, m, "x")
	m = drive(t, m, "enter")
	if f.lastRemove != "gpt" {
		t.Fatalf("RemoveModel got %q, want gpt", f.lastRemove)
	}
	if m.mbErr != "" {
		t.Fatalf("unexpected mbErr after successful remove: %q", m.mbErr)
	}
	if len(m.models) != 1 {
		t.Fatalf("after remove list has %d models, want 1", len(m.models))
	}
}

// Removing the last-listed model with the cursor on it must clamp mbCursor so a
// subsequent action does not index m.models out of range (task 0044 regression).
func TestModelBackendsRemoveLastClampsCursor(t *testing.T) {
	f := newFakeClient(
		&v1.ModelConfig{Name: "claude", Backend: "anthropic", Model: "claude-3"},
		&v1.ModelConfig{Name: "gpt", Backend: "openai", Model: "gpt-5"},
	)
	m := newBackendsModel(f)
	m = runCmds(t, m, m.fetchModels)
	// Put the cursor on the last entry and remove it.
	m.mbCursor = len(m.models) - 1 // "gpt"
	m = drive(t, m, "x")
	m = drive(t, m, "enter")
	if f.lastRemove != "gpt" {
		t.Fatalf("RemoveModel got %q, want gpt", f.lastRemove)
	}
	if len(m.models) != 1 {
		t.Fatalf("after remove list has %d models, want 1", len(m.models))
	}
	// Cursor must have been clamped back into range.
	if m.mbCursor >= len(m.models) {
		t.Fatalf("mbCursor=%d out of range for %d models", m.mbCursor, len(m.models))
	}
	// A subsequent action on the (now last) cursor must not panic and must target
	// the remaining model.
	m = drive(t, m, "x")
	m = drive(t, m, "enter")
	if f.lastRemove != "claude" {
		t.Fatalf("second RemoveModel got %q, want claude", f.lastRemove)
	}
	if len(m.models) != 0 {
		t.Fatalf("after second remove list has %d models, want 0", len(m.models))
	}
	if m.mbCursor != 0 {
		t.Fatalf("mbCursor=%d for empty list, want 0", m.mbCursor)
	}
	// An edit/remove on an empty list must be a no-op (no panic).
	m = drive(t, m, "e")
	m = drive(t, m, "x")
}

// TestModelBackendsListScrollsWithinTerminal guards the same overflow for the
// model-backends list card.
func TestModelBackendsListScrollsWithinTerminal(t *testing.T) {
	m := model{mbOpen: true}
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 12})
	m = updated.(model)
	for i := 0; i < 30; i++ {
		m.models = append(m.models, &v1.ModelInfo{
			Name: fmt.Sprintf("backend-%02d", i), Backend: "openai", Model: "gpt",
		})
	}
	m.mbCursor = len(m.models) - 1

	view := m.mbListView()
	lines := strings.Split(view, "\n")
	if len(lines) > 12 {
		t.Fatalf("mbListView produced %d lines for a 12-row terminal:\n%s", len(lines), view)
	}
	if !strings.Contains(view, "backend-29") {
		t.Errorf("cursor row (last) should stay visible in the window:\n%s", view)
	}
}
