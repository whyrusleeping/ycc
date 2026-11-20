package docs

import "strings"

// setTOMLKey sets the boolean key = value in [table] of a TOML document,
// editing only that value so the rest of the document, comments included, is
// kept. It scans the whole document's syntax (strings, comments, arrays, inline
// tables), so it finds the real assignment in any of its forms: under a
// [table] header (names may be quoted), as a dotted "table.key" key, or inside
// an inline table. A missing key is added under the table's header, inside its
// inline table, or in a new table at the end. Callers validate the result.
func setTOMLKey(doc, table, key, value string) string {
	var current []string
	header := -1 // end of the [table] header line
	for _, st := range scanTOML(doc) {
		if st.header {
			current = st.key
			if !st.array && pathIs(current, table) {
				header = st.end
			}
			continue
		}
		path := append(append([]string(nil), current...), st.key...)
		switch {
		case pathIs(path, table, key):
			return doc[:st.valStart] + value + doc[st.valEnd:]
		case pathIs(path, table) && st.valStart < len(doc) && doc[st.valStart] == '{':
			return setInlineKey(doc, st.valStart, key, value)
		}
	}
	assignment := key + " = " + value
	if header >= 0 {
		return doc[:header] + "\n" + assignment + doc[header:]
	}
	out := strings.TrimRight(doc, "\n")
	if out != "" {
		out += "\n\n"
	}
	return out + "[" + table + "]\n" + assignment + "\n"
}

func pathIs(path []string, want ...string) bool {
	return strings.Join(path, "\x00") == strings.Join(want, "\x00")
}

// setInlineKey sets key = value in the inline table opening at doc[open].
func setInlineKey(doc string, open int, key, value string) string {
	i := open + 1
	for {
		i = skipSpace(doc, i, true)
		if i >= len(doc) {
			return doc
		}
		if doc[i] == '}' {
			body := strings.TrimRight(doc[:i], " \t")
			sep := ", "
			if strings.HasSuffix(body, "{") || strings.HasSuffix(body, ",") {
				sep = " "
			}
			return body + sep + key + " = " + value + " " + doc[i:]
		}
		if doc[i] == ',' {
			i++
			continue
		}
		eq := keyEnd(doc, i)
		if eq >= len(doc) {
			return doc
		}
		path := tomlKeyPath(doc[i:eq])
		start := skipSpace(doc, eq+1, false)
		end := skipValue(doc, start)
		if pathIs(path, key) {
			return doc[:start] + value + doc[end:]
		}
		i = end
	}
}

// tomlStatement is one top-level table header or key/value assignment.
type tomlStatement struct {
	header, array    bool
	key              []string
	valStart, valEnd int // value span (assignments)
	end              int // end of the statement's line, before its newline
}

// scanTOML splits a document into its top-level statements, skipping
// comments and the contents of strings, arrays, and inline tables.
func scanTOML(doc string) []tomlStatement {
	var out []tomlStatement
	for i := 0; i < len(doc); {
		i = skipSpace(doc, i, true)
		if i >= len(doc) {
			break
		}
		var st tomlStatement
		if doc[i] == '[' {
			st.header = true
			st.array = strings.HasPrefix(doc[i:], "[[")
			open := i + 1
			if st.array {
				open++
			}
			closeAt := keyEndAt(doc, open, ']')
			st.key = tomlKeyPath(doc[open:min(closeAt, len(doc))])
			i = closeAt + 1
			if st.array {
				i++
			}
		} else {
			eq := keyEnd(doc, i)
			st.key = tomlKeyPath(doc[i:min(eq, len(doc))])
			st.valStart = skipSpace(doc, eq+1, false)
			st.valEnd = skipValue(doc, st.valStart)
			i = st.valEnd
		}
		for i < len(doc) && doc[i] != '\n' {
			i++ // trailing whitespace and comment
		}
		st.end = i
		out = append(out, st)
	}
	return out
}

// skipSpace skips blanks and comments, and newlines when lines is set.
func skipSpace(doc string, i int, lines bool) int {
	for i < len(doc) {
		switch doc[i] {
		case ' ', '\t', '\r':
			i++
		case '\n':
			if !lines {
				return i
			}
			i++
		case '#':
			if !lines {
				return i
			}
			for i < len(doc) && doc[i] != '\n' {
				i++
			}
		default:
			return i
		}
	}
	return i
}

// keyEnd returns the index of the "=" ending the key that starts at i.
func keyEnd(doc string, i int) int { return keyEndAt(doc, i, '=') }

// keyEndAt returns the index of stop after a (possibly quoted, dotted) key
// starting at i, or of the line end when stop is missing.
func keyEndAt(doc string, i int, stop byte) int {
	for i < len(doc) && doc[i] != stop && doc[i] != '\n' {
		if doc[i] == '"' || doc[i] == '\'' {
			i = skipValue(doc, i)
			continue
		}
		i++
	}
	return i
}

// skipValue returns the end of the TOML value starting at i.
func skipValue(doc string, i int) int {
	if i >= len(doc) {
		return i
	}
	switch {
	case strings.HasPrefix(doc[i:], `"""`):
		for j := i + 3; j < len(doc); j++ {
			if doc[j] == '\\' {
				j++
			} else if strings.HasPrefix(doc[j:], `"""`) {
				return extraQuotes(doc, j+3, '"')
			}
		}
		return len(doc)
	case strings.HasPrefix(doc[i:], "'''"):
		if at := strings.Index(doc[i+3:], "'''"); at >= 0 {
			return extraQuotes(doc, i+3+at+3, '\'')
		}
		return len(doc)
	case doc[i] == '"':
		for j := i + 1; j < len(doc) && doc[j] != '\n'; j++ {
			if doc[j] == '\\' {
				j++
			} else if doc[j] == '"' {
				return j + 1
			}
		}
		return len(doc)
	case doc[i] == '\'':
		if at := strings.IndexAny(doc[i+1:], "'\n"); at >= 0 {
			return i + 1 + at + 1
		}
		return len(doc)
	case doc[i] == '[' || doc[i] == '{':
		closer := byte(']')
		if doc[i] == '{' {
			closer = '}'
		}
		j := i + 1
		for {
			j = skipSpace(doc, j, true)
			if j >= len(doc) {
				return j
			}
			switch {
			case doc[j] == closer:
				return j + 1
			case doc[j] == ',':
				j++
			case closer == '}':
				j = skipValue(doc, skipSpace(doc, keyEnd(doc, j)+1, false))
			default:
				j = skipValue(doc, j)
			}
		}
	}
	j := i
	for j < len(doc) && !strings.ContainsRune(" \t\r\n,]}#", rune(doc[j])) {
		j++
	}
	// A local date and time may be separated by a space (1979-05-27 07:32:00).
	if j-i == 10 && doc[i+4] == '-' && doc[i+7] == '-' && j+3 < len(doc) && doc[j] == ' ' &&
		isDigit(doc[j+1]) && isDigit(doc[j+2]) && doc[j+3] == ':' {
		return skipValue(doc, j+1)
	}
	if j == i {
		return i + 1 // always advance on a stray character
	}
	return j
}

// extraQuotes includes up to two quotes that TOML allows inside a multiline
// string right before its closing delimiter.
func extraQuotes(doc string, j int, q byte) int {
	for n := 0; n < 2 && j < len(doc) && doc[j] == q; n++ {
		j++
	}
	return j
}

// tomlKeyPath splits a TOML key (bare, quoted, or dotted) into its parts.
func tomlKeyPath(key string) []string {
	var parts []string
	var b strings.Builder
	quote := byte(0)
	for i := 0; i < len(key); i++ {
		c := key[i]
		switch {
		case quote != 0 && c == quote:
			quote = 0
		case quote == '"' && c == '\\' && i+1 < len(key):
			i++
			b.WriteByte(key[i])
		case quote != 0:
			b.WriteByte(c)
		case c == '"' || c == '\'':
			quote = c
		case c == '.':
			parts = append(parts, strings.TrimSpace(b.String()))
			b.Reset()
		case c == ' ' || c == '\t':
		default:
			b.WriteByte(c)
		}
	}
	return append(parts, strings.TrimSpace(b.String()))
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }
