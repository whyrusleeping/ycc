package session

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Curated word lists for readable session IDs: s_<adjective>-<noun>-<4hex>.
// 128 × 128 word pairs × 65,536 hex suffixes = 1,073,741,824 combinations
// (30 bits of entropy via crypto/rand with zero modulo bias). Words avoid
// status, git, and system vocabulary so handles are never misread as state.
var sessionAdjectives = [128]string{
	"amber", "ample", "arid", "ashen", "azure", "breezy", "bright", "brisk",
	"broad", "bronze", "calm", "candid", "chalky", "cheery", "chilly", "clever",
	"cloudy", "coastal", "cobalt", "copper", "coral", "cozy", "crisp", "curved",
	"dainty", "dapper", "deft", "dense", "dewy", "dusky", "eager", "earthy",
	"easy", "elder", "even", "faint", "fair", "ferny", "festive", "firm",
	"flaxen", "fleet", "floral", "fluid", "foggy", "fond", "frosty", "gentle",
	"gilded", "glad", "glassy", "glossy", "golden", "grand", "grassy", "gusty",
	"hale", "handy", "happy", "hardy", "hazy", "hearty", "hollow", "honest",
	"humble", "icy", "indigo", "ivory", "jade", "jaunty", "jolly", "jovial",
	"keen", "kind", "leafy", "lilac", "limber", "lofty", "loyal", "lucid",
	"lunar", "lush", "mellow", "merry", "mild", "minty", "misty", "modest",
	"mossy", "muted", "neat", "nimble", "noble", "nordic", "ochre", "olive",
	"opal", "ornate", "pale", "pastel", "peachy", "pearly", "peppy", "perky",
	"placid", "plucky", "plush", "poised", "polite", "proud", "quaint", "quiet",
	"radiant", "rainy", "rapid", "rosy", "ruddy", "russet", "rustic", "sandy",
	"serene", "silken", "silver", "spry", "steady", "sunny", "velvet", "vivid",
}

var sessionNouns = [128]string{
	"acorn", "agate", "alder", "alpaca", "anchor", "anvil", "aspen", "badger",
	"banyan", "barley", "basalt", "beacon", "beaver", "beech", "beetle", "bison",
	"bobcat", "boulder", "brook", "cairn", "canary", "canyon", "caribou", "cedar",
	"chisel", "cinder", "clam", "clover", "cobble", "condor", "cougar", "crane",
	"creek", "crocus", "cypress", "dahlia", "dingo", "dolphin", "donkey", "dove",
	"dune", "eagle", "egret", "elm", "ember", "ermine", "falcon", "ferret",
	"fiddle", "finch", "fjord", "flint", "fox", "garnet", "gazelle", "gecko",
	"geyser", "glacier", "gopher", "granite", "grouse", "harbor", "hawk", "hazel",
	"heron", "hickory", "hornet", "ibex", "iguana", "iris", "jackal", "jaguar",
	"jasper", "jay", "juniper", "kettle", "kestrel", "koala", "lagoon", "lantern",
	"larch", "lark", "laurel", "lemur", "lichen", "linden", "linnet", "lizard",
	"llama", "lobster", "loon", "lupine", "lynx", "magpie", "mallard", "maple",
	"marble", "marmot", "marten", "meadow", "merlin", "minnow", "moose", "mortar",
	"narwhal", "newt", "nutmeg", "ocelot", "orca", "oriole", "osprey", "otter",
	"owl", "panda", "pebble", "pelican", "petrel", "pewter", "pika", "pine",
	"plover", "poplar", "puffin", "puma", "quail", "quartz", "quiver", "raven",
}

var namedSessionIDRe = regexp.MustCompile(`^s_([a-z]+-[a-z]+)-[0-9a-f]{4}$`)

// newID mints a Docker-style session id: s_<adjective>-<noun>-<4hex>.
func newID() (string, error) {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	adj := sessionAdjectives[b[0]&0x7f]
	noun := sessionNouns[b[1]&0x7f]
	return fmt.Sprintf("s_%s-%s-%s", adj, noun, hex.EncodeToString(b[2:4])), nil
}

// ShortSessionID returns the human-readable handle ("amber-cedar") for a
// named session id, falling back to a truncated form for legacy hex ids.
func ShortSessionID(id string) string {
	if m := namedSessionIDRe.FindStringSubmatch(id); m != nil {
		return m[1]
	}
	if len(id) > 10 {
		return id[:10] + "…"
	}
	return id
}

// mintSessionID returns a fresh session id that does not collide with an
// existing session directory in ws or reuse a live session's short handle.
func (m *Manager) mintSessionID(ws string) (string, error) {
	sessionsDir := filepath.Join(ws, ".ycc", "sessions")
	liveHandles := make(map[string]struct{})
	m.mu.Lock()
	for id := range m.sessions {
		liveHandles[id] = struct{}{}
		liveHandles[ShortSessionID(id)] = struct{}{}
	}
	m.mu.Unlock()

	var id string
	for range 8 {
		var err error
		id, err = newID()
		if err != nil {
			return "", err
		}
		if _, busy := liveHandles[id]; busy {
			continue
		}
		if _, busy := liveHandles[ShortSessionID(id)]; busy {
			continue
		}
		if _, err := os.Stat(filepath.Join(sessionsDir, id)); err == nil {
			continue
		}
		return id, nil
	}
	// Fallback: accept the last minted id if all 8 attempts somehow collided.
	if strings.TrimSpace(id) == "" {
		return newID()
	}
	return id, nil
}
