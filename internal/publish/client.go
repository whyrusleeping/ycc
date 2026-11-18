// Package publish uploads project files to a ycc-share server and remembers
// what was published (docs/design/publishing.md). The daemon is the share
// server's only writer; clients never see its token.
package publish

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/whyrusleeping/ycc/internal/llmhttp"
)

// Errors returned by Client for share server responses.
var (
	ErrUnauthorized = errors.New("the share server rejected the upload token")
	ErrTooLarge     = errors.New("the share server refused the file as too large")
	ErrNotFound     = errors.New("the share server has no such item")
)

// requestTimeout bounds one upload or delete, including the body transfer.
const requestTimeout = 2 * time.Minute

// Item is the share server's description of an upload.
type Item struct {
	ID        string
	URL       string
	Size      int64
	SHA256    string
	ExpiresAt time.Time // zero: never
}

// Client talks to one share server.
type Client struct {
	base  string // without trailing slash
	token string
	http  *http.Client
}

// NewClient returns a client for the server at baseURL authenticating with token.
func NewClient(baseURL, token string) *Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if strings.HasPrefix(strings.ToLower(baseURL), "http:") {
		// Config admits clear HTTP only for loopback; never hand that
		// request (and its token) to an environment proxy.
		transport.Proxy = nil
	}
	return &Client{
		base:  strings.TrimRight(baseURL, "/"),
		token: token,
		http: &http.Client{
			Transport: transport,
			Timeout:   requestTimeout,
			// A redirect could carry the token elsewhere or turn the POST
			// into a GET; the configured URL must be the API origin.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
}

// Upload stores data under the display name and returns the new item.
func (c *Client) Upload(ctx context.Context, name string, data []byte) (Item, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/api/items", bytes.NewReader(data))
	if err != nil {
		return Item{}, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("X-Ycc-Filename", url.PathEscape(name))
	resp, err := c.http.Do(req)
	if err != nil {
		return Item{}, c.transportErr(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return Item{}, c.statusErr(resp)
	}
	var body struct {
		ID        string `json:"id"`
		URL       string `json:"url"`
		Size      int64  `json:"size"`
		SHA256    string `json:"sha256"`
		ExpiresAt string `json:"expires_at"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&body); err != nil {
		return Item{}, fmt.Errorf("share server: decode upload response: %w", err)
	}
	if body.ID == "" || body.URL == "" {
		return Item{}, fmt.Errorf("share server: upload response is missing id or url")
	}
	it := Item{ID: body.ID, URL: body.URL, Size: body.Size, SHA256: body.SHA256}
	if body.ExpiresAt != "" {
		t, err := time.Parse(time.RFC3339, body.ExpiresAt)
		if err != nil {
			return Item{}, fmt.Errorf("share server: unparseable expires_at in upload response")
		}
		it.ExpiresAt = t
	}
	return it, nil
}

// Delete removes an item. An unknown or expired item is ErrNotFound.
func (c *Client) Delete(ctx context.Context, id string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.base+"/api/items/"+url.PathEscape(id), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.http.Do(req)
	if err != nil {
		return c.transportErr(err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusNoContent, http.StatusOK:
		return nil
	case http.StatusNotFound:
		return ErrNotFound
	}
	return c.statusErr(resp)
}

// UnavailableError is a network-level failure to reach the share server.
type UnavailableError struct{ Err error }

func (e *UnavailableError) Error() string { return "share server unreachable: " + e.Err.Error() }
func (e *UnavailableError) Unwrap() error { return e.Err }

func (c *Client) transportErr(err error) error {
	return &UnavailableError{Err: errors.New(llmhttp.Redact(err.Error(), c.token))}
}

func (c *Client) statusErr(resp *http.Response) error {
	switch resp.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return ErrUnauthorized
	case http.StatusRequestEntityTooLarge:
		return ErrTooLarge
	}
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	text := strings.TrimSpace(string(msg))
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		text = "redirected to " + resp.Header.Get("Location") + "; set publish.url to the server's final address"
	}
	out := "share server: " + resp.Status
	if text != "" {
		out += ": " + text
	}
	return errors.New(llmhttp.Redact(out, c.token))
}
