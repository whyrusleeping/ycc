package llmhttp

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
)

// CheckRedirect confines credentials (including custom headers and OAuth POST
// bodies) to the original origin. It retains net/http's default ten-hop limit.
func CheckRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return errors.New("stopped after 10 redirects")
	}
	if len(via) == 0 {
		return nil
	}
	original := via[0].URL
	if original.Scheme == "https" && req.URL.Scheme == "http" {
		return errors.New("credential redirect: HTTPS downgrade refused")
	}
	if !strings.EqualFold(original.Scheme, req.URL.Scheme) || !strings.EqualFold(original.Hostname(), req.URL.Hostname()) || originPort(original) != originPort(req.URL) {
		return errors.New("credential redirect: cross-origin redirect refused")
	}
	return nil
}

func originPort(u *url.URL) string {
	if p := u.Port(); p != "" {
		return p
	}
	if strings.EqualFold(u.Scheme, "https") {
		return "443"
	}
	return "80"
}

// Redact removes the request's own non-empty credentials, not arbitrary secrets
// in text. Credentials of at least eight bytes are replaced anywhere. Shorter
// credentials match only whole tokens, bounded by characters other than ASCII
// letters, digits, '_' and '-', to preserve provider codes used for classification.
func Redact(text string, secrets ...string) string {
	for _, secret := range secrets {
		switch {
		case secret == "":
			continue
		case len(secret) >= 8:
			text = strings.ReplaceAll(text, secret, "[REDACTED]")
		default:
			text = redactToken(text, secret)
		}
	}
	return text
}

func redactToken(text, secret string) string {
	var out strings.Builder
	start := 0
	for pos := 0; pos < len(text); {
		i := strings.Index(text[pos:], secret)
		if i < 0 {
			break
		}
		i += pos
		end := i + len(secret)
		if (i == 0 || !tokenByte(text[i-1])) && (end == len(text) || !tokenByte(text[end])) {
			out.WriteString(text[start:i])
			out.WriteString("[REDACTED]")
			start, pos = end, end
		} else {
			pos = i + 1
		}
	}
	out.WriteString(text[start:])
	return out.String()
}

func tokenByte(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || b == '_' || b == '-'
}

// RedactError preserves the error chain for classification and cancellation.
func RedactError(err error, secrets ...string) error {
	if err == nil {
		return nil
	}
	text := Redact(err.Error(), secrets...)
	if text == err.Error() {
		return err
	}
	return &redactedError{error: err, text: text}
}

type redactedError struct {
	error
	text string
}

func (e *redactedError) Error() string { return e.text }
func (e *redactedError) Unwrap() error { return e.error }
