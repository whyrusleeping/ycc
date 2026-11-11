package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode/utf8"
)

// QuickReply is a canned message a client offers as a one-gesture send. ID is
// set only on default replies (and kept while they are edited).
type QuickReply struct {
	ID    string `json:"id,omitempty"`
	Label string `json:"label"`
	Text  string `json:"text"`
	// Icon names one of the clients' shared icons; empty for the default.
	Icon string `json:"icon,omitempty"`
}

// DefaultQuickReplies is the daemon-wide list until the user first edits it.
var DefaultQuickReplies = []QuickReply{
	{ID: "go_ahead", Label: "Go ahead", Text: "Go ahead", Icon: "check"},
	{ID: "continue", Label: "Continue", Text: "Continue", Icon: "forward"},
	{ID: "commit_push", Label: "Commit & push", Text: "Commit and push", Icon: "upload"},
}

const (
	quickRepliesFile     = "quick-replies.json"
	maxQuickReplies      = 12
	maxQuickReplyLabel   = 32
	maxQuickReplyTextLen = 4000
	maxQuickReplyIcon    = 32
)

// ErrInvalidQuickReplies reports a SetQuickReplies list that fails validation.
var ErrInvalidQuickReplies = errors.New("invalid quick replies")

type quickRepliesDoc struct {
	Replies []QuickReply `json:"replies"`
}

// quickReplyStore holds the daemon-wide list: in a state file when the daemon
// persists state, otherwise in memory.
type quickReplyStore struct {
	mu   sync.Mutex
	path string
	mem  []QuickReply
	set  bool // mem holds an edited list (memory-only store)
}

// SetQuickRepliesFile persists the daemon-wide quick replies at path.
func (m *Manager) SetQuickRepliesFile(path string) {
	m.quickReplies.mu.Lock()
	m.quickReplies.path = path
	m.quickReplies.mu.Unlock()
}

// GlobalQuickReplies returns the daemon-wide list (the defaults until edited).
func (m *Manager) GlobalQuickReplies() ([]QuickReply, error) {
	s := &m.quickReplies
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.path == "" {
		if !s.set {
			return append([]QuickReply(nil), DefaultQuickReplies...), nil
		}
		return append([]QuickReply(nil), s.mem...), nil
	}
	replies, ok, err := readQuickReplies(s.path)
	if err != nil {
		return nil, err
	}
	if !ok {
		return append([]QuickReply(nil), DefaultQuickReplies...), nil
	}
	return replies, nil
}

// SetGlobalQuickReplies validates and replaces the daemon-wide list.
func (m *Manager) SetGlobalQuickReplies(replies []QuickReply) ([]QuickReply, error) {
	clean, err := cleanQuickReplies(replies, true)
	if err != nil {
		return nil, err
	}
	s := &m.quickReplies
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.path == "" {
		s.mem, s.set = clean, true
		return append([]QuickReply(nil), clean...), nil
	}
	if err := writeQuickReplies(s.path, clean); err != nil {
		return nil, err
	}
	return clean, nil
}

// QuickReplies returns the project's own replies from its root workspace's
// .ycc/quick-replies.json; a missing file means none.
func (m *Manager) QuickReplies(project string) ([]QuickReply, error) {
	workspace, err := m.resolveProjectWorkspace(project)
	if err != nil {
		return nil, err
	}
	replies, _, err := readQuickReplies(filepath.Join(workspace, ".ycc", quickRepliesFile))
	return replies, err
}

// SetQuickReplies validates and replaces the project's own replies, returning
// the stored (trimmed) list.
func (m *Manager) SetQuickReplies(project string, replies []QuickReply) ([]QuickReply, error) {
	clean, err := cleanQuickReplies(replies, false)
	if err != nil {
		return nil, err
	}
	workspace, err := m.resolveProjectWorkspace(project)
	if err != nil {
		return nil, err
	}
	if err := writeQuickReplies(filepath.Join(workspace, ".ycc", quickRepliesFile), clean); err != nil {
		return nil, err
	}
	return clean, nil
}

func cleanQuickReplies(replies []QuickReply, keepDefaultIDs bool) ([]QuickReply, error) {
	if len(replies) > maxQuickReplies {
		return nil, fmt.Errorf("%w: at most %d replies", ErrInvalidQuickReplies, maxQuickReplies)
	}
	clean := make([]QuickReply, 0, len(replies))
	for i, r := range replies {
		label, text, icon := strings.TrimSpace(r.Label), strings.TrimSpace(r.Text), strings.TrimSpace(r.Icon)
		switch {
		case label == "" || text == "":
			return nil, fmt.Errorf("%w: reply %d needs a label and a message", ErrInvalidQuickReplies, i+1)
		case utf8.RuneCountInString(label) > maxQuickReplyLabel:
			return nil, fmt.Errorf("%w: label %q is longer than %d characters", ErrInvalidQuickReplies, label, maxQuickReplyLabel)
		case len(text) > maxQuickReplyTextLen:
			return nil, fmt.Errorf("%w: the message for %q is longer than %d bytes", ErrInvalidQuickReplies, label, maxQuickReplyTextLen)
		case !validQuickReplyIcon(icon):
			return nil, fmt.Errorf("%w: icon %q for %q is not a name of up to %d lowercase letters, digits or underscores", ErrInvalidQuickReplies, icon, label, maxQuickReplyIcon)
		}
		id := ""
		if keepDefaultIDs && isDefaultQuickReplyID(r.ID) {
			id = r.ID
		}
		clean = append(clean, QuickReply{ID: id, Label: label, Text: text, Icon: icon})
	}
	return clean, nil
}

// validQuickReplyIcon accepts an empty icon or a short snake_case name; the
// daemon does not know the clients' icon set, so unknown names are kept.
func validQuickReplyIcon(icon string) bool {
	if len(icon) > maxQuickReplyIcon {
		return false
	}
	for _, c := range icon {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_') {
			return false
		}
	}
	return true
}

func isDefaultQuickReplyID(id string) bool {
	for _, d := range DefaultQuickReplies {
		if d.ID == id {
			return true
		}
	}
	return false
}

// readQuickReplies reads a quick-replies file; ok is false when it is missing.
func readQuickReplies(path string) (replies []QuickReply, ok bool, err error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	var doc quickRepliesDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, false, fmt.Errorf("read quick replies: %w", err)
	}
	if doc.Replies == nil {
		doc.Replies = []QuickReply{}
	}
	return doc.Replies, true, nil
}

// writeQuickReplies atomically replaces a quick-replies file.
func writeQuickReplies(path string, replies []QuickReply) error {
	data, err := json.MarshalIndent(quickRepliesDoc{Replies: replies}, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".quick-replies-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, writeErr := f.Write(data)
	if writeErr == nil {
		writeErr = f.Sync()
	}
	if closeErr := f.Close(); writeErr == nil {
		writeErr = closeErr
	}
	if writeErr != nil {
		return writeErr
	}
	return os.Rename(f.Name(), path)
}
