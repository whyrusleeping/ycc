package secrets

import (
	"regexp"
	"strings"
)

// obviousCredentialPatterns intentionally covers only high-confidence shapes.
// This is a warning guardrail, not a promise to recognize or redact arbitrary
// credentials; broad entropy/regex filtering would corrupt ordinary prompts.
var obviousCredentialPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\b(?:api[_ -]?key|access[_ -]?token|bearer[_ -]?token|password|client[_ -]?secret)\b\s*(?:is\s+|[:=]\s*)["']?[A-Za-z0-9_./+=:-]{12,}`),
	regexp.MustCompile(`\b(?:sk-(?:ant-|proj-)?|gh[pousr]_|xox[baprs]-|AKIA)[A-Za-z0-9_=-]{12,}`),
}

// LooksLikeCredential reports whether text appears to contain an obvious raw
// credential. It also recognizes exact values already in the local store. The
// caller should warn/refuse before recording text, rather than modify replay.
func LooksLikeCredential(text string) bool {
	if strings.TrimSpace(text) == "" {
		return false
	}
	if s, err := Load(); err == nil {
		for _, value := range s.Tokens {
			if len(value) >= 8 && strings.Contains(text, value) {
				return true
			}
		}
	}
	for _, pattern := range obviousCredentialPatterns {
		if pattern.MatchString(text) {
			return true
		}
	}
	return false
}

// RedactForPresentation applies exact stored-value and high-confidence pattern
// redaction to an export/debug presentation. Durable events and replay must keep
// their original bytes; callers should visibly disclose when changed is true.
func RedactForPresentation(text string) (redacted string, changed bool) {
	redacted, changed = RedactKnown(text)
	for _, pattern := range obviousCredentialPatterns {
		next := pattern.ReplaceAllString(redacted, "[REDACTED POSSIBLE CREDENTIAL]")
		if next != redacted {
			changed = true
			redacted = next
		}
	}
	return redacted, changed
}
