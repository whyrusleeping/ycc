package docs

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// InstructionFileNames are the workspace-root project instruction files, in
// precedence order: the first one that yields content is used.
var InstructionFileNames = []string{"AGENTS.md", "CLAUDE.md"}

// MaxProjectInstructions caps the instruction bytes injected into a prompt.
const MaxProjectInstructions = 32 << 10

// ProjectInstructions is the committed instruction file picked up for a workspace.
type ProjectInstructions struct {
	Name      string // workspace-relative file name, e.g. "AGENTS.md"
	Content   string // at most MaxProjectInstructions bytes plus a truncation marker
	Size      int64  // the file's full size in bytes
	Truncated bool
}

// LoadProjectInstructions returns the first usable instruction file at the
// workspace root. Missing, empty, unreadable, non-regular files and symlinks
// resolving outside the workspace are skipped; ok is false when none is usable.
func LoadProjectInstructions(root string) (ProjectInstructions, bool) {
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return ProjectInstructions{}, false
	}
	for _, name := range InstructionFileNames {
		if pi, ok := loadInstructionFile(realRoot, name); ok {
			return pi, true
		}
	}
	return ProjectInstructions{}, false
}

func loadInstructionFile(realRoot, name string) (ProjectInstructions, bool) {
	resolved, err := filepath.EvalSymlinks(filepath.Join(realRoot, name))
	if err != nil {
		return ProjectInstructions{}, false
	}
	if rel, err := filepath.Rel(realRoot, resolved); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return ProjectInstructions{}, false
	}
	// Stat before opening so a FIFO or device is never opened (which could block).
	if fi, err := os.Stat(resolved); err != nil || !fi.Mode().IsRegular() {
		return ProjectInstructions{}, false
	}
	f, err := os.Open(resolved)
	if err != nil {
		return ProjectInstructions{}, false
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		return ProjectInstructions{}, false
	}
	data, err := io.ReadAll(io.LimitReader(f, MaxProjectInstructions+1))
	if err != nil || strings.TrimSpace(string(data)) == "" {
		return ProjectInstructions{}, false
	}
	pi := ProjectInstructions{Name: name, Size: fi.Size()}
	if len(data) <= MaxProjectInstructions {
		pi.Content = string(data)
		return pi, true
	}
	cut := MaxProjectInstructions
	for cut > 0 && !utf8.RuneStart(data[cut]) {
		cut--
	}
	if pi.Size < int64(len(data)) {
		pi.Size = int64(len(data)) // the file grew after Stat
	}
	pi.Truncated = true
	pi.Content = string(data[:cut]) + fmt.Sprintf("\n…[truncated: %s is %d bytes; only the first %d are shown]", name, pi.Size, cut)
	return pi, true
}
