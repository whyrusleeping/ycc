package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// isolateEnv points the secrets store and user-config discovery at a hermetic
// temp dir and clears the keys doctor consults, so tests do not depend on the
// developer's real machine-local secrets or exported env.
func isolateEnv(t *testing.T) {
	t.Helper()
	cfgDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgDir) // secrets.Path + config discovery on linux
	t.Setenv("HOME", cfgDir)            // fallback for os.UserConfigDir elsewhere
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("EXA_API_KEY", "")
}

const validConfig = `max_tokens = 32000

[models.claude]
backend = "anthropic"
base_url = "https://api.anthropic.com"
model = "claude-opus-4-8"
key_env = "ANTHROPIC_API_KEY"

[roles]
coordinator = "claude"
implementer = "claude"
reviewers = ["claude"]
`

// A valid config whose model key is present in the env resolves cleanly: no hard
// failure and the line reports resolution from env.
func TestRunDoctorValidConfigKeyInEnv(t *testing.T) {
	isolateEnv(t)
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "ycc.toml"), []byte(validConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ANTHROPIC_API_KEY", "sk-test-value")

	var out bytes.Buffer
	if hard := runDoctor(ws, "", "", "", &out); hard {
		t.Fatalf("expected no hard failure:\n%s", out.String())
	}
	s := out.String()
	if !bytes.Contains(out.Bytes(), []byte("resolved from env")) {
		t.Fatalf("expected key resolved from env:\n%s", s)
	}
	if bytes.Contains(out.Bytes(), []byte("sk-test-value")) {
		t.Fatalf("secret value must never be printed:\n%s", s)
	}
}

// A valid config with the model key absent everywhere is a HARD failure that
// names the KEY_ENV and the `ycc token set` remedy.
func TestRunDoctorMissingKeyHardFails(t *testing.T) {
	isolateEnv(t)
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "ycc.toml"), []byte(validConfig), 0o644); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if hard := runDoctor(ws, "", "", "", &out); !hard {
		t.Fatalf("expected a hard failure for a missing model key:\n%s", out.String())
	}
	s := out.String()
	if !bytes.Contains(out.Bytes(), []byte("ANTHROPIC_API_KEY")) {
		t.Fatalf("output should name the missing KEY_ENV:\n%s", s)
	}
	if !bytes.Contains(out.Bytes(), []byte("ycc token set ANTHROPIC_API_KEY")) {
		t.Fatalf("output should include the token-set remedy:\n%s", s)
	}
}

// A malformed TOML config is a HARD failure.
func TestRunDoctorMalformedConfigHardFails(t *testing.T) {
	isolateEnv(t)
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "ycc.toml"), []byte("this is = not valid toml ]["), 0o644); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if hard := runDoctor(ws, "", "", "", &out); !hard {
		t.Fatalf("expected a hard failure for malformed config:\n%s", out.String())
	}
	if !bytes.Contains(out.Bytes(), []byte("config file")) {
		t.Fatalf("output should flag the config file:\n%s", out.String())
	}
}

// With no config file and no fallback ANTHROPIC_API_KEY, every session would
// 401, so the unresolvable fallback key is a HARD failure. Providing the env key
// clears it.
func TestRunDoctorNoConfigFallbackKey(t *testing.T) {
	isolateEnv(t)
	ws := t.TempDir()

	var out bytes.Buffer
	if hard := runDoctor(ws, "", "", "", &out); !hard {
		t.Fatalf("expected a hard failure when the fallback key is unresolvable:\n%s", out.String())
	}
	if !bytes.Contains(out.Bytes(), []byte("no ycc.toml")) {
		t.Fatalf("output should note the missing config:\n%s", out.String())
	}

	t.Setenv("ANTHROPIC_API_KEY", "sk-fallback")
	var out2 bytes.Buffer
	if hard := runDoctor(ws, "", "", "", &out2); hard {
		t.Fatalf("expected no hard failure once the fallback key is set:\n%s", out2.String())
	}
}

// doctor must NOT mutate the workspace: probing git state never runs `git init`,
// so a non-repo workspace stays a non-repo (no .git directory created).
func TestRunDoctorDoesNotInitGit(t *testing.T) {
	isolateEnv(t)
	ws := t.TempDir()
	t.Setenv("ANTHROPIC_API_KEY", "sk-test") // avoid unrelated hard failure noise

	var out bytes.Buffer
	runDoctor(ws, "", "", "", &out)

	if _, err := os.Stat(filepath.Join(ws, ".git")); !os.IsNotExist(err) {
		t.Fatalf("doctor must not create a .git directory (got err=%v)", err)
	}
	if !bytes.Contains(out.Bytes(), []byte("not a git repository")) {
		t.Fatalf("expected a not-a-repo git line:\n%s", out.String())
	}
}
