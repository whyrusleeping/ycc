package docs

import (
	"regexp"
	"testing"
)

func countHeaders(body, title string) int {
	re := regexp.MustCompile(`(?im)^\s*##\s+` + regexp.QuoteMeta(title) + `\s*$`)
	return len(re.FindAllString(body, -1))
}

func TestTaskBodyPlainText(t *testing.T) {
	body := TaskBody("Do the thing and make it work.")
	if got := countHeaders(body, "Description"); got != 1 {
		t.Fatalf("Description headers = %d, want 1\nbody:\n%s", got, body)
	}
	if got := countHeaders(body, "Acceptance criteria"); got != 1 {
		t.Fatalf("Acceptance criteria headers = %d, want 1\nbody:\n%s", got, body)
	}
	if got := countHeaders(body, "Work log"); got != 1 {
		t.Fatalf("Work log headers = %d, want 1\nbody:\n%s", got, body)
	}
}

func TestTaskBodyPreStructured(t *testing.T) {
	desc := "## Description\nThe feature does X.\n\n## Acceptance criteria\n- [ ] X works\n"
	body := TaskBody(desc)
	if got := countHeaders(body, "Description"); got != 1 {
		t.Fatalf("Description headers = %d, want 1\nbody:\n%s", got, body)
	}
	if got := countHeaders(body, "Acceptance criteria"); got != 1 {
		t.Fatalf("Acceptance criteria headers = %d, want 1\nbody:\n%s", got, body)
	}
	if got := countHeaders(body, "Work log"); got != 1 {
		t.Fatalf("Work log headers = %d, want 1\nbody:\n%s", got, body)
	}
}

func TestTaskBodyPreStructuredCaseInsensitiveAndWhitespace(t *testing.T) {
	desc := "  ## description\nBlah.\n\n##  ACCEPTANCE CRITERIA\n- [ ] ok\n\n## Work log\n- note\n"
	body := TaskBody(desc)
	if got := countHeaders(body, "description"); got != 1 {
		t.Fatalf("description headers = %d, want 1\nbody:\n%s", got, body)
	}
	if got := countHeaders(body, "ACCEPTANCE CRITERIA"); got != 1 {
		t.Fatalf("ACCEPTANCE CRITERIA headers = %d, want 1\nbody:\n%s", got, body)
	}
	if got := countHeaders(body, "Work log"); got != 1 {
		t.Fatalf("Work log headers = %d, want 1\nbody:\n%s", got, body)
	}
}

func TestTaskBodyInlineMentionNotTreatedAsHeader(t *testing.T) {
	// A prose mention of the header text must not suppress the real header.
	desc := "See the ## Description section for details."
	body := TaskBody(desc)
	if got := countHeaders(body, "Description"); got != 1 {
		t.Fatalf("Description headers = %d, want 1\nbody:\n%s", got, body)
	}
}

func TestTaskBodyEmpty(t *testing.T) {
	if got := TaskBody(""); got != "" {
		t.Fatalf("TaskBody(\"\") = %q, want empty", got)
	}
	if got := TaskBody("   \n\t "); got != "" {
		t.Fatalf("TaskBody(whitespace) = %q, want empty", got)
	}
}
