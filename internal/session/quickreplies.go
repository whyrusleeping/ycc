package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// QuickReply is a canned message a client offers as a one-gesture send.
// ID is set only on built-ins.
type QuickReply struct {
	ID    string `json:"-"`
	Label string `json:"label"`
	Text  string `json:"text"`
}

// BuiltinQuickReplies are offered in every project, ahead of its own replies.
var BuiltinQuickReplies = []QuickReply{
	{ID: "go_ahead", Label: "Go ahead", Text: "Go ahead"},
	{ID: "continue", Label: "Continue", Text: "Continue"},
	{ID: "commit_push", Label: "Commit & push", Text: "Commit and push"},
}

const (
	quickRepliesFile     = "quick-replies.json"
	maxQuickReplies      = 12
	maxQuickReplyLabel   = 32
	maxQuickReplyTextLen = 4000
)

// ErrInvalidQuickReplies reports a SetQuickReplies list that fails validation.
var ErrInvalidQuickReplies = errors.New("invalid quick replies")

type quickRepliesDoc struct {
	Replies []QuickReply `json:"replies"`
}

// QuickReplies returns the project's own replies from its root workspace's
// .ycc/quick-replies.json; a missing file means none.
func (m *Manager) QuickReplies(project string) ([]QuickReply, error) {
	workspace, err := m.resolveProjectWorkspace(project)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(filepath.Join(workspace, ".ycc", quickRepliesFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var doc quickRepliesDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("read quick replies: %w", err)
	}
	return doc.Replies, nil
}

// SetQuickReplies validates and replaces the project's own replies, returning
// the stored (trimmed) list.
func (m *Manager) SetQuickReplies(project string, replies []QuickReply) ([]QuickReply, error) {
	if len(replies) > maxQuickReplies {
		return nil, fmt.Errorf("%w: at most %d replies", ErrInvalidQuickReplies, maxQuickReplies)
	}
	clean := make([]QuickReply, 0, len(replies))
	for i, r := range replies {
		label, text := strings.TrimSpace(r.Label), strings.TrimSpace(r.Text)
		switch {
		case label == "" || text == "":
			return nil, fmt.Errorf("%w: reply %d needs a label and a message", ErrInvalidQuickReplies, i+1)
		case utf8.RuneCountInString(label) > maxQuickReplyLabel:
			return nil, fmt.Errorf("%w: label %q is longer than %d characters", ErrInvalidQuickReplies, label, maxQuickReplyLabel)
		case len(text) > maxQuickReplyTextLen:
			return nil, fmt.Errorf("%w: the message for %q is longer than %d bytes", ErrInvalidQuickReplies, label, maxQuickReplyTextLen)
		}
		clean = append(clean, QuickReply{Label: label, Text: text})
	}
	workspace, err := m.resolveProjectWorkspace(project)
	if err != nil {
		return nil, err
	}
	data, err := json.MarshalIndent(quickRepliesDoc{Replies: clean}, "", "  ")
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(workspace, ".ycc")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	f, err := os.CreateTemp(dir, ".quick-replies-*")
	if err != nil {
		return nil, err
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
		return nil, writeErr
	}
	if err := os.Rename(f.Name(), filepath.Join(dir, quickRepliesFile)); err != nil {
		return nil, err
	}
	return clean, nil
}
