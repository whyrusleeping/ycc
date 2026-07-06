package docs

import (
	"regexp"
	"strings"
)

// TaskBody assembles a backlog task file body from a caller-supplied
// description. Agents (and CLI users) often pass fully-structured markdown that
// already contains its own "## Description" / "## Acceptance criteria" headers,
// so each canonical section header is only added when the description does not
// already provide it — otherwise the header would be duplicated. An empty
// description yields an empty body (the Store then scaffolds a blank template).
func TaskBody(desc string) string {
	if strings.TrimSpace(desc) == "" {
		return ""
	}
	var b strings.Builder
	if !startsWithDescriptionHeader(desc) {
		b.WriteString("## Description\n")
	}
	b.WriteString(desc)
	if !hasHeaderLine(desc, "acceptance criteria") {
		b.WriteString("\n\n## Acceptance criteria")
	}
	if !hasHeaderLine(desc, "work log") {
		b.WriteString("\n\n## Work log\n")
	}
	return b.String()
}

// startsWithDescriptionHeader reports whether the first non-empty line of desc
// is a "## Description" header (case-insensitive, tolerating leading whitespace).
func startsWithDescriptionHeader(desc string) bool {
	for _, line := range strings.Split(desc, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		return headerLineRe.MatchString(trimmed) &&
			strings.EqualFold(headerTitle(trimmed), "description")
	}
	return false
}

// hasHeaderLine reports whether desc contains a "## <title>" header on its own
// line (case-insensitive on the title, tolerating surrounding whitespace).
func hasHeaderLine(desc, title string) bool {
	for _, line := range strings.Split(desc, "\n") {
		trimmed := strings.TrimSpace(line)
		if headerLineRe.MatchString(trimmed) && strings.EqualFold(headerTitle(trimmed), title) {
			return true
		}
	}
	return false
}

var headerLineRe = regexp.MustCompile(`^##\s+\S`)

// headerTitle returns the trimmed text of a "## ..." header line.
func headerTitle(line string) string {
	return strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "##"))
}
