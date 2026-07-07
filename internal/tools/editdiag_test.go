package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The not-found error must diagnose an old_string pasted from Read output
// with the "  123\t" line-number prefixes still attached.
func TestEditHintLineNumberPaste(t *testing.T) {
	root := t.TempDir()
	reg := workerReg(root)
	dispatch(t, reg, "Write", `{"file_path":"a.txt","content":"alpha\nbeta\ngamma"}`)

	res := dispatch(t, reg, "Edit",
		`{"file_path":"a.txt","old_string":"     1\talpha\n     2\tbeta","new_string":"x"}`)
	if !res.IsError || !strings.Contains(res.Content, "line-number prefixes") {
		t.Fatalf("expected line-number-paste hint, got %q (err=%v)", res.Content, res.IsError)
	}
}

// The not-found error must point at the exact line range when the only
// difference is whitespace (indentation, tabs vs spaces, trailing blanks).
func TestEditHintWhitespaceOnly(t *testing.T) {
	root := t.TempDir()
	reg := workerReg(root)
	content := "fn main() {\n\tlet x = 1;\n\tlet y = 2;\n}\n"
	if err := os.WriteFile(filepath.Join(root, "a.rs"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	// Spaces where the file has tabs.
	res := dispatch(t, reg, "Edit",
		`{"file_path":"a.rs","old_string":"    let x = 1;\n    let y = 2;","new_string":"x"}`)
	if !res.IsError || !strings.Contains(res.Content, "except for whitespace") {
		t.Fatalf("expected whitespace hint, got %q (err=%v)", res.Content, res.IsError)
	}
	if !strings.Contains(res.Content, "lines 2-3") {
		t.Fatalf("whitespace hint should name lines 2-3, got %q", res.Content)
	}
	if !strings.Contains(res.Content, "\tlet x = 1;") {
		t.Fatalf("whitespace hint should echo the exact file text, got %q", res.Content)
	}
}

// When the file changed since it was read (e.g. by the agent's own earlier
// Edit), the not-found error must show the closest current region so the model
// can retry without a re-Read.
func TestEditHintClosestMatch(t *testing.T) {
	root := t.TempDir()
	reg := workerReg(root)
	content := "// header\nfunc a() {\n\treturn 1\n}\nfunc b() {\n\treturn 2\n}\n"
	if err := os.WriteFile(filepath.Join(root, "a.go"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	// 3 of 4 lines match the func b block; the model remembered a stale body.
	res := dispatch(t, reg, "Edit",
		`{"file_path":"a.go","old_string":"}\nfunc b() {\n\treturn 99\n}","new_string":"x"}`)
	if !res.IsError || !strings.Contains(res.Content, "closest match") {
		t.Fatalf("expected closest-match hint, got %q (err=%v)", res.Content, res.IsError)
	}
	if !strings.Contains(res.Content, "lines 4-7") || !strings.Contains(res.Content, "return 2") {
		t.Fatalf("closest-match hint should show current lines 4-7, got %q", res.Content)
	}
}

// With nothing similar in the file, the error falls back to re-Read advice
// rather than a misleading snippet.
func TestEditHintNoSimilarText(t *testing.T) {
	root := t.TempDir()
	reg := workerReg(root)
	dispatch(t, reg, "Write", `{"file_path":"a.txt","content":"alpha\nbeta\ngamma"}`)

	res := dispatch(t, reg, "Edit",
		`{"file_path":"a.txt","old_string":"nothing\nlike\nthis\nhere","new_string":"x"}`)
	if !res.IsError || !strings.Contains(res.Content, "Read the relevant section") {
		t.Fatalf("expected re-read advice, got %q (err=%v)", res.Content, res.IsError)
	}
	if strings.Contains(res.Content, "closest match") {
		t.Fatalf("no-similarity failure should not claim a closest match: %q", res.Content)
	}
}

// A no-op edit (old_string == new_string) is rejected instead of reporting a
// misleading success.
func TestEditIdenticalStringsRejected(t *testing.T) {
	root := t.TempDir()
	reg := workerReg(root)
	dispatch(t, reg, "Write", `{"file_path":"a.txt","content":"hello world"}`)

	res := dispatch(t, reg, "Edit",
		`{"file_path":"a.txt","old_string":"hello","new_string":"hello"}`)
	if !res.IsError || !strings.Contains(res.Content, "identical") {
		t.Fatalf("expected identical-strings rejection, got %q (err=%v)", res.Content, res.IsError)
	}
}

// The closest-match snippet is capped so a huge old_string cannot balloon the
// error message.
func TestEditHintSnippetCapped(t *testing.T) {
	var fileB, needleB strings.Builder
	for i := 0; i < 40; i++ {
		fileB.WriteString(strings.Repeat("x", 5) + " line\n")
		if i == 39 {
			needleB.WriteString("DIFFERENT")
		} else {
			needleB.WriteString(strings.Repeat("x", 5) + " line")
		}
		if i < 39 {
			needleB.WriteString("\n")
		}
	}
	hint := editNotFoundHint(fileB.String(), needleB.String())
	if !strings.Contains(hint, "more lines]") {
		t.Fatalf("expected capped snippet with elision marker, got %q", hint)
	}
	if n := strings.Count(hint, "\t"); n > editHintMaxSnippetLines {
		t.Fatalf("snippet shows %d lines, cap is %d: %q", n, editHintMaxSnippetLines, hint)
	}
}
