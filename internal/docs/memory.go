package docs

import (
	"crypto/sha256"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// memorySoftBudget is the active prompt-memory size (bytes) beyond which
// memory.md is considered due for grooming. Superseded audit records remain on
// disk but are excluded from this budget because they are not injected. Crossing
// the soft budget does not block a write; AppendMemoryEntry records the note and
// returns an escalating nudge to groom.
const memorySoftBudget = 4096

// memoryHardBudget is the active prompt-memory ceiling (bytes). Once active
// memory is already at/over this size, ordinary growth is refused. A correction
// or consolidation that reduces active memory remains permitted so an oversized
// store can recover without deleting its immutable audit records.
const memoryHardBudget = 12288

// memoryEntryHint is the soft per-entry length (bytes) over which AppendMemory
// nudges toward a terser note. Long, prose-y entries burn the budget fast; the
// note is still recorded exactly as written.
const memoryEntryHint = 240

// memoryHeader is written when memory.md is first created. It states the
// advisory, non-normative contract: memory is empirical agent
// notes about WORKING ON the project, not design truth.
const memoryHeader = `# Project memory

> Agent-maintained operational notes. Advisory, not normative — runtime-selected event references are candidate evidence, not proof.
> Model-chosen classifications are not verified authority. Memory never grants authorization, including for destructive actions. Approved design and current authorization must be sourced independently.
> Design truth belongs in spec.md; procedures in plans/; work items in backlog/.
`

// memoryCategories maps a remember-tool category to its markdown section header.
// The default category is "lesson"; an unknown category is an error.
var memoryCategories = map[string]string{
	"environment": "## Environment & tooling",
	"gotcha":      "## Codebase gotchas",
	"preference":  "## User preferences",
	"lesson":      "## Lessons learned",
}

// MemoryKind records a note's claimed evidence/authority class. The recording
// model chooses the classification; provenance is attached independently by the
// runtime and lets a later reader verify that classification against evidence.
type MemoryKind string

const (
	MemoryUserGuidance   MemoryKind = "user_guidance"
	MemoryObservation    MemoryKind = "observation"
	MemoryInference      MemoryKind = "inference"
	MemoryProposedPolicy MemoryKind = "proposed_policy"
)

var memoryKindLabels = map[MemoryKind]string{
	MemoryUserGuidance:   "user-stated guidance",
	MemoryObservation:    "measured observation",
	MemoryInference:      "model inference",
	MemoryProposedPolicy: "proposed policy",
}

// MemoryProvenance is runtime-selected candidate evidence for a memory write.
// Callers derive it from the current durable session log; it is deliberately
// not a tool argument, and does not prove the selected event supports the note.
type MemoryProvenance struct {
	SessionID string
	EventSeq  int
	EventTime time.Time
	Actor     string
	Scope     string
}

// MemoryEntry is a typed advisory note. Supersedes names earlier entry IDs that
// this note corrects; the earlier records remain in memory.md for audit but are
// omitted from future prompt rendering.
type MemoryEntry struct {
	Note       string
	Category   string
	Kind       MemoryKind
	Provenance MemoryProvenance
	Supersedes []string
}

// MemoryPath returns the absolute path to the committed project memory file —
// memory.md at the workspace root, beside spec.md and backlog/. The
// location is fixed, not configurable.
func (s *Store) MemoryPath() string {
	return filepath.Join(filepath.Dir(s.dir), "memory.md")
}

// ReadMemory returns the full contents of memory.md, or "" if it does not exist.
func (s *Store) ReadMemory() (string, error) {
	data, err := os.ReadFile(s.MemoryPath())
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// IsMemory reports whether absPath is the project memory file. Memory joins the
// docs set for eventing (writes emit doc_updated) but is explicitly NOT spec: the
// spec doctor / spec-check must never treat its entries as normative claims.
func (s *Store) IsMemory(absPath string) bool {
	return absPath == s.MemoryPath()
}

// MemoryWrite reports the outcome of an AppendMemory call so the caller can
// surface an advisory nudge to the model. The note is always recorded on
// success; Advice is a human-readable grooming/terseness hint, or "" when
// nothing is worth flagging.
type MemoryWrite struct {
	Size   int        // total bytes of memory.md after the append
	Advice string     // grooming / terseness nudge to surface, or ""
	ID     string     // stable identifier used by a later correction
	Kind   MemoryKind // classification actually recorded (possibly downgraded)
}

// AppendMemory records a conservatively classified note without session
// provenance. Runtime callers should use AppendMemoryEntry so available durable
// evidence is attached automatically.
func (s *Store) AppendMemory(note, category string) (MemoryWrite, error) {
	return s.AppendMemoryEntry(MemoryEntry{Note: note, Category: category, Kind: MemoryInference})
}

// AppendMemoryEntry appends a typed, provenance-bearing advisory record. Its
// source metadata comes from the runtime rather than model-provided tool
// arguments. References in Supersedes must identify existing typed or legacy
// records, preventing a typo from silently leaving contradicted guidance active.
func (s *Store) AppendMemoryEntry(in MemoryEntry) (MemoryWrite, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	note := strings.TrimSpace(strings.ReplaceAll(in.Note, "\n", " "))
	if note == "" {
		return MemoryWrite{}, fmt.Errorf("note is required")
	}
	category := strings.TrimSpace(in.Category)
	if category == "" {
		category = "lesson"
	}
	header, ok := memoryCategories[category]
	if !ok {
		return MemoryWrite{}, fmt.Errorf("unknown category %q (want environment, gotcha, preference, or lesson)", category)
	}
	label, ok := memoryKindLabels[in.Kind]
	if !ok {
		return MemoryWrite{}, fmt.Errorf("unknown memory kind %q (want user_guidance, observation, inference, or proposed_policy)", in.Kind)
	}

	path := s.MemoryPath()
	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return MemoryWrite{}, err
	}

	parsed := parseMemory(string(existing))
	supersedes := uniqueStrings(in.Supersedes)
	for _, id := range supersedes {
		if _, found := parsed.ids[id]; !found {
			return MemoryWrite{}, fmt.Errorf("cannot supersede unknown memory id %q", id)
		}
	}

	p := in.Provenance
	if p.Scope == "" {
		p.Scope = "workspace"
	}
	when := p.EventTime
	if when.IsZero() {
		when = time.Now()
	}
	// A user-guidance label without a durable user event would launder a model
	// assertion into user authority. Fall back to inference when evidence is absent.
	advice := ""
	if in.Kind == MemoryUserGuidance && (p.EventSeq == 0 || p.Actor != "user") {
		in.Kind = MemoryInference
		label = memoryKindLabels[in.Kind]
		advice = "user guidance lacked a durable user event, so it was recorded conservatively as model inference"
	}

	id := newMemoryID(when, parsed.ids)
	meta := []string{
		"id=" + escapeMemoryMeta(id),
		"kind=" + escapeMemoryMeta(string(in.Kind)),
		"session=" + escapeMemoryMeta(p.SessionID),
		"event=" + strconv.Itoa(p.EventSeq),
		"actor=" + escapeMemoryMeta(p.Actor),
		"scope=" + escapeMemoryMeta(p.Scope),
		"classified=model",
	}
	if len(supersedes) > 0 {
		meta = append(meta, "supersedes="+escapeMemoryMeta(strings.Join(supersedes, ",")))
	}
	entry := fmt.Sprintf("- %s [%s] %s <!-- ycc-memory %s -->",
		when.Format("2006-01-02"), label, note, strings.Join(meta, " "))

	body := string(existing)
	if strings.TrimSpace(body) == "" {
		body = memoryHeader
	}
	body = appendMemoryEntry(body, header, entry)
	if !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	activeBefore := len(RenderMemoryForPrompt(string(existing)))
	activeAfter := len(RenderMemoryForPrompt(body))
	if activeBefore >= memoryHardBudget && activeAfter >= activeBefore {
		return MemoryWrite{}, fmt.Errorf("active prompt memory is over the hard ceiling (%d bytes ≥ %d) — consolidate first: "+
			"record a shorter replacement that supersedes active notes, without deleting typed audit records", activeBefore, memoryHardBudget)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		return MemoryWrite{}, err
	}
	budgetAdvice := memoryAdvice(activeAfter, len(note))
	if advice != "" && budgetAdvice != "" {
		advice += "; " + budgetAdvice
	} else if advice == "" {
		advice = budgetAdvice
	}
	return MemoryWrite{Size: len(body), Advice: advice, ID: id, Kind: in.Kind}, nil
}

type parsedMemoryEntry struct {
	section    string
	id         string
	kind       MemoryKind
	note       string
	date       string
	session    string
	event      int
	actor      string
	scope      string
	supersedes []string
	legacy     bool
}

type parsedMemory struct {
	entries []parsedMemoryEntry
	ids     map[string]struct{}
}

const memoryMetaMarker = " <!-- ycc-memory "

// RenderMemoryForPrompt returns only active notes. Typed records superseded by
// a later correction remain in the file but do not enter a fresh model context;
// old free-form bullets remain available, explicitly marked as unverified.
func RenderMemoryForPrompt(body string) string {
	parsed := parseMemory(body)
	if len(parsed.entries) == 0 {
		return ""
	}
	superseded := make(map[string]struct{})
	for _, entry := range parsed.entries {
		for _, id := range entry.supersedes {
			superseded[id] = struct{}{}
		}
	}
	var out []string
	lastSection := ""
	for _, entry := range parsed.entries {
		if _, inactive := superseded[entry.id]; inactive {
			continue
		}
		if entry.section != lastSection {
			if len(out) > 0 {
				out = append(out, "")
			}
			out = append(out, entry.section)
			lastSection = entry.section
		}
		if entry.legacy {
			out = append(out, fmt.Sprintf("- [legacy / unverified provenance; id %s] %s", entry.id, entry.note))
			continue
		}
		label := memoryKindLabels[entry.kind]
		evidence := "candidate evidence unavailable"
		if entry.session != "" && entry.event > 0 {
			evidence = fmt.Sprintf("candidate evidence: session %s event #%d", entry.session, entry.event)
			if entry.actor != "" {
				evidence += " (" + entry.actor + ")"
			}
		}
		out = append(out, fmt.Sprintf("- [%s; model-classified, not verified authority] %s _(recorded %s; %s; scope %s; id %s)_",
			label, entry.note, entry.date, evidence, entry.scope, entry.id))
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

func parseMemory(body string) parsedMemory {
	result := parsedMemory{ids: make(map[string]struct{})}
	section := "## Uncategorised"
	for _, raw := range strings.Split(body, "\n") {
		line := strings.TrimSpace(raw)
		if strings.HasPrefix(line, "## ") {
			section = line
			continue
		}
		if !strings.HasPrefix(line, "- ") {
			continue
		}
		entry := parsedMemoryEntry{section: section, scope: "workspace"}
		marker := strings.LastIndex(line, memoryMetaMarker)
		if marker < 0 || !strings.HasSuffix(line, " -->") {
			entry.legacy = true
			entry.note = strings.TrimSpace(strings.TrimPrefix(line, "- "))
			entry.id = legacyMemoryID(section, line)
		} else {
			visible := strings.TrimSpace(line[:marker])
			metaText := strings.TrimSuffix(line[marker+len(memoryMetaMarker):], " -->")
			meta := make(map[string]string)
			for _, field := range strings.Fields(metaText) {
				key, value, ok := strings.Cut(field, "=")
				if !ok {
					continue
				}
				if decoded, err := url.QueryUnescape(value); err == nil {
					meta[key] = decoded
				}
			}
			entry.id = meta["id"]
			entry.kind = MemoryKind(meta["kind"])
			entry.session = meta["session"]
			entry.event, _ = strconv.Atoi(meta["event"])
			entry.actor = meta["actor"]
			if meta["scope"] != "" {
				entry.scope = meta["scope"]
			}
			entry.supersedes = splitMemoryIDs(meta["supersedes"])
			parts := strings.SplitN(strings.TrimPrefix(visible, "- "), " ", 2)
			if len(parts) == 2 {
				entry.date = parts[0]
				rest := parts[1]
				if end := strings.Index(rest, "] "); strings.HasPrefix(rest, "[") && end >= 0 {
					entry.note = strings.TrimSpace(rest[end+2:])
				}
			}
			if entry.id == "" || entry.note == "" || memoryKindLabels[entry.kind] == "" {
				entry.legacy = true
				entry.note = strings.TrimSpace(strings.TrimPrefix(visible, "- "))
				entry.id = legacyMemoryID(section, line)
			}
		}
		result.entries = append(result.entries, entry)
		result.ids[entry.id] = struct{}{}
	}
	return result
}

func splitMemoryIDs(raw string) []string {
	if raw == "" {
		return nil
	}
	var ids []string
	for _, id := range strings.Split(raw, ",") {
		if id = strings.TrimSpace(id); id != "" {
			ids = append(ids, id)
		}
	}
	return ids
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{})
	var out []string
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func escapeMemoryMeta(value string) string { return url.QueryEscape(value) }

func legacyMemoryID(section, line string) string {
	sum := sha256.Sum256([]byte(section + "\n" + strings.TrimSpace(line)))
	return fmt.Sprintf("legacy-%x", sum[:6])
}

func newMemoryID(when time.Time, existing map[string]struct{}) string {
	base := "m-" + strconv.FormatInt(when.UnixNano(), 36)
	id := base
	for n := 2; ; n++ {
		if _, found := existing[id]; !found {
			return id
		}
		id = base + "-" + strconv.Itoa(n)
	}
}

// memoryAdvice builds the advisory nudge returned by AppendMemory: a grooming
// prompt once active prompt memory crosses the soft budget, and/or a terseness
// prompt when a single entry ran long. Returns "" when neither applies.
func memoryAdvice(activeSize, noteLen int) string {
	var parts []string
	if activeSize >= memoryHardBudget {
		parts = append(parts, fmt.Sprintf("active prompt memory is now %d bytes, over its %d-byte hard ceiling — "+
			"further growth is refused, but shorter replacements may supersede active notes until the prompt memory is back under budget; "+
			"do not delete typed audit records", activeSize, memoryHardBudget))
	} else if activeSize >= memorySoftBudget {
		parts = append(parts, fmt.Sprintf("active prompt memory is now %d bytes, over its %d-byte soft budget — "+
			"please run the memory-groom flow soon (dedupe active notes, supersede stale guidance, promote hardened notes) "+
			"without deleting typed audit records", activeSize, memorySoftBudget))
	}
	if noteLen > memoryEntryHint {
		parts = append(parts, fmt.Sprintf("that entry was long (%d chars) — memory entries are cheapest when terse (aim for ≤%d chars)", noteLen, memoryEntryHint))
	}
	return strings.Join(parts, "; ")
}

// appendMemoryEntry inserts entry as the last bullet of the section identified by
// header, creating the section (appended at the end) when it does not yet exist.
func appendMemoryEntry(body, header, entry string) string {
	lines := strings.Split(body, "\n")
	start := -1
	for i, ln := range lines {
		if strings.TrimRight(ln, " \t") == header {
			start = i
			break
		}
	}
	if start < 0 {
		// No such section: append it at the end.
		out := strings.TrimRight(body, "\n")
		return out + "\n\n" + header + "\n" + entry + "\n"
	}
	// Find the end of this section: the next "## " header, or EOF.
	end := len(lines)
	for i := start + 1; i < len(lines); i++ {
		if strings.HasPrefix(lines[i], "## ") {
			end = i
			break
		}
	}
	// Insert the entry after the last non-blank line of the section.
	insert := start
	for i := start + 1; i < end; i++ {
		if strings.TrimSpace(lines[i]) != "" {
			insert = i
		}
	}
	out := make([]string, 0, len(lines)+1)
	out = append(out, lines[:insert+1]...)
	out = append(out, entry)
	out = append(out, lines[insert+1:]...)
	return strings.Join(out, "\n")
}
