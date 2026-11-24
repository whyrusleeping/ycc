package antigravity

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/whyrusleeping/ycc/internal/googleauth"
)

// clientMetadata identifies the caller to loadCodeAssist/onboardUser the way
// the Antigravity clients do.
var clientMetadata = map[string]any{"ideType": "ANTIGRAVITY"}

// projectRef accepts cloudaicompanionProject as either a string or {id}.
type projectRef string

func (p *projectRef) UnmarshalJSON(b []byte) error {
	var s string
	if json.Unmarshal(b, &s) == nil {
		*p = projectRef(s)
		return nil
	}
	var o struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(b, &o); err != nil {
		return nil // unknown shape: treat as absent
	}
	*p = projectRef(o.ID)
	return nil
}

type tierInfo struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	IsDefault bool   `json:"isDefault"`
}

type loadCodeAssistResponse struct {
	Project         projectRef `json:"cloudaicompanionProject"`
	CurrentTier     *tierInfo  `json:"currentTier"`
	PaidTier        *tierInfo  `json:"paidTier"`
	AllowedTiers    []tierInfo `json:"allowedTiers"`
	IneligibleTiers []struct {
		ReasonCode    string `json:"reasonCode"`
		ReasonMessage string `json:"reasonMessage"`
		TierID        string `json:"tierId"`
		ValidationURL string `json:"validationUrl"`
	} `json:"ineligibleTiers"`
}

type operation struct {
	Name     string `json:"name"`
	Done     bool   `json:"done"`
	Response struct {
		Project projectRef `json:"cloudaicompanionProject"`
	} `json:"response"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// onboardPoll bounds onboarding: Google's long-running operation normally
// finishes within seconds.
var (
	onboardPollInterval = 3 * time.Second
	onboardPollLimit    = 20
)

// projectFor returns the Code Assist project for tok's login, discovering and
// persisting it on first use. A non-nil response is a terminal failure to
// return to gollama as-is (an ineligible account or a provider error).
func (t *Transport) projectFor(ctx context.Context, req *http.Request, tok googleauth.Token) (string, *http.Response, error) {
	if tok.ProjectID != "" {
		return tok.ProjectID, nil, nil
	}
	t.mu.Lock()
	if t.project != "" && t.projFor == tok.RefreshToken {
		p := t.project
		t.mu.Unlock()
		return p, nil, nil
	}
	t.mu.Unlock()

	var lr loadCodeAssistResponse
	resp, err := t.postJSON(ctx, tok, "/v1internal:loadCodeAssist", map[string]any{"metadata": clientMetadata}, &lr)
	if err != nil || resp != nil {
		return "", resp, err
	}
	project := string(lr.Project)
	if project == "" {
		if lr.CurrentTier == nil && len(lr.IneligibleTiers) > 0 {
			return "", ineligible(req, lr), nil
		}
		project, resp, err = t.onboard(ctx, req, tok, onboardTier(lr))
		if err != nil || resp != nil {
			return "", resp, err
		}
	}
	t.mu.Lock()
	t.project, t.projFor = project, tok.RefreshToken
	t.mu.Unlock()
	if t.store != nil {
		_ = t.store.SetProject(tok.RefreshToken, project)
	}
	return project, nil, nil
}

func ineligible(req *http.Request, lr loadCodeAssistResponse) *http.Response {
	var parts []string
	for _, it := range lr.IneligibleTiers {
		msg := strings.TrimSpace(it.ReasonMessage)
		if msg == "" {
			msg = it.ReasonCode
		}
		if it.ValidationURL != "" {
			msg += " (" + it.ValidationURL + ")"
		}
		if msg != "" {
			parts = append(parts, msg)
		}
	}
	return googleError(req, http.StatusForbidden, "PERMISSION_DENIED",
		"this Google account is not eligible for Antigravity: "+strings.Join(parts, "; "))
}

// onboardTier picks the tier to onboard into: the default allowed tier, else
// the first allowed one, else free-tier.
func onboardTier(lr loadCodeAssistResponse) string {
	for _, tier := range lr.AllowedTiers {
		if tier.IsDefault && tier.ID != "" {
			return tier.ID
		}
	}
	if len(lr.AllowedTiers) > 0 && lr.AllowedTiers[0].ID != "" {
		return lr.AllowedTiers[0].ID
	}
	if lr.CurrentTier != nil && lr.CurrentTier.ID != "" {
		return lr.CurrentTier.ID
	}
	return "free-tier"
}

// onboard runs onboardUser and polls its long-running operation until it
// yields a project.
func (t *Transport) onboard(ctx context.Context, req *http.Request, tok googleauth.Token, tier string) (string, *http.Response, error) {
	var op operation
	resp, err := t.postJSON(ctx, tok, "/v1internal:onboardUser", map[string]any{"tierId": tier, "metadata": clientMetadata}, &op)
	if err != nil || resp != nil {
		return "", resp, err
	}
	for i := 0; !op.Done && op.Name != "" && i < onboardPollLimit; i++ {
		select {
		case <-ctx.Done():
			return "", nil, ctx.Err()
		case <-time.After(onboardPollInterval):
		}
		resp, err := t.getJSON(ctx, tok, "/v1internal/"+strings.TrimPrefix(op.Name, "/"), &op)
		if err != nil || resp != nil {
			return "", resp, err
		}
	}
	if op.Error != nil {
		return "", googleError(req, http.StatusForbidden, "PERMISSION_DENIED", "Antigravity onboarding failed: "+op.Error.Message), nil
	}
	if !op.Done || op.Response.Project == "" {
		return "", nil, fmt.Errorf("antigravity onboarding did not return a project")
	}
	return string(op.Response.Project), nil, nil
}

func (t *Transport) getJSON(ctx context.Context, tok googleauth.Token, path string, out any) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, t.baseURL+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Accept", "application/json")
	resp, err := t.base.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return t.inspectError(resp, tok), nil
	}
	defer resp.Body.Close()
	return nil, json.NewDecoder(resp.Body).Decode(out)
}

// catalogTTL bounds how long a fetched model catalog is trusted.
const catalogTTL = time.Hour

// thinkingSuffixes are the level suffixes Antigravity model ids carry.
var thinkingSuffixes = []string{"-low", "-medium", "-high", "-xhigh"}

// wireModel maps the configured model id (e.g. gemini-4-neon) and the
// request's thinkingLevel to the Antigravity id (gemini-4-neon-high). An id
// that already carries a level, or that the catalog lists bare, is used as
// is; without a catalog, Gemini 4 ids get the level suffix.
//
// A 401/403 from the catalog (an expired grant or a suspension) is returned
// as the terminal response instead of proceeding to inference.
func (t *Transport) wireModel(ctx context.Context, tok googleauth.Token, project, model, thinkingLevel string) (string, *http.Response, error) {
	model = strings.TrimPrefix(model, "models/")
	for _, s := range thinkingSuffixes {
		if strings.HasSuffix(model, s) {
			return model, nil, nil
		}
	}
	level := strings.ToLower(thinkingLevel)
	if level == "" {
		level = "high"
	}
	suffixed := model + "-" + level
	catalog, failure := t.models(ctx, tok, project)
	if failure != nil {
		return "", failure, nil
	}
	switch {
	case catalog == nil:
		if strings.HasPrefix(model, "gemini-4-") {
			return suffixed, nil, nil
		}
		return model, nil, nil
	case catalog[suffixed]:
		return suffixed, nil, nil
	default:
		return model, nil, nil
	}
}

// models returns the login's model catalog (cached), or nil when it could
// not be fetched. An auth failure is returned as the response to stop on.
func (t *Transport) models(ctx context.Context, tok googleauth.Token, project string) (map[string]bool, *http.Response) {
	t.mu.Lock()
	if t.catalog != nil && t.catFor == tok.RefreshToken && time.Since(t.catTime) < catalogTTL {
		c := t.catalog
		t.mu.Unlock()
		return c, nil
	}
	t.mu.Unlock()
	ids, failure, err := t.fetchModels(ctx, tok, project)
	if failure != nil {
		if failure.StatusCode == http.StatusUnauthorized || failure.StatusCode == http.StatusForbidden {
			return nil, failure
		}
		failure.Body.Close()
		return nil, nil
	}
	if err != nil || len(ids) == 0 {
		return nil, nil
	}
	catalog := make(map[string]bool, len(ids))
	for _, id := range ids {
		catalog[id] = true
	}
	t.mu.Lock()
	t.catalog, t.catFor, t.catTime = catalog, tok.RefreshToken, time.Now()
	t.mu.Unlock()
	return catalog, nil
}

// fetchModels calls fetchAvailableModels, whose models member maps each wire
// id to its catalog row. Disabled and internal rows are skipped. A non-200
// reply is returned as the response.
func (t *Transport) fetchModels(ctx context.Context, tok googleauth.Token, project string) ([]string, *http.Response, error) {
	var out struct {
		Models map[string]struct {
			Disabled   bool `json:"disabled"`
			IsInternal bool `json:"isInternal"`
		} `json:"models"`
	}
	resp, err := t.postJSON(ctx, tok, "/v1internal:fetchAvailableModels", map[string]any{"project": project}, &out)
	if err != nil || resp != nil {
		return nil, resp, err
	}
	var ids []string
	for id, row := range out.Models {
		if id != "" && !row.Disabled && !row.IsInternal {
			ids = append(ids, id)
		}
	}
	return ids, nil, nil
}

// ListModels returns the model ids the logged-in account can use, for the
// settings model picker. It resolves (and persists) the account's project.
func ListModels(ctx context.Context, baseURL string, httpClient *http.Client, tokens TokenSource) ([]string, error) {
	return listModels(ctx, newTransport(baseURL, httpClient, tokens, googleStore{}))
}

func listModels(ctx context.Context, t *Transport) ([]string, error) {
	tokens := t.tokens
	tok, err := tokens(ctx)
	if err != nil {
		return nil, err
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, clientBase, nil)
	project, failure, err := t.projectFor(ctx, req, tok)
	if err != nil {
		return nil, err
	}
	if failure != nil {
		failure.Body.Close()
		return nil, fmt.Errorf("resolving the Antigravity project: %s", failure.Status)
	}
	ids, failure, err := t.fetchModels(ctx, tok, project)
	if failure != nil {
		failure.Body.Close()
		return nil, fmt.Errorf("listing Antigravity models: %s", failure.Status)
	}
	return ids, err
}
