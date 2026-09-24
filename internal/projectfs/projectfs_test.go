package projectfs

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func write(t *testing.T, root, rel string, data []byte) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// pngHeader is enough for http.DetectContentType to say image/png.
var pngHeader = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")

func TestCleanRel(t *testing.T) {
	ok := map[string]string{
		"":            "",
		".":           "",
		"./":          "",
		"a/b.go":      filepath.Join("a", "b.go"),
		"./a//b/../c": filepath.Join("a", "c"),
		" spec.md ":   "spec.md",
		".github/x":   filepath.Join(".github", "x"),
	}
	for in, want := range ok {
		got, err := CleanRel(in)
		if err != nil || got != want {
			t.Errorf("CleanRel(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	invalid := []string{"/etc/passwd", "..", "../x", "a/../../x", "a\x00b"}
	for _, in := range invalid {
		if _, err := CleanRel(in); !errors.Is(err, ErrInvalidPath) {
			t.Errorf("CleanRel(%q) err = %v; want ErrInvalidPath", in, err)
		}
	}
	for _, in := range []string{".git", ".git/config", "sub/.git/HEAD"} {
		if _, err := CleanRel(in); !errors.Is(err, ErrDenied) {
			t.Errorf("CleanRel(%q) err = %v; want ErrDenied", in, err)
		}
	}
}

func TestListOrderingHiddenAndTypes(t *testing.T) {
	root := t.TempDir()
	write(t, root, "b.go", []byte("package b\n"))
	write(t, root, "A.md", []byte("# a\n"))
	write(t, root, "zdir/x.txt", []byte("x"))
	write(t, root, "adir/y.txt", []byte("y"))
	write(t, root, ".hidden", []byte("h"))
	write(t, root, ".ycc/sessions/s/events.jsonl", []byte("{}"))
	if err := os.Symlink("zdir", filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}

	l, err := List(context.Background(), root, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range l.Entries {
		names = append(names, e.Name)
	}
	if got, want := strings.Join(names, ","), "adir,link,zdir,A.md,b.go"; got != want {
		t.Fatalf("names = %s; want %s", got, want)
	}
	for _, e := range l.Entries {
		switch e.Name {
		case "link":
			if !e.IsDir || !e.IsSymlink {
				t.Errorf("link: %+v", e)
			}
		case "b.go":
			if e.IsDir || e.Size != int64(len("package b\n")) || e.ModTime.IsZero() {
				t.Errorf("b.go: %+v", e)
			}
		}
	}
	if l.Path != "" || l.Truncated {
		t.Errorf("listing meta: %+v", l)
	}

	sub, err := List(context.Background(), root, "zdir", 0)
	if err != nil || sub.Path != "zdir" || len(sub.Entries) != 1 {
		t.Fatalf("sub listing: %+v, %v", sub, err)
	}

	capped, err := List(context.Background(), root, "", 2)
	if err != nil || len(capped.Entries) != 2 || !capped.Truncated {
		t.Fatalf("capped: %+v, %v", capped, err)
	}
}

func TestListErrors(t *testing.T) {
	root := t.TempDir()
	write(t, root, "f.txt", []byte("x"))
	ctx := context.Background()
	if _, err := List(ctx, root, "missing", 0); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing: %v", err)
	}
	if _, err := List(ctx, root, "f.txt", 0); !errors.Is(err, ErrNotDir) {
		t.Errorf("file: %v", err)
	}
	if _, err := List(ctx, root, "../", 0); !errors.Is(err, ErrInvalidPath) {
		t.Errorf("escape: %v", err)
	}
}

func TestListMarksGitignored(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	write(t, root, ".gitignore", []byte("build/\n*.log\ntracked.log\n"))
	write(t, root, "build/out.bin", []byte("x"))
	write(t, root, "debug.log", []byte("x"))
	write(t, root, "tracked.log", []byte("x"))
	write(t, root, "main.go", []byte("package main\n"))
	write(t, root, "sub/deep.log", []byte("x"))
	write(t, root, "sub/keep.go", []byte("x"))
	run("add", "-f", "tracked.log")

	l, err := List(context.Background(), root, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, e := range l.Entries {
		got[e.Name] = e.Ignored
	}
	want := map[string]bool{"build": true, "debug.log": true, "tracked.log": false, "main.go": false, "sub": false}
	for name, ign := range want {
		if got[name] != ign {
			t.Errorf("%s ignored = %v; want %v", name, got[name], ign)
		}
	}

	sub, err := List(context.Background(), root, "sub", 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range sub.Entries {
		if want := e.Name == "deep.log"; e.Ignored != want {
			t.Errorf("sub/%s ignored = %v; want %v", e.Name, e.Ignored, want)
		}
	}
}

func TestListNonRepoHasNoIgnored(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.log", []byte("x"))
	l, err := List(context.Background(), root, "", 0)
	if err != nil || len(l.Entries) != 1 || l.Entries[0].Ignored {
		t.Fatalf("%+v, %v", l, err)
	}
}

func TestSymlinkConfinement(t *testing.T) {
	outside := t.TempDir()
	write(t, outside, "secret.txt", []byte("secret"))
	root := t.TempDir()
	write(t, root, "ok.txt", []byte("ok"))
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(root, "esc.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escdir")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("ok.txt", filepath.Join(root, "in.txt")); err != nil {
		t.Fatal(err)
	}
	write(t, root, ".git/config", []byte("[core]"))
	if err := os.Symlink(".git/config", filepath.Join(root, "gitcfg")); err != nil {
		t.Fatal(err)
	}

	for _, p := range []string{"esc.txt", "escdir/secret.txt", "gitcfg"} {
		if _, err := Read(root, p, 0); !errors.Is(err, ErrDenied) {
			t.Errorf("Read(%s) err = %v; want ErrDenied", p, err)
		}
	}
	if _, err := List(context.Background(), root, "escdir", 0); !errors.Is(err, ErrDenied) {
		t.Errorf("List(escdir) err = %v; want ErrDenied", err)
	}
	f, err := Read(root, "in.txt", 0)
	if err != nil || string(f.Data) != "ok" || f.Path != "in.txt" {
		t.Fatalf("in-root symlink: %+v, %v", f, err)
	}

	// A root that is itself reached through a symlink still works.
	linkedRoot := filepath.Join(t.TempDir(), "linked")
	if err := os.Symlink(root, linkedRoot); err != nil {
		t.Fatal(err)
	}
	if f, err := Read(linkedRoot, "ok.txt", 0); err != nil || string(f.Data) != "ok" {
		t.Fatalf("symlinked root: %+v, %v", f, err)
	}
}

func TestReadText(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a/b.go", []byte("package b\n\nfunc F() {}\n"))
	f, err := Read(root, "a/b.go", 0)
	if err != nil {
		t.Fatal(err)
	}
	if f.Path != "a/b.go" || f.IsBinary || f.Truncated || f.Size != int64(len(f.Data)) ||
		!strings.HasPrefix(f.MediaType, "text/plain") || f.ModTime.IsZero() {
		t.Fatalf("%+v", f)
	}
	empty := filepath.Join(root, "empty")
	if err := os.WriteFile(empty, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if f, err := Read(root, "empty", 0); err != nil || len(f.Data) != 0 || f.IsBinary {
		t.Fatalf("empty: %+v, %v", f, err)
	}
}

func TestReadTruncatesAtLineBoundary(t *testing.T) {
	root := t.TempDir()
	var buf bytes.Buffer
	for i := 0; i < 5000; i++ {
		buf.WriteString("line of text\n") // 13 bytes
	}
	write(t, root, "big.txt", buf.Bytes())
	for _, cap := range []int64{100, 20000} { // below and above the sniff window
		f, err := Read(root, "big.txt", cap)
		if err != nil {
			t.Fatal(err)
		}
		if !f.Truncated || int64(len(f.Data)) > cap || !bytes.HasSuffix(f.Data, []byte("\n")) ||
			len(f.Data)%13 != 0 || f.Size != int64(buf.Len()) {
			t.Fatalf("cap %d: truncated=%v len=%d", cap, f.Truncated, len(f.Data))
		}
	}
	// Single long line with multibyte runes: never ends mid-rune.
	write(t, root, "wide.txt", bytes.Repeat([]byte("é"), 100))
	f, err := Read(root, "wide.txt", 51)
	if err != nil || !f.Truncated || len(f.Data) != 50 {
		t.Fatalf("wide: len=%d %+v %v", len(f.Data), f.Truncated, err)
	}
	// Exactly at the cap is not truncated.
	write(t, root, "exact.txt", []byte("0123456789"))
	if f, err := Read(root, "exact.txt", 10); err != nil || f.Truncated || len(f.Data) != 10 {
		t.Fatalf("exact: %+v %v", f, err)
	}
}

func TestReadBinaryAndImages(t *testing.T) {
	root := t.TempDir()
	write(t, root, "blob.bin", []byte("abc\x00def"))
	f, err := Read(root, "blob.bin", 0)
	if err != nil || !f.IsBinary || f.Data != nil || f.Size != 7 || f.MediaType != "application/octet-stream" {
		t.Fatalf("binary: %+v %v", f, err)
	}

	img := append(append([]byte{}, pngHeader...), bytes.Repeat([]byte{1}, 100)...)
	write(t, root, "pic.png", img)
	f, err = Read(root, "pic.png", 0)
	if err != nil || !f.IsBinary || f.MediaType != "image/png" || !bytes.Equal(f.Data, img) || f.Truncated {
		t.Fatalf("image: %+v %v", f.MediaType, err)
	}
	f, err = Read(root, "pic.png", 50)
	if err != nil || !f.Truncated || f.Data != nil {
		t.Fatalf("over-cap image: truncated=%v len=%d %v", f.Truncated, len(f.Data), err)
	}
}

func TestReadErrors(t *testing.T) {
	root := t.TempDir()
	write(t, root, "d/x", []byte("x"))
	cases := map[string]error{
		"":           ErrNotFile,
		"d":          ErrNotFile,
		"missing":    ErrNotFound,
		"/etc/hosts": ErrInvalidPath,
		"../x":       ErrInvalidPath,
		".git/HEAD":  ErrDenied,
	}
	for p, want := range cases {
		if _, err := Read(root, p, 0); !errors.Is(err, want) {
			t.Errorf("Read(%q) err = %v; want %v", p, err, want)
		}
	}
	fifo := filepath.Join(root, "pipe")
	if err := syscall.Mkfifo(fifo, 0o644); err == nil {
		if _, err := Read(root, "pipe", 0); !errors.Is(err, ErrNotFile) {
			t.Errorf("fifo err = %v; want ErrNotFile", err)
		}
	}
	if _, err := Read(filepath.Join(root, "nope"), "x", 0); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing root: %v", err)
	}
}
