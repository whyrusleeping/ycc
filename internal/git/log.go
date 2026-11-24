package git

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Commit is one `git log` entry with its shortstat.
type Commit struct {
	SHA         string
	Subject     string
	AuthorName  string
	AuthorEmail string
	AuthoredAt  time.Time
	CommittedAt time.Time
	Parents     []string
	Files       int
	Additions   int64
	Deletions   int64
}

// Branch is one local branch (refs/heads).
type Branch struct {
	Name    string // short name without refs/heads/
	SHA     string
	Current bool // checked out in this worktree
}

// fullSHARe matches a full object id (SHA-1 or SHA-256).
var fullSHARe = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)

// Branches lists local branches, most recent tip first. The command runs with
// --no-optional-locks because history views poll it beside agent commits.
func (r *Repo) Branches() ([]Branch, error) {
	out, err := r.run("--no-optional-locks", "for-each-ref", "--sort=-committerdate",
		"--format=%(refname)%1f%(objectname)%1f%(HEAD)", "refs/heads")
	if err != nil {
		return nil, err
	}
	var branches []Branch
	for _, line := range strings.Split(out, "\n") {
		f := strings.Split(line, "\x1f")
		if len(f) != 3 || !strings.HasPrefix(f[0], "refs/heads/") {
			continue
		}
		branches = append(branches, Branch{
			Name:    strings.TrimPrefix(f[0], "refs/heads/"),
			SHA:     f[1],
			Current: f[2] == "*",
		})
	}
	return branches, nil
}

// HeadCommit returns the full sha HEAD points at, or "" for an unborn HEAD.
func (r *Repo) HeadCommit() (string, error) {
	out, err := r.run("rev-parse", "--verify", "--quiet", "HEAD^{commit}")
	if err != nil {
		// rev-parse --verify --quiet exits nonzero without output for an
		// unborn HEAD; a repository failure still reports itself here.
		if _, statErr := r.run("rev-parse", "--git-dir"); statErr != nil {
			return "", statErr
		}
		return "", nil
	}
	return strings.TrimSpace(out), nil
}

// Log returns up to limit commits reachable from tip in `git log` order,
// skipping the first skip. tip must be a full commit sha, so neither a flag nor
// a ref expression can reach the git invocation.
func (r *Repo) Log(tip string, skip, limit int) ([]Commit, error) {
	if !fullSHARe.MatchString(tip) {
		return nil, fmt.Errorf("invalid commit sha %q", tip)
	}
	if skip < 0 || limit <= 0 {
		return nil, fmt.Errorf("invalid log range skip=%d limit=%d", skip, limit)
	}
	out, err := r.run("-c", "log.showSignature=false", "--no-optional-locks", "log", "--no-color",
		"--no-show-signature", "--format=%x1e%H%x1f%P%x1f%an%x1f%ae%x1f%aI%x1f%cI%x1f%s", "--shortstat",
		"--skip="+strconv.Itoa(skip), "-n", strconv.Itoa(limit), "--end-of-options", tip)
	if err != nil {
		return nil, err
	}
	return parseLog(out), nil
}

// CommitsSince returns the commits reachable from HEAD but not from base
// (`git log base..HEAD`), newest first, without shortstats. base must be a
// full commit sha.
func (r *Repo) CommitsSince(base string) ([]Commit, error) {
	if !fullSHARe.MatchString(base) {
		return nil, fmt.Errorf("invalid commit sha %q", base)
	}
	out, err := r.run("-c", "log.showSignature=false", "--no-optional-locks", "log", "--no-color",
		"--no-show-signature", "--format=%x1e%H%x1f%P%x1f%an%x1f%ae%x1f%aI%x1f%cI%x1f%s",
		"--end-of-options", base+"..HEAD")
	if err != nil {
		return nil, err
	}
	return parseLog(out), nil
}

// parseLog reads Log's record-separated format. Each record is the header line
// followed by an optional shortstat line (absent for merges and empty commits).
func parseLog(out string) []Commit {
	var commits []Commit
	for _, rec := range strings.Split(out, "\x1e") {
		header, rest, _ := strings.Cut(rec, "\n")
		f := strings.Split(header, "\x1f")
		if len(f) != 7 {
			continue
		}
		c := Commit{
			SHA:         f[0],
			Parents:     strings.Fields(f[1]),
			AuthorName:  f[2],
			AuthorEmail: f[3],
			Subject:     f[6],
		}
		c.AuthoredAt, _ = time.Parse(time.RFC3339, f[4])
		c.CommittedAt, _ = time.Parse(time.RFC3339, f[5])
		c.Files, c.Additions, c.Deletions = parseShortstat(rest)
		commits = append(commits, c)
	}
	return commits
}

// parseShortstat reads " 3 files changed, 10 insertions(+), 2 deletions(-)",
// where either count clause may be absent.
func parseShortstat(s string) (files int, additions, deletions int64) {
	for _, part := range strings.Split(strings.TrimSpace(s), ",") {
		fields := strings.Fields(part)
		if len(fields) < 2 {
			continue
		}
		n, err := strconv.ParseInt(fields[0], 10, 64)
		if err != nil {
			continue
		}
		switch {
		case strings.HasPrefix(fields[1], "file"):
			files = int(n)
		case strings.HasPrefix(fields[1], "insertion"):
			additions = n
		case strings.HasPrefix(fields[1], "deletion"):
			deletions = n
		}
	}
	return files, additions, deletions
}
