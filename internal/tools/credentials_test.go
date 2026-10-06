package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/whyrusleeping/ycc/internal/credenv"
	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/jobs"
	"github.com/whyrusleeping/ycc/internal/project"
	"github.com/whyrusleeping/ycc/internal/sandbox"
)

func TestBashScrubsCredentials(t *testing.T) {
	t.Setenv("YCC_TOKEN", "daemon-secret")
	t.Setenv("TOOLS_TEST_PROVIDER_KEY", "provider-secret")
	t.Setenv("TOOLS_TEST_ORDINARY", "ordinary")
	credenv.Register("TOOLS_TEST_PROVIDER_KEY")
	for _, mode := range []string{"foreground", "background", "reviewer"} {
		for _, overrides := range []bool{false, true} {
			t.Run(mode+"/overrides="+strconv.FormatBool(overrides), func(t *testing.T) {
				if mode == "reviewer" && sandbox.Available() == sandbox.None {
					t.Skip("reviewer shell unavailable")
				}
				jr := jobs.NewRegistry()
				t.Cleanup(jr.KillAll)
				ws := &Workspace{Root: t.TempDir(), Jobs: jr, Emitter: event.NewEmitter(&captureRec{}, "coordinator")}
				expected := "creds=unset,unset ordinary=ordinary workspace=unset"
				if overrides {
					ws.Env = []string{"TOOLS_TEST_WORKSPACE=workspace"}
					expected = "creds=unset,unset ordinary=ordinary workspace=workspace"
				}
				reg := New()
				if mode == "reviewer" {
					reg.Add(Reviewer(ws)...)
				} else {
					reg.Add(Editing(ws)...)
				}
				params, _ := json.Marshal(map[string]any{
					"command":           "printf 'creds=%s,%s ordinary=%s workspace=%s' \"${YCC_TOKEN-unset}\" \"${TOOLS_TEST_PROVIDER_KEY-unset}\" \"$TOOLS_TEST_ORDINARY\" \"${TOOLS_TEST_WORKSPACE-unset}\"",
					"run_in_background": mode == "background",
				})
				res := dispatch(t, reg, "Bash", string(params))
				if res.IsError {
					t.Fatal(res.Content)
				}
				if mode == "background" {
					res = dispatch(t, reg, "wait", `{"job_ids":["job_1"]}`)
				}
				if res.IsError || !strings.Contains(res.Content, expected) || strings.Contains(res.Content, "-secret") {
					t.Fatalf("shell environment: %s", res.Content)
				}
			})
		}
	}
}

func TestResolveDeniesCredentialLocations(t *testing.T) {
	base := t.TempDir()
	t.Setenv("HOME", base)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(base, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(base, "state"))
	configDir, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	configDir = filepath.Join(configDir, "ycc")
	token := filepath.Join(filepath.Dir(project.StateFile()), "daemon-token")
	for _, dir := range []string{configDir, filepath.Dir(token)} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	secrets := filepath.Join(configDir, "secrets.json")
	for _, path := range []string{secrets, token} {
		if err := os.WriteFile(path, []byte("secret"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for name, target := range map[string]string{"config-link": configDir, "token-link": token} {
		if err := os.Symlink(target, filepath.Join(base, name)); err != nil {
			t.Fatal(err)
		}
	}
	ws := &Workspace{Root: base, WriteRoots: []string{configDir}}
	for _, path := range []string{configDir, secrets, filepath.Join(configDir, "new.json"), token, "config-link/secrets.json", "token-link"} {
		for _, resolve := range []func(string) (string, error){ws.resolveRead, ws.resolve} {
			if _, err := resolve(path); err == nil || !strings.Contains(err.Error(), "protected ycc credential location") {
				t.Errorf("resolve(%q) = %v", path, err)
			}
		}
	}
	// Outgoing credential symlinks must protect both the symlink spelling and
	// its external target, without weakening the incoming-symlink coverage.
	externalSecret := filepath.Join(base, "external-secret.json")
	if err := os.WriteFile(externalSecret, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(secrets); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(externalSecret, secrets); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{secrets, externalSecret, "config-link/secrets.json"} {
		for _, resolve := range []func(string) (string, error){ws.resolveRead, ws.resolve} {
			if _, err := resolve(path); err == nil || !strings.Contains(err.Error(), "protected ycc credential location") {
				t.Errorf("resolve outgoing credential symlink %q = %v", path, err)
			}
		}
	}
	for _, path := range []string{filepath.Join(base, "ordinary"), filepath.Join(configDir+"-other", "secrets.json"), filepath.Join(filepath.Dir(token), "projects.json")} {
		if _, err := ws.resolveRead(path); err != nil {
			t.Errorf("ordinary read %q: %v", path, err)
		}
	}
}
