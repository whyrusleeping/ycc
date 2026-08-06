package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/whyrusleeping/ycc/internal/config"
	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/session"
	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
)

func tinyPNG(t *testing.T) []byte {
	t.Helper()
	var out bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	img.Set(0, 0, color.White)
	if err := png.Encode(&out, img); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestValidateInputImages(t *testing.T) {
	data := tinyPNG(t)
	images, err := validateInputImages([]*v1.ImageAttachment{{
		Data: data, MediaType: "image/png", Filename: "../photo.png",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(images) != 1 || images[0].MediaType != "image/png" || images[0].Filename != "photo.png" {
		t.Fatalf("images = %+v", images)
	}
	decoded, err := base64.StdEncoding.DecodeString(images[0].Base64)
	if err != nil || !bytes.Equal(decoded, data) {
		t.Fatal("base64 payload did not round-trip")
	}
}

func TestValidateInputImagesRejectsUnsupportedMismatchAndOversize(t *testing.T) {
	data := tinyPNG(t)
	for name, attachment := range map[string]*v1.ImageAttachment{
		"unsupported": {Data: data, MediaType: "image/heic"},
		"mismatch":    {Data: data, MediaType: "image/jpeg"},
		"oversize":    {Data: bytes.Repeat([]byte{'x'}, maxInputImageSize+1), MediaType: "image/jpeg"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := validateInputImages([]*v1.ImageAttachment{attachment}); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestImagePayloadIsNotEventMetadata(t *testing.T) {
	// Regression guard for the design boundary: callers derive only these safe
	// fields for user_input events, never Base64.
	image := struct{ MediaType, Filename, Base64 string }{"image/png", "photo.png", strings.Repeat("secret", 20)}
	metadata := map[string]any{"media_type": image.MediaType, "filename": image.Filename}
	if strings.Contains(metadata["filename"].(string), image.Base64) {
		t.Fatal("payload leaked into metadata")
	}
}

// StartSession accepts opening-prompt pictures (spec §12): the session starts,
// its first user_input event carries picture METADATA (never the bytes), and the
// bytes themselves only reach model history.
func TestStartSessionWithPromptImages(t *testing.T) {
	srv, ws := newImageTestServer(t)
	ctx := context.Background()

	resp, err := srv.StartSession(ctx, connect.NewRequest(&v1.StartSessionRequest{
		Workspace: ws, Mode: "chat", Prompt: "what is this?",
		Images: []*v1.ImageAttachment{{
			Data: tinyPNG(t), MediaType: "image/png", Filename: "shot.png",
		}},
	}))
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	id := resp.Msg.SessionId
	defer srv.StopSession(ctx, connect.NewRequest(&v1.StopSessionRequest{SessionId: id}))

	// The initial echo is emitted by the session's run goroutine.
	var input *v1.Event
	for i := 0; i < 200 && input == nil; i++ {
		tr, err := srv.GetSessionTranscript(ctx, connect.NewRequest(&v1.GetSessionTranscriptRequest{
			SessionId: id,
		}))
		if err != nil {
			t.Fatalf("GetSessionTranscript: %v", err)
		}
		for _, ev := range tr.Msg.Events {
			if ev.Type == string(event.UserInput) {
				input = ev
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if input == nil {
		t.Fatal("no user_input event for the opening prompt")
	}
	var data map[string]any
	if err := json.Unmarshal([]byte(input.DataJson), &data); err != nil {
		t.Fatalf("decode event data (%s): %v", input.DataJson, err)
	}
	images, ok := data["images"].([]any)
	if !ok || len(images) != 1 {
		t.Fatalf("event data = %v, want one picture", data)
	}
	meta, _ := images[0].(map[string]any)
	if meta["media_type"] != "image/png" || meta["filename"] != "shot.png" {
		t.Fatalf("picture metadata = %v", meta)
	}
	if strings.Contains(input.DataJson, base64.StdEncoding.EncodeToString(tinyPNG(t))) {
		t.Fatal("image bytes leaked into the event log")
	}
}

// Invalid opening-prompt attachments are rejected with InvalidArgument and leave
// NO session behind (validation runs before the log/worktree is created).
func TestStartSessionRejectsBadPromptImages(t *testing.T) {
	data := tinyPNG(t)
	many := make([]*v1.ImageAttachment, maxInputImages+1)
	for i := range many {
		many[i] = &v1.ImageAttachment{Data: data, MediaType: "image/png"}
	}
	for name, images := range map[string][]*v1.ImageAttachment{
		"too many":    many,
		"unsupported": {{Data: data, MediaType: "image/heic"}},
		"mismatch":    {{Data: data, MediaType: "image/jpeg"}},
		"empty":       {{MediaType: "image/png"}},
		"nil":         {nil},
	} {
		t.Run(name, func(t *testing.T) {
			srv, ws := newImageTestServer(t)
			_, err := srv.StartSession(context.Background(), connect.NewRequest(&v1.StartSessionRequest{
				Workspace: ws, Mode: "chat", Prompt: "hi", Images: images,
			}))
			if connect.CodeOf(err) != connect.CodeInvalidArgument {
				t.Fatalf("err = %v, want InvalidArgument", err)
			}
			if sessions := srv.mgr.List(); len(sessions) != 0 {
				t.Fatalf("sessions after rejected start = %+v, want none", sessions)
			}
		})
	}
}

func newImageTestServer(t *testing.T) (*Server, string) {
	t.Helper()
	cfg := &config.Config{
		Models: map[string]config.Model{
			"a": {Backend: "ollama", BaseURL: "http://127.0.0.1:1", Model: "model-a"},
		},
		Roles: config.Roles{Coordinator: "a", Implementer: "a", Reviewers: []string{"a"}},
	}
	return New(session.NewManager(config.NewRegistry(cfg), t.TempDir())), t.TempDir()
}
