// Package projectfs implements read-only, root-confined file browsing for
// remote clients (the ListFiles / ReadFile RPCs). Every request path is
// relative to a root directory (a project checkout or a live workstream
// worktree); absolute paths, `..` escapes, symlinks resolving outside the root,
// and anything inside a `.git` directory are refused. Nothing here writes.
package projectfs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// Sentinel errors, mapped to RPC codes by the server.
var (
	// ErrInvalidPath: absolute, `..`-escaping, or otherwise malformed path.
	ErrInvalidPath = errors.New("invalid path")
	// ErrDenied: the path resolves outside the root or into a .git directory.
	ErrDenied = errors.New("path is outside the browsable root")
	// ErrNotFound: nothing exists at the path.
	ErrNotFound = errors.New("no such file or directory")
	// ErrNotDir: List was given a file.
	ErrNotDir = errors.New("not a directory")
	// ErrNotFile: Read was given a directory or a special file.
	ErrNotFile = errors.New("not a regular file")
)

const (
	// DefaultMaxEntries caps a directory listing.
	DefaultMaxEntries = 5000
	// DefaultMaxBytes is the default Read cap (text is truncated at a line
	// boundary; images over the cap are not returned at all).
	DefaultMaxBytes = 1 << 20
	// MaxBytesLimit bounds a caller-requested cap.
	MaxBytesLimit = 8 << 20
	// sniffLen is how much of a file is inspected for binary detection.
	sniffLen = 8 << 10
	// gitTimeout bounds the gitignore probe so a wedged git never stalls a
	// listing.
	gitTimeout = 5 * time.Second
)

// Entry is one directory-listing row.
type Entry struct {
	Name      string
	IsDir     bool // follows symlinks
	IsSymlink bool
	Ignored   bool // matched by .gitignore (tracked files never are)
	Size      int64
	ModTime   time.Time
}

// Listing is a directory's visible contents.
type Listing struct {
	Path      string // cleaned root-relative path, "/"-separated; "" = root
	Entries   []Entry
	Truncated bool
}

// File is a (possibly truncated) file read.
type File struct {
	Path      string // cleaned root-relative path, "/"-separated
	Data      []byte // nil for non-image binaries and over-cap images
	Size      int64  // full on-disk size
	MediaType string
	IsBinary  bool
	Truncated bool
	ModTime   time.Time
}

// CleanRel validates and normalizes a root-relative request path. It returns
// "" for the root. Absolute paths and paths escaping via `..` are rejected
// with ErrInvalidPath; any `.git` component is rejected with ErrDenied.
func CleanRel(p string) (string, error) {
	p = strings.TrimSpace(p)
	if strings.ContainsRune(p, 0) {
		return "", fmt.Errorf("%w: contains NUL", ErrInvalidPath)
	}
	p = filepath.FromSlash(p)
	if filepath.IsAbs(p) || filepath.VolumeName(p) != "" {
		return "", fmt.Errorf("%w: must be relative to the project root", ErrInvalidPath)
	}
	c := filepath.Clean(p)
	if c == "." {
		return "", nil
	}
	if c == ".." || strings.HasPrefix(c, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: escapes the project root", ErrInvalidPath)
	}
	if hasGitComponent(c) {
		return "", fmt.Errorf("%w: .git internals are not browsable", ErrDenied)
	}
	return c, nil
}

func hasGitComponent(rel string) bool {
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if part == ".git" {
			return true
		}
	}
	return false
}

// resolve maps a request path to its real (symlink-resolved) absolute path,
// enforcing confinement to root. It returns the cleaned relative path too.
func resolve(root, p string) (rel, real string, err error) {
	rel, err = CleanRel(p)
	if err != nil {
		return "", "", err
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", "", fmt.Errorf("%w: root %s", ErrNotFound, root)
		}
		return "", "", err
	}
	real, err = filepath.EvalSymlinks(filepath.Join(realRoot, rel))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", "", fmt.Errorf("%w: %s", ErrNotFound, filepath.ToSlash(rel))
		}
		if errors.Is(err, fs.ErrPermission) {
			return "", "", fmt.Errorf("%w: %v", ErrDenied, err)
		}
		return "", "", err
	}
	inside, err := filepath.Rel(realRoot, real)
	if err != nil || inside == ".." || strings.HasPrefix(inside, ".."+string(filepath.Separator)) || filepath.IsAbs(inside) {
		return "", "", fmt.Errorf("%w: %s", ErrDenied, filepath.ToSlash(rel))
	}
	if inside != "." && hasGitComponent(inside) {
		return "", "", fmt.Errorf("%w: .git internals are not browsable", ErrDenied)
	}
	return rel, real, nil
}

// List returns the visible entries of a root-relative directory: dotfiles and
// dot-directories are omitted, directories sort before files, then names
// case-insensitively. maxEntries <= 0 uses DefaultMaxEntries.
func List(ctx context.Context, root, p string, maxEntries int) (Listing, error) {
	if maxEntries <= 0 {
		maxEntries = DefaultMaxEntries
	}
	rel, real, err := resolve(root, p)
	if err != nil {
		return Listing{}, err
	}
	info, err := os.Stat(real)
	if err != nil {
		return Listing{}, statErr(err, rel)
	}
	if !info.IsDir() {
		return Listing{}, fmt.Errorf("%w: %s", ErrNotDir, filepath.ToSlash(rel))
	}
	dirents, err := os.ReadDir(real)
	if err != nil {
		return Listing{}, statErr(err, rel)
	}
	entries := make([]Entry, 0, len(dirents))
	for _, de := range dirents {
		name := de.Name()
		if name == "" || name[0] == '.' {
			continue
		}
		e := Entry{Name: name, IsSymlink: de.Type()&fs.ModeSymlink != 0}
		full := filepath.Join(real, name)
		// Follow symlinks for type/size; a dangling link lists as a file.
		fi, err := os.Stat(full)
		if err != nil {
			fi, err = os.Lstat(full)
		}
		if err == nil {
			e.IsDir = fi.IsDir()
			e.ModTime = fi.ModTime()
			if !e.IsDir {
				e.Size = fi.Size()
			}
		}
		entries = append(entries, e)
	}
	sort.Slice(entries, func(i, j int) bool {
		a, b := entries[i], entries[j]
		if a.IsDir != b.IsDir {
			return a.IsDir
		}
		la, lb := strings.ToLower(a.Name), strings.ToLower(b.Name)
		if la != lb {
			return la < lb
		}
		return a.Name < b.Name
	})
	truncated := false
	if len(entries) > maxEntries {
		entries = entries[:maxEntries]
		truncated = true
	}
	markIgnored(ctx, real, entries)
	return Listing{Path: filepath.ToSlash(rel), Entries: entries, Truncated: truncated}, nil
}

// markIgnored sets Ignored on entries git would ignore in dir. Non-repos and
// git failures leave every entry unmarked (best effort, never an error).
func markIgnored(ctx context.Context, dir string, entries []Entry) {
	if len(entries) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()
	var in bytes.Buffer
	for _, e := range entries {
		in.WriteString(e.Name)
		in.WriteByte(0)
	}
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "check-ignore", "-z", "--stdin")
	cmd.Stdin = &in
	out, err := cmd.Output()
	// Exit status 1 means "nothing ignored"; anything else (128: not a repo,
	// git missing, timeout) yields nothing worth trusting. Either way no
	// entry is marked.
	if err != nil {
		return
	}
	ignored := make(map[string]bool)
	for _, name := range bytes.Split(out, []byte{0}) {
		if len(name) > 0 {
			ignored[string(name)] = true
		}
	}
	for i := range entries {
		entries[i].Ignored = ignored[entries[i].Name]
	}
}

// Read returns a root-relative regular file. Text over maxBytes is cut at the
// last line boundary within the cap and flagged Truncated; binary files carry
// no data except images within the cap. maxBytes <= 0 uses DefaultMaxBytes and
// larger requests are clamped to MaxBytesLimit.
func Read(root, p string, maxBytes int64) (File, error) {
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}
	if maxBytes > MaxBytesLimit {
		maxBytes = MaxBytesLimit
	}
	rel, real, err := resolve(root, p)
	if err != nil {
		return File{}, err
	}
	if rel == "" {
		return File{}, fmt.Errorf("%w: the project root is a directory", ErrNotFile)
	}
	info, err := os.Stat(real)
	if err != nil {
		return File{}, statErr(err, rel)
	}
	// Refuse directories AND special files: opening a FIFO would block.
	if !info.Mode().IsRegular() {
		return File{}, fmt.Errorf("%w: %s", ErrNotFile, filepath.ToSlash(rel))
	}
	f, err := os.Open(real)
	if err != nil {
		return File{}, statErr(err, rel)
	}
	defer f.Close()

	out := File{Path: filepath.ToSlash(rel), Size: info.Size(), ModTime: info.ModTime()}
	head := make([]byte, sniffLen)
	n, err := io.ReadFull(f, head)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return File{}, fmt.Errorf("read %s: %w", out.Path, err)
	}
	head = head[:n]
	ct := http.DetectContentType(head)
	isImage := strings.HasPrefix(ct, "image/")
	out.IsBinary = isImage || bytes.IndexByte(head, 0) >= 0

	switch {
	case isImage:
		out.MediaType = ct
		if out.Size > maxBytes {
			out.Truncated = true
			return out, nil
		}
		rest, err := io.ReadAll(io.LimitReader(f, maxBytes-int64(n)))
		if err != nil {
			return File{}, fmt.Errorf("read %s: %w", out.Path, err)
		}
		out.Data = append(head, rest...)
		return out, nil
	case out.IsBinary:
		if ct == "text/plain; charset=utf-8" {
			ct = "application/octet-stream"
		}
		out.MediaType = ct
		return out, nil
	}

	out.MediaType = "text/plain; charset=utf-8"
	var data []byte
	if int64(n) > maxBytes {
		data = head[:maxBytes]
		out.Truncated = true
	} else {
		// Read one byte past the cap to detect truncation even when the
		// on-disk size changed under us.
		rest, err := io.ReadAll(io.LimitReader(f, maxBytes-int64(n)+1))
		if err != nil {
			return File{}, fmt.Errorf("read %s: %w", out.Path, err)
		}
		data = append(head, rest...)
		if int64(len(data)) > maxBytes {
			data = data[:maxBytes]
			out.Truncated = true
		}
	}
	if out.Truncated {
		// Cut at a line boundary so the client never renders a half line
		// (and never a split UTF-8 sequence).
		if nl := bytes.LastIndexByte(data, '\n'); nl > 0 {
			data = data[:nl+1]
		} else {
			// One enormous line (minified output): at least avoid ending
			// mid-rune.
			for i := 0; i < utf8.UTFMax && len(data) > 0 && !utf8.Valid(data); i++ {
				data = data[:len(data)-1]
			}
		}
	}
	out.Data = data
	return out, nil
}

func statErr(err error, rel string) error {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("%w: %s", ErrNotFound, filepath.ToSlash(rel))
	case errors.Is(err, fs.ErrPermission):
		return fmt.Errorf("%w: %v", ErrDenied, err)
	}
	return err
}
