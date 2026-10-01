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

// MemorySoftBudget is the active prompt-memory size (bytes) beyond which
// memory.md is considered due for grooming. Superseded and retired audit records
// remain on disk but are excluded from this budget because they are not
// injected. Crossing the soft budget does not block a write; the daemon uses it
// as the trigger for automatic grooming.
const MemorySoftBudget = 4096

// MemoryHardBudget is the active prompt-memory backstop (bytes). A write that
// would leave active memory above it is refused unless it reduces active memory
// (a consolidating supersession). It matches the prompt-injection cap so every
// accepted note is actually delivered to agents. Automatic grooming is expected
// to keep memory far below it; the refusal exists for when grooming is
// disabled or failing.
const MemoryHardBudget = 16384

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

// memoryRetiredSection holds retraction records. They are audit-only: prompt
// rendering never includes them.
const memoryRetiredSection = "## Retired notes (audit only)"

// MemoryKind records a note's claimed evidence/authority class. The recording
// model chooses the classification; provenance is attached independently by the
// runtime and lets a later reader verify that classification against evidence.
type MemoryKind string

const (
	MemoryUserGuidance   MemoryKind = "user_guidance"
	MemoryObservation    MemoryKind = "observation"
	MemoryInference      MemoryKind = "inference"
	MemoryProposedPolicy MemoryKind = "proposed_policy"
	// MemoryRetraction marks an audit record that retires earlier notes without
	// a replacement. It is written only by RetireMemory, is never a remember
	// kind, and never renders into prompts.
	MemoryRetraction MemoryKind = "retraction"
)

// memoryKindLabels are the human-readable labels written into memory.md.
var memoryKindLabels = map[MemoryKind]string{
	MemoryUserGuidance:   "user-stated guidance",
	MemoryObservation:    "measured observation",
	MemoryInference:      "model inference",
	MemoryProposedPolicy: "proposed policy",
}

// memoryKindTags are the compact kind tags used in prompt rendering. The
// "model-classified, not verified authority" caveat is stated once in the prompt
// header rather than repeated on every line.
var memoryKindTags = map[MemoryKind]string{
	MemoryUserGuidance:   "user-stated",
	MemoryObservation:    "observation",
	MemoryInference:      "inference",
	MemoryProposedPolicy: "proposed-policy",
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

// MemoryStatus summarizes active prompt memory against its budgets.
type MemoryStatus struct {
	ActiveBytes int // size of the prompt rendering of active notes
	ActiveNotes int // number of active notes
	SoftBudget  int
	HardBudget  int
}

// OverSoftBudget reports whether active memory is due for grooming.
func (st MemoryStatus) OverSoftBudget() bool { return st.ActiveBytes >= st.SoftBudget }

// MemoryStatusOf measures a memory.md body.
func MemoryStatusOf(body string) MemoryStatus {
	parsed := parseMemory(body)
	return MemoryStatus{
		ActiveBytes: len(RenderMemoryForPrompt(body)),
		ActiveNotes: len(parsed.active()),
		SoftBudget:  MemorySoftBudget,
		HardBudget:  MemoryHardBudget,
	}
}

// MemoryStatus measures the project's memory.md. A missing file is empty.
func (s *Store) MemoryStatus() (MemoryStatus, error) {
	body, err := s.ReadMemory()
	if err != nil {
		return MemoryStatus{}, err
	}
	return MemoryStatusOf(body), nil
}

// MemoryWrite reports the outcome of an AppendMemory call so the caller can
// surface an advisory nudge to the model. The note is always recorded on
// success.
type MemoryWrite struct {
	Size   int        // total bytes of memory.md after the append
	Advice string     // all advice combined (Notes then BudgetAdvice), or ""
	ID     string     // stable identifier used by a later correction
	Kind   MemoryKind // classification actually recorded (possibly downgraded)
	// Active is the active prompt-memory size after the write; OverSoft reports
	// whether it is at/over the soft budget.
	Active   int
	OverSoft bool
	// Notes are non-budget remarks (classification downgrade, terseness).
	Notes []string
	// BudgetAdvice describes the active size against the budgets, or "".
	BudgetAdvice string
}

// MemoryNoteSize identifies one active note and its rendered prompt cost.
type MemoryNoteSize struct {
	ID      string
	Bytes   int
	Preview string
}

// MemoryBudgetError is returned when a write would leave active prompt memory
// above the hard backstop without reducing it. It carries enough detail for the
// model to act: how much to free and which notes cost the most.
type MemoryBudgetError struct {
	Active  int // active bytes before the write
	After   int // active bytes the write would produce
	Ceiling int
	Largest []MemoryNoteSize
}

// NeedToFree is how many active bytes must be freed before the refused write fits.
func (e *MemoryBudgetError) NeedToFree() int { return e.After - e.Ceiling }

func (e *MemoryBudgetError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "active prompt memory is %d bytes and this write would make it %d, over the %d-byte ceiling — "+
		"consolidate first by freeing at least %d bytes: retire obsolete notes with forget (always allowed), or record one "+
		"shorter note whose supersedes lists several related notes (a replacement is accepted only when it shrinks active "+
		"memory in total; typed audit records are kept, never delete them by hand)",
		e.Active, e.After, e.Ceiling, e.NeedToFree())
	if len(e.Largest) > 0 {
		b.WriteString(". Largest active notes: ")
		for i, n := range e.Largest {
			if i > 0 {
				b.WriteString("; ")
			}
			fmt.Fprintf(&b, "%s (%d B) %q", n.ID, n.Bytes, n.Preview)
		}
	}
	return b.String()
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

	p := normalizeProvenance(in.Provenance)
	// A user-guidance label without a durable user event would launder a model
	// assertion into user authority. Fall back to inference when evidence is absent.
	var notes []string
	if in.Kind == MemoryUserGuidance && (p.EventSeq == 0 || p.Actor != "user") {
		in.Kind = MemoryInference
		label = memoryKindLabels[in.Kind]
		notes = append(notes, "user guidance lacked a durable user event, so it was recorded conservatively as model inference")
	}

	id := newMemoryID(p.EventTime, parsed.ids)
	entry := formatMemoryRecord(p, id, label, note, in.Kind, supersedes)

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
	if activeAfter > MemoryHardBudget && activeAfter >= activeBefore {
		return MemoryWrite{}, &MemoryBudgetError{
			Active: activeBefore, After: activeAfter, Ceiling: MemoryHardBudget,
			Largest: largestActiveNotes(parsed, 5),
		}
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		return MemoryWrite{}, err
	}
	if len(note) > memoryEntryHint {
		notes = append(notes, fmt.Sprintf("that entry was long (%d chars) — memory entries are cheapest when terse (aim for ≤%d chars)", len(note), memoryEntryHint))
	}
	budget := memoryBudgetAdvice(activeAfter)
	advice := append(append([]string(nil), notes...), budget)
	return MemoryWrite{
		Size: len(body), Advice: joinNonEmpty(advice, "; "), ID: id, Kind: in.Kind,
		Active: activeAfter, OverSoft: activeAfter >= MemorySoftBudget,
		Notes: notes, BudgetAdvice: budget,
	}, nil
}

// MemoryRetire reports the outcome of RetireMemory.
type MemoryRetire struct {
	ID              string   // id of the retraction audit record
	Retired         []string // ids that were active and are now retired
	AlreadyInactive []string // requested ids that were already superseded/retired
	ActiveBefore    int
	ActiveAfter     int
}

// RetireMemory retires active notes without recording a replacement. It appends
// an audit-only retraction record that supersedes ids; the retired records stay
// in memory.md but leave future prompts. It only shrinks active memory, so it is
// never refused by the budget. Unknown ids are an error; ids that are already
// inactive are reported rather than rejected, unless nothing would change.
func (s *Store) RetireMemory(ids []string, reason string, prov MemoryProvenance) (MemoryRetire, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	ids = uniqueStrings(ids)
	if len(ids) == 0 {
		return MemoryRetire{}, fmt.Errorf("at least one memory id is required")
	}
	path := s.MemoryPath()
	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return MemoryRetire{}, err
	}
	parsed := parseMemory(string(existing))
	activeIDs := make(map[string]struct{})
	for _, e := range parsed.active() {
		activeIDs[e.id] = struct{}{}
	}
	var retired, inactive []string
	for _, id := range ids {
		if _, found := parsed.ids[id]; !found {
			return MemoryRetire{}, fmt.Errorf("cannot retire unknown memory id %q", id)
		}
		if _, active := activeIDs[id]; active {
			retired = append(retired, id)
		} else {
			inactive = append(inactive, id)
		}
	}
	if len(retired) == 0 {
		return MemoryRetire{}, fmt.Errorf("nothing to retire: %s already inactive (superseded or retired)", strings.Join(inactive, ", "))
	}
	reason = strings.TrimSpace(strings.ReplaceAll(reason, "\n", " "))
	if reason == "" {
		reason = "obsolete"
	}
	p := normalizeProvenance(prov)
	id := newMemoryID(p.EventTime, parsed.ids)
	entry := formatMemoryRecord(p, id, "retired", reason, MemoryRetraction, retired)

	body := string(existing)
	body = appendMemoryEntry(body, memoryRetiredSection, entry)
	if !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		return MemoryRetire{}, err
	}
	return MemoryRetire{
		ID: id, Retired: retired, AlreadyInactive: inactive,
		ActiveBefore: len(RenderMemoryForPrompt(string(existing))),
		ActiveAfter:  len(RenderMemoryForPrompt(body)),
	}, nil
}

func normalizeProvenance(p MemoryProvenance) MemoryProvenance {
	if p.Scope == "" {
		p.Scope = "workspace"
	}
	if p.EventTime.IsZero() {
		p.EventTime = time.Now()
	}
	return p
}

func formatMemoryRecord(p MemoryProvenance, id, label, text string, kind MemoryKind, supersedes []string) string {
	meta := []string{
		"id=" + escapeMemoryMeta(id),
		"kind=" + escapeMemoryMeta(string(kind)),
		"session=" + escapeMemoryMeta(p.SessionID),
		"event=" + strconv.Itoa(p.EventSeq),
		"actor=" + escapeMemoryMeta(p.Actor),
		"scope=" + escapeMemoryMeta(p.Scope),
		"classified=model",
	}
	if len(supersedes) > 0 {
		meta = append(meta, "supersedes="+escapeMemoryMeta(strings.Join(supersedes, ",")))
	}
	return fmt.Sprintf("- %s [%s] %s%s%s -->",
		p.EventTime.Format("2006-01-02"), label, text, memoryMetaMarker, strings.Join(meta, " "))
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

// active returns the entries that render into prompts: not superseded by any
// later record, and not themselves retraction records.
func (p parsedMemory) active() []parsedMemoryEntry {
	superseded := make(map[string]struct{})
	for _, entry := range p.entries {
		for _, id := range entry.supersedes {
			superseded[id] = struct{}{}
		}
	}
	var out []parsedMemoryEntry
	for _, entry := range p.entries {
		if entry.kind == MemoryRetraction {
			continue
		}
		if _, inactive := superseded[entry.id]; inactive {
			continue
		}
		out = append(out, entry)
	}
	return out
}

const memoryMetaMarker = " <!-- ycc-memory "

// RenderMemoryForPrompt returns only active notes, one compact line each:
//
//   - [<kind>; <recorded date>; <session>#<event>[/<actor>]; <id>] note
//
// The actor is omitted when it is the coordinator and the scope when it is the
// default workspace scope. Legacy (untyped) bullets render as "- [<legacy id>]
// note". The advisory / unverified-authority framing is stated once by the
// prompt header instead of per line, which keeps a note's metadata cheaper than
// its content. Typed records superseded by a later correction or retired remain
// in the file but do not enter a fresh model context.
func RenderMemoryForPrompt(body string) string {
	active := parseMemory(body).active()
	var out []string
	lastSection := ""
	for _, entry := range active {
		if entry.section != lastSection {
			if len(out) > 0 {
				out = append(out, "")
			}
			out = append(out, entry.section)
			lastSection = entry.section
		}
		out = append(out, renderMemoryEntry(entry))
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

func renderMemoryEntry(entry parsedMemoryEntry) string {
	if entry.legacy {
		return fmt.Sprintf("- [%s] %s", entry.id, entry.note)
	}
	tags := []string{memoryKindTags[entry.kind], entry.date}
	evidence := "no evidence"
	if entry.session != "" && entry.event > 0 {
		evidence = fmt.Sprintf("%s#%d", entry.session, entry.event)
		if entry.actor != "" && entry.actor != "coordinator" {
			evidence += "/" + entry.actor
		}
	}
	tags = append(tags, evidence)
	if entry.scope != "" && entry.scope != "workspace" {
		tags = append(tags, "scope "+entry.scope)
	}
	tags = append(tags, entry.id)
	return "- [" + strings.Join(tags, "; ") + "] " + entry.note
}

// largestActiveNotes returns up to n active notes ordered by rendered size.
func largestActiveNotes(parsed parsedMemory, n int) []MemoryNoteSize {
	var sizes []MemoryNoteSize
	for _, e := range parsed.active() {
		preview := e.note
		if r := []rune(preview); len(r) > 60 {
			preview = string(r[:60]) + "…"
		}
		sizes = append(sizes, MemoryNoteSize{ID: e.id, Bytes: len(renderMemoryEntry(e)) + 1, Preview: preview})
	}
	sort.SliceStable(sizes, func(i, j int) bool { return sizes[i].Bytes > sizes[j].Bytes })
	if len(sizes) > n {
		sizes = sizes[:n]
	}
	return sizes
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
			knownKind := memoryKindLabels[entry.kind] != "" || entry.kind == MemoryRetraction
			if entry.id == "" || entry.note == "" || !knownKind {
				entry.legacy = true
				entry.kind = ""
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

func joinNonEmpty(parts []string, sep string) string {
	var out []string
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, sep)
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

// memoryBudgetAdvice states active size against the budgets once it crosses the
// soft budget, with the remedies available to any coordinator. Callers with
// access to automatic grooming may replace it with a more specific status.
func memoryBudgetAdvice(activeSize int) string {
	if activeSize < MemorySoftBudget {
		return ""
	}
	return fmt.Sprintf("active prompt memory is now %d bytes, over its %d-byte soft budget (hard ceiling %d) — "+
		"retire obsolete notes with forget, or merge related notes into one shorter note via remember supersedes; "+
		"never delete typed audit records by hand", activeSize, MemorySoftBudget, MemoryHardBudget)
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
		if out == "" {
			return header + "\n" + entry + "\n"
		}
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
