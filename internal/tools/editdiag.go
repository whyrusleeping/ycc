package tools

import (
	"fmt"
	"regexp"
	"strings"
)

// Edit-failure diagnostics. Transcript analysis of real sessions shows the
// dominant Edit failure is "old_string not found", and that the model then
// recovers by re-Reading the file and retrying — a wasted round trip plus a
// potentially large re-Read. editNotFoundHint closes most of that loop by
// diagnosing WHY the match failed and, when possible, showing the closest
// region of the current file so the model can retry immediately:
//
//   - old_string pasted from Read output with the "  123\t" line-number
//     prefixes still attached → say so explicitly.
//   - a region matches except for whitespace (indentation, tabs vs spaces,
//     trailing blanks) → point at the exact line range.
//   - a region mostly matches (the file changed since it was read, often by
//     the agent's own earlier Edit) → show that region verbatim.
//   - nothing similar → advise a re-Read of the relevant section.

// editHintMaxSnippetLines caps how many lines of the closest-match region are
// echoed back in the error; editHintMaxLineChars caps each echoed line.
const (
	editHintMaxSnippetLines = 12
	editHintMaxLineChars    = 200
	// editHintMinScore is the fraction of (whitespace-normalized) lines that
	// must match for a region to be reported as the closest match; below it
	// the snippet would be noise rather than help.
	editHintMinScore = 0.5
	// editHintMaxNeedleLines bounds the sliding-window scan (O(file lines ×
	// needle lines)); an old_string bigger than this gets the generic hint.
	editHintMaxNeedleLines = 400
)

// readLineNoPrefix matches the cat -n formatting the Read tool emits (spaces,
// a line number, a tab). An old_string whose lines mostly carry this prefix
// was pasted from Read output rather than copied as bare file content.
var readLineNoPrefix = regexp.MustCompile(`^\s*\d+\t`)

// editNotFoundHint explains why oldStr has no exact match in content and
// returns actionable guidance to append to the not-found error. It never
// returns an empty string.
func editNotFoundHint(content, oldStr string) string {
	needle := strings.Split(oldStr, "\n")

	// Pasted Read output: a majority of lines still carry the line-number
	// prefix. (Majority, not all — the first line of a copied region often
	// loses its prefix to prompt-assembly trimming.)
	prefixed := 0
	for _, l := range needle {
		if readLineNoPrefix.MatchString(l) {
			prefixed++
		}
	}
	if prefixed*2 > len(needle) {
		return "old_string looks like it was pasted from Read output with the line-number prefixes still attached; pass the bare file content without the \"  123\\t\" numbering."
	}

	const rereadAdvice = "The file content may have changed since you read it (possibly by your own earlier Edit) — Read the relevant section and retry with its exact current text."
	if len(needle) > editHintMaxNeedleLines {
		return rereadAdvice
	}

	fileLines := strings.Split(content, "\n")
	if len(needle) > len(fileLines) {
		return rereadAdvice
	}
	normFile := make([]string, len(fileLines))
	for i, l := range fileLines {
		normFile[i] = normalizeWS(l)
	}
	normNeedle := make([]string, len(needle))
	for i, l := range needle {
		normNeedle[i] = normalizeWS(l)
	}

	// Slide a window of len(needle) lines over the file, scoring each start by
	// how many whitespace-normalized lines match. A full-score window means the
	// difference is whitespace-only; otherwise the best-scoring window is the
	// closest match.
	m := len(needle)
	bestAt, bestMatches := -1, 0
	for i := 0; i+m <= len(fileLines); i++ {
		matches := 0
		for j := 0; j < m; j++ {
			if normFile[i+j] == normNeedle[j] {
				matches++
			}
		}
		if matches > bestMatches {
			bestMatches, bestAt = matches, i
			if matches == m {
				break
			}
		}
	}
	switch {
	case bestAt >= 0 && bestMatches == m:
		return fmt.Sprintf("lines %d-%d match except for whitespace (indentation, tabs vs spaces, or trailing blanks):\n%s\nCopy that text exactly.",
			bestAt+1, bestAt+m, editSnippet(fileLines, bestAt, m))
	case bestAt >= 0 && m > 1 && float64(bestMatches) >= editHintMinScore*float64(m):
		return fmt.Sprintf("closest match in the current file is lines %d-%d:\n%s\nThe file may have changed since you read it (possibly by your own earlier Edit); retry against this current text.",
			bestAt+1, bestAt+m, editSnippet(fileLines, bestAt, m))
	}
	return rereadAdvice
}

// normalizeWS collapses each run of whitespace in s to a single space and
// trims the ends, so two lines differing only in indentation/tabs/trailing
// blanks compare equal.
func normalizeWS(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// editSnippet renders file lines [start, start+n) in cat -n style, capped at
// editHintMaxSnippetLines lines and editHintMaxLineChars per line so a huge
// old_string cannot balloon the error message.
func editSnippet(lines []string, start, n int) string {
	var b strings.Builder
	shown := n
	if shown > editHintMaxSnippetLines {
		shown = editHintMaxSnippetLines
	}
	for i := 0; i < shown; i++ {
		line := lines[start+i]
		if len(line) > editHintMaxLineChars {
			line = line[:editHintMaxLineChars] + "…"
		}
		fmt.Fprintf(&b, "%6d\t%s\n", start+i+1, line)
	}
	if n > shown {
		fmt.Fprintf(&b, "… [%d more lines]\n", n-shown)
	}
	return strings.TrimRight(b.String(), "\n")
}
