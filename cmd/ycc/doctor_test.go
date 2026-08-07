package main

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// isolateEnv points the secrets store and user-config discovery at a hermetic
// temp dir and clears the keys doctor consults, so tests do not depend on the
// developer's real machine-local secrets or exported env.
func isolateEnv(t *testing.T) {
	t.Helper()
	cfgDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgDir) // secrets.Path + config discovery on linux
	t.Setenv("XDG_CACHE_HOME", filepath.Join(cfgDir, "cache"))
	t.Setenv("HOME", cfgDir) // fallback for os.UserConfigDir/UserCacheDir elsewhere
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

func TestRunDoctorWarnsForWorkspaceInlineNotifyAuth(t *testing.T) {
	isolateEnv(t)
	ws := t.TempDir()
	secret := "Bearer must-not-appear"
	contents := validConfig + `
[notify]
url = "https://ntfy.sh/topic"
auth = "` + secret + `"
`
	if err := os.WriteFile(filepath.Join(ws, "ycc.toml"), []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")

	var out bytes.Buffer
	if hard := runDoctor(ws, "", "", "", &out); hard {
		t.Fatalf("credential hygiene warning must not hard-fail:\n%s", out.String())
	}
	if !bytes.Contains(out.Bytes(), []byte("workspace ycc.toml contains inline notify.auth")) {
		t.Fatalf("expected inline notify.auth warning:\n%s", out.String())
	}
	if !bytes.Contains(out.Bytes(), []byte("notify.auth_env")) || !bytes.Contains(out.Bytes(), []byte("workspace-first")) {
		t.Fatalf("expected accurate auth_env/single-file discovery remedy:\n%s", out.String())
	}
	if bytes.Contains(out.Bytes(), []byte(secret)) {
		t.Fatalf("doctor must never print the credential value:\n%s", out.String())
	}
}

func TestRunDoctorWarnsForInlineNotifyAuthInPartialWorkspaceConfig(t *testing.T) {
	isolateEnv(t)
	ws := t.TempDir()
	secret := "Bearer partial-must-not-appear"
	contents := `[worktree]
copy = ["README.md"]

[notify]
url = "https://ntfy.sh/topic"
auth = "` + secret + `"
`
	if err := os.WriteFile(filepath.Join(ws, "ycc.toml"), []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")

	var out bytes.Buffer
	if hard := runDoctor(ws, "", "", "", &out); !hard {
		t.Fatalf("partial config should still fail full runtime validation:\n%s", out.String())
	}
	if !bytes.Contains(out.Bytes(), []byte("workspace ycc.toml contains inline notify.auth")) {
		t.Fatalf("expected inline notify.auth warning despite full-config validation failure:\n%s", out.String())
	}
	if bytes.Contains(out.Bytes(), []byte(secret)) {
		t.Fatalf("doctor must never print the credential value:\n%s", out.String())
	}
}

func TestCredentialHygieneWarnsForBroadKnownFiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not provide Unix permission semantics")
	}
	isolateEnv(t)
	configDir, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	paths := []string{
		filepath.Join(configDir, "ycc", "ycc.toml"),
		filepath.Join(configDir, "ycc", "secrets.json"),
		filepath.Join(cacheDir, "ycc", "daemon.log"),
	}
	for _, path := range paths {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("must-not-be-read-or-printed"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	checks := credentialHygieneChecks(t.TempDir())
	var rendered strings.Builder
	for _, c := range checks {
		rendered.WriteString(c.detail)
		rendered.WriteString("\n")
		rendered.WriteString(c.remedy)
		rendered.WriteString("\n")
	}
	got := rendered.String()
	for _, want := range []string{"user config", "secrets store", "daemon log", "chmod 600"} {
		if !strings.Contains(got, want) {
			t.Errorf("permission warnings missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "must-not-be-read-or-printed") {
		t.Fatalf("permission check printed file contents:\n%s", got)
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

// Forge integration is optional. With PATH controlled so neither gh nor glab
// can exist, doctor emits one actionable warning but reports no hard failure.
func TestRunDoctorNoForgeCLIWarnOnly(t *testing.T) {
	isolateEnv(t)
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "ycc.toml"), []byte(validConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")
	t.Setenv("PATH", t.TempDir())

	var out bytes.Buffer
	if hard := runDoctor(ws, "", "", "", &out); hard {
		t.Fatalf("missing forge CLIs must not cause a hard failure:\n%s", out.String())
	}
	if !bytes.Contains(out.Bytes(), []byte("⚠ forge: no forge CLI (gh/glab) installed; forge features (task import, PR publish) unavailable")) {
		t.Fatalf("expected missing forge CLI warning:\n%s", out.String())
	}
	if !bytes.Contains(out.Bytes(), []byte("install gh (https://cli.github.com) or glab")) {
		t.Fatalf("expected actionable forge CLI remedy:\n%s", out.String())
	}
}
