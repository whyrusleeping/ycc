package secrets

import "regexp"

// obviousCredentialPatterns intentionally covers only high-confidence shapes.
// It drives best-effort presentation redaction, not a promise to recognize or
// redact arbitrary credentials; broad entropy/regex filtering would corrupt
// ordinary text.
var obviousCredentialPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\b(?:api[_ -]?key|access[_ -]?token|bearer[_ -]?token|password|client[_ -]?secret)\b\s*(?:is\s+|[:=]\s*)["']?[A-Za-z0-9_./+=:-]{12,}`),
	regexp.MustCompile(`\b(?:sk-(?:ant-|proj-)?|gh[pousr]_|xox[baprs]-|AKIA)[A-Za-z0-9_=-]{12,}`),
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
