package session

import (
	"testing"

	"github.com/whyrusleeping/ycc/internal/engine"
	"github.com/whyrusleeping/ycc/internal/event"
)

// Pictures attached to the OPENING prompt (StartSession's images, spec §12) seed
// the first coordinator loop as native image blocks alongside the prompt text.
func TestPromptImagesSeedFirstLoop(t *testing.T) {
	m := NewManager(testRegistry(), t.TempDir())
	log, err := event.OpenLog(t.TempDir() + "/events.jsonl")
	if err != nil {
		t.Fatalf("OpenLog: %v", err)
	}
	defer log.Close()

	s, err := m.newSession(t.TempDir(), "s_img", "chat", false, "what is this?", log, false, "")
	if err != nil {
		t.Fatalf("newSession: %v", err)
	}
	s.promptImages = []engine.Image{{Base64: "AAAA", MediaType: "image/png", Filename: "shot.png"}}

	loop, err := s.buildLoop("chat", "what is this?")
	if err != nil {
		t.Fatalf("buildLoop: %v", err)
	}
	hist := loop.History()
	if len(hist) != 1 || hist[0].Role != "user" {
		t.Fatalf("history = %+v, want one user message", hist)
	}
	blocks := hist[0].MultiContent
	if len(blocks) != 2 || blocks[0].Type != "text" || blocks[0].Text != "what is this?" {
		t.Fatalf("blocks = %+v, want text + image", blocks)
	}
	if blocks[1].Type != "image" || blocks[1].ImageBase64 != "AAAA" || blocks[1].ImageMediaType != "image/png" {
		t.Fatalf("image block = %+v", blocks[1])
	}

	// A later mode transition rebuilds the loop with the same code path; the
	// opening pictures must not be re-attached to the fresh history.
	next, err := s.buildLoop("work", "now implement it")
	if err != nil {
		t.Fatalf("buildLoop(work): %v", err)
	}
	nextHist := next.History()
	if len(nextHist) != 1 || nextHist[0].Content != "now implement it" || len(nextHist[0].MultiContent) != 0 {
		t.Fatalf("transition history = %+v, want text-only seed", nextHist)
	}
}

// The initial user_input event records picture METADATA only — events.jsonl
// never stores image bytes (the same boundary SendInputMessage keeps).
func TestPromptImageMetadataOnly(t *testing.T) {
	meta := imageMetadata([]engine.Image{
		{Base64: "SECRETBYTES", MediaType: "image/jpeg", Filename: "a.jpg"},
		{Base64: "MOREBYTES", MediaType: "image/png", Filename: "b.png"},
	})
	if len(meta) != 2 {
		t.Fatalf("meta = %+v", meta)
	}
	for _, entry := range meta {
		if _, ok := entry["base64"]; ok {
			t.Fatalf("payload leaked into metadata: %+v", entry)
		}
		if len(entry) != 2 || entry["media_type"] == "" || entry["filename"] == "" {
			t.Fatalf("entry = %+v, want media_type + filename", entry)
		}
	}
	if imageMetadata(nil) != nil {
		t.Fatal("no images must omit the field entirely")
	}
}
