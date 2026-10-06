package credenv

import (
	"reflect"
	"sync"
	"testing"
)

func TestScrub(t *testing.T) {
	Register("CREDENV_TEST_PROVIDER")
	env := []string{"PATH=/bin", "YCC_TOKEN=secret", "ANTHROPIC_API_KEY=secret", "ANTHROPIC_AUTH_TOKEN=secret", "OPENAI_API_KEY=secret", "EXA_API_KEY=secret", "ANTHROPIC_OAUTH=secret", "OPENAI_OAUTH=secret", "CREDENV_TEST_PROVIDER=secret", "ORDINARY=a=b"}
	got := Scrub(env)
	want := []string{"PATH=/bin", "ORDINARY=a=b"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Scrub = %v, want %v", got, want)
	}
	got[0] = "changed"
	if env[0] != "PATH=/bin" {
		t.Fatal("Scrub aliases its input")
	}
}

func TestRegisterConcurrent(t *testing.T) {
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			Register("CREDENV_CONCURRENT")
			Scrub([]string{"CREDENV_CONCURRENT=secret"})
		}()
	}
	wg.Wait()
	if len(Scrub([]string{"CREDENV_CONCURRENT=secret"})) != 0 {
		t.Fatal("registered key was not scrubbed")
	}
}

func TestValidateKeyRef(t *testing.T) {
	for _, name := range []string{"YCC_TOKEN", "ANTHROPIC_OAUTH", "OPENAI_OAUTH"} {
		if ValidateKeyRef(name) == nil {
			t.Errorf("accepted reserved reference %q", name)
		}
	}
	for _, name := range []string{"", "ANTHROPIC_API_KEY", "CUSTOM_PROVIDER_KEY"} {
		if err := ValidateKeyRef(name); err != nil {
			t.Errorf("rejected reference %q: %v", name, err)
		}
	}
}

func TestValidateCredentialURL(t *testing.T) {
	for _, u := range []string{"", "https://provider.example/v1", "http://localhost:8080", "http://LOCALHOST", "http://127.0.0.1", "http://127.42.1.2:1234/v1", "http://[::1]:8080"} {
		if err := ValidateCredentialURL(u); err != nil {
			t.Errorf("rejected %q: %v", u, err)
		}
	}
	for _, u := range []string{"http://provider.example", "http://192.168.1.1", "http://128.0.0.1", "http://localhost.example", "http://localhost@evil.example", "//localhost", "https:", "https:///v1", "ftp://localhost", "://broken"} {
		if ValidateCredentialURL(u) == nil {
			t.Errorf("accepted %q", u)
		}
	}
}
