// This file owns unified diff generation and terminal diff rendering.
package tui

import (
	"fmt"
	"strings"
)

func looksDiff(s string) bool {
	return strings.Contains(s, "diff --git ") || strings.Contains(s, "\n@@ ") || strings.HasPrefix(s, "@@ ")
}

func looksCatN(s string) bool {
	first := s
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		first = s[:i]
	}
	return catnRe.MatchString(first)
}

func colorizeDiff(s string) string {
	var b strings.Builder
	for _, ln := range strings.Split(s, "\n") {
		switch {
		case strings.HasPrefix(ln, "+++") || strings.HasPrefix(ln, "---"):
			b.WriteString(diffMetaStyle.Render(ln))
		case strings.HasPrefix(ln, "@@"):
			b.WriteString(diffHunkStyle.Render(ln))
		case strings.HasPrefix(ln, "+"):
			b.WriteString(diffAddStyle.Render(ln))
		case strings.HasPrefix(ln, "-"):
			b.WriteString(diffDelStyle.Render(ln))
		case strings.HasPrefix(ln, "diff ") || strings.HasPrefix(ln, "index "):
			b.WriteString(dimStyle.Render(ln))
		default:
			b.WriteString(ln)
		}
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
}

// unifiedDiff computes a git-style unified diff between oldStr and newStr at the
// given amount of context lines. It performs a line-level LCS diff and groups
// changes into hunks. Original line text (including indentation/whitespace) is
// preserved verbatim after the +/-/space prefix. The output is bounded so it
// degrades gracefully on very large or pathological inputs.
func unifiedDiff(oldStr, newStr string, context int) string {
	if context < 0 {
		context = 0
	}
	oldLines := strings.Split(oldStr, "\n")
	newLines := strings.Split(newStr, "\n")

	const maxLines = 400    // cap total emitted diff lines
	const lcsLineCap = 2000 // bound O(n*m) LCS cost
	var b strings.Builder
	emitted := 0
	truncated := false
	writeLine := func(s string) bool {
		if emitted >= maxLines {
			truncated = true
			return false
		}
		b.WriteString(s)
		b.WriteByte('\n')
		emitted++
		return true
	}

	// Fall back to a wholesale remove/add diff for very large inputs to avoid the
	// quadratic LCS cost.
	if len(oldLines) > lcsLineCap || len(newLines) > lcsLineCap {
		writeLine(fmt.Sprintf("@@ -1,%d +1,%d @@", len(oldLines), len(newLines)))
		for _, ln := range oldLines {
			if !writeLine("-" + ln) {
				break
			}
		}
		for _, ln := range newLines {
			if !writeLine("+" + ln) {
				break
			}
		}
		if truncated {
			b.WriteString("… (diff truncated)\n")
		}
		return strings.TrimRight(b.String(), "\n")
	}

	ops := diffOps(oldLines, newLines)

	// Group ops into hunks separated by more than 2*context unchanged lines.
	type hunk struct{ start, end int } // indices into ops (half-open)
	var hunks []hunk
	for i := 0; i < len(ops); {
		if ops[i].kind == diffEqual {
			i++
			continue
		}
		// Found a change; extend the hunk to include neighbouring changes that are
		// within 2*context unchanged lines of each other.
		start := i
		end := i + 1
		gap := 0
		for j := i + 1; j < len(ops); j++ {
			if ops[j].kind == diffEqual {
				gap++
				if gap > 2*context {
					break
				}
			} else {
				gap = 0
				end = j + 1
			}
		}
		// Expand by context on both sides.
		s := start - context
		if s < 0 {
			s = 0
		}
		e := end + context
		if e > len(ops) {
			e = len(ops)
		}
		// Merge with previous hunk if they overlap.
		if n := len(hunks); n > 0 && s <= hunks[n-1].end {
			hunks[n-1].end = e
		} else {
			hunks = append(hunks, hunk{s, e})
		}
		i = end
	}

	for _, h := range hunks {
		var oldStart, newStart, oldCount, newCount int
		// Compute 1-based start line numbers and counts for the hunk header.
		oldLine, newLine := 1, 1
		for i := 0; i < h.start; i++ {
			switch ops[i].kind {
			case diffEqual:
				oldLine++
				newLine++
			case diffDelete:
				oldLine++
			case diffInsert:
				newLine++
			}
		}
		oldStart, newStart = oldLine, newLine
		for i := h.start; i < h.end; i++ {
			switch ops[i].kind {
			case diffEqual:
				oldCount++
				newCount++
			case diffDelete:
				oldCount++
			case diffInsert:
				newCount++
			}
		}
		if !writeLine(fmt.Sprintf("@@ -%d,%d +%d,%d @@", oldStart, oldCount, newStart, newCount)) {
			break
		}
		stop := false
		for i := h.start; i < h.end; i++ {
			op := ops[i]
			var prefix string
			switch op.kind {
			case diffEqual:
				prefix = " "
			case diffDelete:
				prefix = "-"
			case diffInsert:
				prefix = "+"
			}
			if !writeLine(prefix + op.text) {
				stop = true
				break
			}
		}
		if stop {
			break
		}
	}
	if truncated {
		b.WriteString("… (diff truncated)\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

type diffKind int

const (
	diffEqual diffKind = iota
	diffDelete
	diffInsert
)

type diffOp struct {
	kind diffKind
	text string
}

// diffOps produces an edit script (equal/delete/insert) transforming a into b
// using a classic LCS dynamic-programming table.
func diffOps(a, b []string) []diffOp {
	n, m := len(a), len(b)
	// lcs[i][j] = length of LCS of a[i:] and b[j:].
	lcs := make([][]int, n+1)
	for i := range lcs {
		lcs[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else if lcs[i+1][j] >= lcs[i][j+1] {
				lcs[i][j] = lcs[i+1][j]
			} else {
				lcs[i][j] = lcs[i][j+1]
			}
		}
	}
	var ops []diffOp
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case a[i] == b[j]:
			ops = append(ops, diffOp{diffEqual, a[i]})
			i++
			j++
		case lcs[i+1][j] >= lcs[i][j+1]:
			ops = append(ops, diffOp{diffDelete, a[i]})
			i++
		default:
			ops = append(ops, diffOp{diffInsert, b[j]})
			j++
		}
	}
	for ; i < n; i++ {
		ops = append(ops, diffOp{diffDelete, a[i]})
	}
	for ; j < m; j++ {
		ops = append(ops, diffOp{diffInsert, b[j]})
	}
	return ops
}

func dimLineNumbers(s string) string {
	lines := strings.Split(s, "\n")
	for i, ln := range lines {
		if mm := catnRe.FindStringSubmatch(ln); mm != nil {
			lines[i] = dimStyle.Render(mm[1]) + mm[2]
		}
	}
	return strings.Join(lines, "\n")
}
