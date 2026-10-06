package daemon

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestLocalTokenPersistence(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_STATE_HOME", root)
	if got := ReadLocalToken(); got != "" {
		t.Fatalf("read missing token = %q", got)
	}
	if _, err := os.Stat(filepath.Join(root, "ycc")); !os.IsNotExist(err) {
		t.Fatalf("read-only lookup created state: %v", err)
	}
	token, err := EnsureLocalToken()
	if err != nil {
		t.Fatal(err)
	}
	if len(token) != 64 || ReadLocalToken() != token {
		t.Fatal("generated token was not persisted")
	}
	path := filepath.Join(root, "ycc", "daemon-token")
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("token file is not private: info=%v err=%v", info, err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	again, err := EnsureLocalToken()
	if err != nil || again != token {
		t.Fatalf("second ensure changed token: %v", err)
	}
	info, err = os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("token mode was not repaired: info=%v err=%v", info, err)
	}
}

func TestLocalTokenConcurrentCreation(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	var wg sync.WaitGroup
	results := make(chan string, 16)
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			token, err := EnsureLocalToken()
			if err != nil {
				t.Error(err)
			}
			results <- token
		}()
	}
	wg.Wait()
	close(results)
	winner := ReadLocalToken()
	for token := range results {
		if token == "" || token != winner {
			t.Fatal("concurrent creators did not agree on a token")
		}
	}
}

func TestLocalTokenEmptyFailsClosed(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	if err := os.MkdirAll(filepath.Dir(LocalTokenFile()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(LocalTokenFile(), []byte(" \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if ReadLocalToken() != "" {
		t.Fatal("empty file returned a credential")
	}
	if _, err := EnsureLocalToken(); err == nil {
		t.Fatal("empty existing file did not fail closed")
	}
}

func TestProbeLocalCredentials(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	o := baseOptions(t)
	o.Token = "file-token"
	ip, err := StartInProcess(o)
	if err != nil {
		t.Fatal(err)
	}
	defer ip.Close()
	if token, ok := probeLocal(ip.Addr, ""); ok || token != "" {
		t.Fatal("discovery without credentials attached to daemon")
	}
	if err := os.MkdirAll(filepath.Dir(LocalTokenFile()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(LocalTokenFile(), []byte(o.Token), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, explicit := range []string{"", "wrong-token", o.Token} {
		if token, ok := probeLocal(ip.Addr, explicit); !ok || token != o.Token {
			t.Fatalf("discovery with explicit %q did not return accepted token", explicit)
		}
	}
	if err := os.WriteFile(LocalTokenFile(), []byte("wrong-file-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	if token, ok := probeLocal(ip.Addr, o.Token); !ok || token != o.Token {
		t.Fatal("explicit token not preferred over stored token")
	}
	if _, ok := probeLocal(ip.Addr, "wrong-token"); ok {
		t.Fatal("discovery accepted invalid credentials")
	}
}

func TestBackgroundTokenUsesLocalFile(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	token, err := backgroundToken("")
	if err != nil || token == "" || token != ReadLocalToken() {
		t.Fatalf("background token did not use local file: %v", err)
	}
	_, env := backgroundDaemonCmdline("", "", token, nil)
	if len(env) != 1 || env[0] != "YCC_TOKEN="+token {
		t.Fatal("generated token not passed to background daemon")
	}
	if explicit, err := backgroundToken("explicit"); err != nil || explicit != "explicit" {
		t.Fatal("explicit background token overridden")
	}
}
