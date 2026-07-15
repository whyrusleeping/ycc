package setup

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/whyrusleeping/ycc/internal/anthropicauth"
	"github.com/whyrusleeping/ycc/internal/codex"
	"github.com/whyrusleeping/ycc/internal/config"
	"github.com/whyrusleeping/ycc/internal/openaiauth"
	"github.com/whyrusleeping/ycc/internal/secrets"
)

// step is the wizard's current screen.
type step int

const (
	stepProvider step = iota // editing the current provider's fields
	stepLogin                // Claude subscription OAuth login (oauth auth, no stored creds)
	stepVerify               // testing the just-entered provider's connection
	stepAddMore              // add another provider or continue
	stepRoles                // assign coordinator/implementer/reviewers
	stepDone                 // terminal
)

// provider field indices (focus order). backend and auth are focusable
// non-text fields cycled with ←/→.
const (
	fieldName = iota
	fieldBackend
	fieldBaseURL
	fieldModel
	fieldAuth
	fieldKeyEnv
	fieldKey
	numFields
)

// authList mirrors config.Model.Auth (spec §13): "" is the api-key default,
// "oauth" authenticates the anthropic backend with a Claude subscription.
var authList = []string{"", "oauth"}

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
	inputs      [numFields]textinput.Model // name, base_url, model, key_env, key (backend/auth slots unused)
	backendIdx  int                        // index into backends
	authIdx     int                        // index into authList (anthropic-only; pinned to 0 elsewhere)
	focus       int                        // current field index
	editErr     string
	editInfo    string   // inline info line (e.g. "N models fetched")
	fetchedIDs  []string // ids from the last successful ctrl+f discovery
	cycleIdx    int      // cursor into the id cycle source (ctrl+n/p)
	discovering bool     // a ctrl+f discovery is in flight

	// stepLogin (subscription OAuth, spec §13): entered from the provider
	// editor when auth=oauth and no credentials are stored yet. Two modes:
	// anthropic uses a paste-code flow (loginInput), openai a browser flow
	// with a local callback server (waiting screen; openaiLoginMsg resolves).
	loginInput  textinput.Model
	loginPKCE   anthropicauth.PKCE
	loginURL    string
	loginErr    string
	loginWait   bool               // openai browser flow in flight (no input; esc cancels)
	loginCancel context.CancelFunc // cancels the in-flight openai login
	// injectable for tests: code exchange (anthropic), browser login
	// (openai), stored-credentials probes, browser open
	exchange      func(code string, p anthropicauth.PKCE) (*anthropicauth.Credentials, error)
	openaiLogin   func(ctx context.Context, onURL func(string)) (*openaiauth.Credentials, error)
	hasCreds      func() bool // anthropic credentials stored?
	hasOpenAICred func() bool // openai credentials stored?
	openURL       func(string)

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
	m.loginInput = textinput.New()
	m.loginInput.Placeholder = "paste the code shown after login (code#state)"
	m.loginInput.CharLimit = 500
	m.loginInput.SetWidth(50)
	m.cycleIdx = -1
	m.verify = realVerify
	m.discover = realDiscover
	m.exchange = realExchange
	m.openaiLogin = openaiauth.Login
	m.hasCreds = func() bool { _, ok := anthropicauth.Load(); return ok }
	m.hasOpenAICred = func() bool { _, ok := openaiauth.Load(); return ok }
	m.openURL = openBrowser
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
// timeout. A nil error means the credentials + base_url reach the backend. For
// a subscription-authenticated provider the stored OAuth access token is used
// instead of an API key; openai subscriptions (codex backend) have no listing
// endpoint at all, so verification is a stored-credentials check.
func realVerify(p provider) error {
	if p.auth == "oauth" && p.backend == "openai" {
		if _, ok := openaiauth.Load(); !ok {
			return fmt.Errorf("no ChatGPT subscription credentials stored; run `ycc login openai`")
		}
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := config.DiscoverModels(ctx, p.backend, p.baseURL, providerKey(ctx, p))
	return err
}

// realDiscover lists model ids for a provider (ctrl+f in the editor). The
// codex backend has no listing endpoint; its curated model set is returned.
func realDiscover(p provider) ([]string, error) {
	if p.auth == "oauth" && p.backend == "openai" {
		return append([]string(nil), codex.Models...), nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return config.DiscoverModels(ctx, p.backend, p.baseURL, providerKey(ctx, p))
}

// providerKey resolves the credential DiscoverModels should present: the OAuth
// access token for subscription auth, the api key otherwise.
func providerKey(ctx context.Context, p provider) string {
	if p.auth == "oauth" {
		tok, err := anthropicauth.AccessToken(ctx)
		if err != nil {
			return ""
		}
		return tok
	}
	return resolveKey(p)
}

// realExchange trades a pasted login code for credentials (stepLogin).
func realExchange(code string, p anthropicauth.PKCE) (*anthropicauth.Credentials, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return anthropicauth.Exchange(ctx, code, p)
}

// openBrowser makes a best-effort attempt to open url in the user's browser;
// failures are silent (the URL is always shown for manual opening).
func openBrowser(url string) {
	var c *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		c = exec.Command("open", url)
	case "windows":
		c = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		c = exec.Command("xdg-open", url)
	}
	_ = c.Start()
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
	// Subscription (oauth) auth is anthropic/openai-only: leaving those
	// backends resets the auth picker to the api-key default.
	if !oauthBackend(b) {
		m.authIdx = 0
	}
	// A backend change invalidates any previously-fetched id list.
	m.fetchedIDs = nil
	m.cycleIdx = -1
}

// oauthBackend reports whether a backend supports subscription (oauth) auth.
// Mirrors config.Model.validateAuth.
func oauthBackend(b string) bool { return b == "anthropic" || b == "openai" }

func (m *model) resetProviderEditor() {
	for i := range m.inputs {
		m.inputs[i].SetValue("")
		m.inputs[i].Blur()
	}
	m.backendIdx = 0
	m.authIdx = 0
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
		auth:    authList[m.authIdx],
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
	case openaiLoginURLMsg:
		if m.step == stepLogin && m.loginWait {
			m.loginURL = msg.url
		}
		return m, nil
	case openaiLoginMsg:
		// Ignore a stale result (user esc'd back to the editor already).
		if m.step != stepLogin || !m.loginWait {
			return m, nil
		}
		m.loginWait = false
		if m.loginCancel != nil {
			m.loginCancel()
			m.loginCancel = nil
		}
		if msg.err != nil {
			// Show the failure on the login screen; esc returns to the editor.
			m.loginErr = msg.err.Error()
			return m, nil
		}
		if err := openaiauth.Save(msg.creds); err != nil {
			m.loginErr = "storing credentials: " + err.Error()
			return m, nil
		}
		m.step = stepVerify
		m.verifying = true
		m.verifyDone = false
		m.verifyErr = nil
		return m, m.verifyCmd(m.candidate)
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
		case "ctrl+c":
			m.skipped = true
			return m, tea.Quit
		case "esc":
			// In the login step esc backs out to the provider editor (the
			// user may want to switch auth back to api-key); everywhere else
			// it skips the wizard.
			if m.step == stepLogin {
				if m.loginCancel != nil {
					m.loginCancel()
					m.loginCancel = nil
				}
				m.loginWait = false
				m.step = stepProvider
				m.loginErr = ""
				m.focusField(m.focus)
				return m, nil
			}
			m.skipped = true
			return m, tea.Quit
		}
		switch m.step {
		case stepProvider:
			return m.updateProvider(msg)
		case stepLogin:
			return m.updateLogin(msg)
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
		m.cycleAuth(-1)
		return m, nil
	case "right":
		if m.focus == fieldBackend {
			prev := backends[m.backendIdx]
			m.backendIdx = (m.backendIdx + 1) % len(backends)
			m.applyBackendDefaults(prev)
		}
		m.cycleAuth(1)
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
		// Subscription auth without stored credentials: run the OAuth login
		// first (spec §13); verification then uses the fresh credentials.
		if p.auth == "oauth" && p.backend == "anthropic" && !m.hasCreds() {
			pkce, err := anthropicauth.NewPKCE()
			if err != nil {
				m.editErr = "oauth: " + err.Error()
				return m, nil
			}
			m.loginPKCE = pkce
			m.loginURL = anthropicauth.AuthorizeURL(pkce)
			m.loginErr = ""
			m.loginWait = false
			m.loginInput.SetValue("")
			m.loginInput.Focus()
			m.step = stepLogin
			m.openURL(m.loginURL)
			return m, nil
		}
		if p.auth == "oauth" && p.backend == "openai" && !m.hasOpenAICred() {
			return m.startOpenAILogin()
		}
		m.step = stepVerify
		m.verifying = true
		m.verifyDone = false
		m.verifyErr = nil
		return m, m.verifyCmd(p)
	}
	// text editing on the focused text field
	if m.focus != fieldBackend && m.focus != fieldAuth {
		var cmd tea.Cmd
		m.inputs[m.focus], cmd = m.inputs[m.focus].Update(key)
		return m, cmd
	}
	return m, nil
}

// cycleAuth cycles the auth picker when it is focused. Subscription (oauth)
// auth is anthropic/openai-only; on other backends the picker is pinned to
// api-key. Switching an openai provider to oauth re-seeds a still-default
// model id with the codex backend's default (the platform catalog does not
// apply to subscription inference), and switching back restores it.
func (m *model) cycleAuth(d int) {
	if m.focus != fieldAuth {
		return
	}
	b := backends[m.backendIdx]
	if !oauthBackend(b) {
		return
	}
	m.authIdx = (m.authIdx + d + len(authList)) % len(authList)
	if b == "openai" {
		cur := m.inputs[fieldModel].Value()
		if authList[m.authIdx] == "oauth" && cur == defaultModel(b) {
			m.inputs[fieldModel].SetValue(codex.Models[0])
		} else if authList[m.authIdx] == "" && cur == codex.Models[0] {
			m.inputs[fieldModel].SetValue(defaultModel(b))
		}
		m.fetchedIDs = nil
		m.cycleIdx = -1
	}
}

// openaiLoginURLMsg carries the authorize URL once the browser flow binds its
// callback server (displayed on the waiting screen).
type openaiLoginURLMsg struct{ url string }

// openaiLoginMsg is the terminal result of the openai browser login flow.
type openaiLoginMsg struct {
	creds *openaiauth.Credentials
	err   error
}

// startOpenAILogin kicks off the ChatGPT browser OAuth flow (spec §13): a
// local callback server waits for the browser redirect while the wizard shows
// a waiting screen. Two commands run: one relays the authorize URL to the UI,
// one resolves with the exchanged credentials (or error). Esc cancels via
// loginCancel.
func (m model) startOpenAILogin() (tea.Model, tea.Cmd) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	m.loginCancel = cancel
	m.loginWait = true
	m.loginErr = ""
	m.loginURL = ""
	m.step = stepLogin
	urlCh := make(chan string, 1)
	login := m.openaiLogin
	openURL := m.openURL
	waitURL := func() tea.Msg { return openaiLoginURLMsg{url: <-urlCh} }
	run := func() tea.Msg {
		creds, err := login(ctx, func(u string) {
			select {
			case urlCh <- u:
			default:
			}
			if openURL != nil {
				openURL(u)
			}
		})
		// Unblock waitURL if the flow failed before surfacing a URL.
		select {
		case urlCh <- "":
		default:
		}
		return openaiLoginMsg{creds: creds, err: err}
	}
	return m, tea.Batch(waitURL, run)
}

// updateLogin drives the subscription login screen. Anthropic mode: the user
// pastes the code shown after browser login; Enter exchanges it (persisting
// credentials) and proceeds to verification. OpenAI mode (loginWait): the
// browser flow completes on its own; keys are ignored while waiting. Esc
// (handled globally) cancels/returns to the editor in both modes.
func (m model) updateLogin(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.loginWait {
		return m, nil
	}
	if key.String() == "enter" {
		code := strings.TrimSpace(m.loginInput.Value())
		if code == "" {
			m.loginErr = "paste the code shown after logging in"
			return m, nil
		}
		creds, err := m.exchange(code, m.loginPKCE)
		if err != nil {
			m.loginErr = err.Error()
			return m, nil
		}
		if err := anthropicauth.Save(creds); err != nil {
			m.loginErr = "storing credentials: " + err.Error()
			return m, nil
		}
		m.loginErr = ""
		m.step = stepVerify
		m.verifying = true
		m.verifyDone = false
		m.verifyErr = nil
		return m, m.verifyCmd(m.candidate)
	}
	var cmd tea.Cmd
	m.loginInput, cmd = m.loginInput.Update(key)
	return m, cmd
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
	if i != fieldBackend && i != fieldAuth {
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
		oauth := authList[m.authIdx] == "oauth"
		labels := [numFields]string{"name", "backend", "base url", "model", "auth", "key env", "api key"}
		for i := 0; i < numFields; i++ {
			cursor := "  "
			if m.focus == i {
				cursor = "▸ "
			}
			switch i {
			case fieldBackend:
				b.WriteString(fmt.Sprintf("%s%-9s ◂ %s ▸\n", cursor, labels[i]+":", backends[m.backendIdx]))
			case fieldAuth:
				if oauthBackend(backends[m.backendIdx]) {
					show := "api key"
					if oauth {
						show = "oauth (Claude subscription)"
						if backends[m.backendIdx] == "openai" {
							show = "oauth (ChatGPT subscription)"
						}
					}
					b.WriteString(fmt.Sprintf("%s%-9s ◂ %s ▸\n", cursor, labels[i]+":", show))
				} else {
					b.WriteString(fmt.Sprintf("%s%-9s %s\n", cursor, labels[i]+":", dimStyle.Render("api key  (oauth is anthropic/openai-only)")))
				}
			default:
				line := m.inputs[i].View()
				if oauth && (i == fieldKeyEnv || i == fieldKey) {
					line += "  " + dimStyle.Render("(unused with oauth)")
				}
				b.WriteString(fmt.Sprintf("%s%-9s %s\n", cursor, labels[i]+":", line))
			}
			if i == fieldAuth && m.focus == fieldAuth && oauth {
				b.WriteString("   " + dimStyle.Render("Enter runs a browser login if no subscription credentials are stored yet") + "\n")
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
	case stepLogin:
		if m.loginWait {
			b.WriteString(titleStyle.Render("ycc first-run setup — log in with your ChatGPT subscription"))
			b.WriteString("\n\n")
			if m.loginURL != "" {
				b.WriteString("Open this URL in your browser and log in (it may have opened already):\n\n")
				b.WriteString("  " + m.loginURL + "\n\n")
			}
			if m.loginErr != "" {
				b.WriteString(errStyle.Render(m.loginErr) + "\n\n")
				b.WriteString(dimStyle.Render("Esc back to editor"))
			} else {
				b.WriteString(dimStyle.Render("waiting for the browser to complete login (callback on localhost:1455)…"))
				b.WriteString("\n\n" + dimStyle.Render("Esc cancel"))
			}
			break
		}
		b.WriteString(titleStyle.Render("ycc first-run setup — log in with your Claude subscription"))
		b.WriteString("\n\n")
		b.WriteString("Open this URL in your browser and log in (it may have opened already):\n\n")
		b.WriteString("  " + m.loginURL + "\n\n")
		b.WriteString("code: " + m.loginInput.View() + "\n")
		if m.loginErr != "" {
			b.WriteString("\n" + errStyle.Render(m.loginErr) + "\n")
		}
		b.WriteString("\n" + dimStyle.Render("Enter exchange code · Esc back to editor"))
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
