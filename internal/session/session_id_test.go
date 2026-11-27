package session

import (
	"regexp"
	"testing"
)

func TestSessionIDWordLists(t *testing.T) {
	wordRe := regexp.MustCompile(`^[a-z]{3,8}$`)
	banned := map[string]bool{
		"stale": true, "dead": true, "live": true, "idle": true, "busy": true,
		"dirty": true, "clean": true, "open": true, "closed": true, "failed": true,
		"broken": true, "blocked": true, "ready": true, "active": true, "main": true,
		"master": true, "merge": true, "commit": true, "prod": true, "raw": true,
		"bare": true, "fixed": true, "stuck": true, "branch": true, "tree": true,
		"fork": true, "push": true, "pull": true, "sync": true, "lock": true,
	}
	seen := make(map[string]string, len(sessionAdjectives)+len(sessionNouns))
	check := func(kind string, words []string) {
		for _, w := range words {
			if !wordRe.MatchString(w) {
				t.Errorf("%s %q does not match ^[a-z]{3,8}$", kind, w)
			}
			if banned[w] {
				t.Errorf("%s %q is in the banned status/git list", kind, w)
			}
			if prev, dup := seen[w]; dup {
				t.Errorf("word %q duplicated (%s and %s)", w, prev, kind)
			}
			seen[w] = kind
		}
	}
	check("adjective", sessionAdjectives[:])
	check("noun", sessionNouns[:])
}

func TestNewIDFormatAndUniqueness(t *testing.T) {
	seen := make(map[string]struct{}, 500)
	for range 500 {
		id, err := newID()
		if err != nil {
			t.Fatalf("newID: %v", err)
		}
		if !namedSessionIDRe.MatchString(id) {
			t.Fatalf("newID() = %q, want s_<adj>-<noun>-<4hex>", id)
		}
		if _, dup := seen[id]; dup {
			t.Fatalf("duplicate id %q in 500 draws", id)
		}
		seen[id] = struct{}{}
	}
}

func TestShortSessionID(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"s_amber-cedar-4f2a", "amber-cedar"},
		{"s_0123456789abcdef", "s_01234567…"},
		{"s_short", "s_short"},
		{"", ""},
	}
	for _, tc := range cases {
		if got := ShortSessionID(tc.in); got != tc.want {
			t.Errorf("ShortSessionID(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
