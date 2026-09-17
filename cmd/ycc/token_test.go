package main

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/whyrusleeping/ycc/internal/secrets"
	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
)

func TestSecretKeyNameValidation(t *testing.T) {
	for _, valid := range []string{"EXA_API_KEY", "custom_key", "_KEY2"} {
		if !validKeyEnv(valid) {
			t.Errorf("validKeyEnv(%q) = false", valid)
		}
	}
	for _, invalid := range []string{"sk-test-SENTINEL-CREDENTIAL-0389", "9KEY", "KEY=value", "KEY ENV"} {
		if validKeyEnv(invalid) {
			t.Errorf("validKeyEnv(%q) = true", invalid)
		}
	}
}

func TestTokenSetConfirmationDoesNotEchoValue(t *testing.T) {
	const sentinel = "sk-test-ENTRY-CONFIRMATION-SENTINEL-0389"
	configDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configDir)
	t.Setenv("HOME", configDir)

	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := inW.WriteString(sentinel + "\n"); err != nil {
		t.Fatal(err)
	}
	inW.Close()
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldIn, oldOut := os.Stdin, os.Stdout
	os.Stdin, os.Stdout = inR, outW
	t.Cleanup(func() {
		os.Stdin, os.Stdout = oldIn, oldOut
		inR.Close()
		outR.Close()
	})

	err = tokenCommand().Run(context.Background(), []string{"token", "set", "EXA_API_KEY"})
	outW.Close()
	output, readErr := io.ReadAll(outR)
	if err != nil {
		t.Fatalf("token set: %v", err)
	}
	if readErr != nil {
		t.Fatal(readErr)
	}
	if strings.Contains(string(output), sentinel) {
		t.Fatal("dedicated secret-entry confirmation echoed the credential")
	}
	if got, ok := secrets.Lookup("EXA_API_KEY"); !ok || got != sentinel {
		t.Fatal("dedicated secret-entry command did not store the credential")
	}
}

func TestTokenSetRejectsSecretValueArgument(t *testing.T) {
	const sentinel = "sk-test-ARGV-SENTINEL-CREDENTIAL-0389"
	configDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configDir)
	t.Setenv("HOME", configDir)

	if err := tokenCommand().Run(context.Background(), []string{"token", "set", "EXA_API_KEY", sentinel}); err == nil {
		t.Fatal("token set accepted an extra argument that could expose a credential in shell history")
	} else if strings.Contains(err.Error(), sentinel) {
		t.Fatal("token set error echoed the extra argument")
	}
	if _, ok := secrets.Lookup("EXA_API_KEY"); ok {
		t.Fatal("token set stored a value supplied in argv")
	}
}

func TestReadSecretTokenFromPipe(t *testing.T) {
	const sentinel = "sk-test-ENTRY-SENTINEL-CREDENTIAL-0389"
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.WriteString(sentinel + "\n"); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	got, err := readSecretToken(r, "EXA_API_KEY")
	if err != nil {
		t.Fatal(err)
	}
	if got != sentinel {
		t.Fatal("dedicated input did not preserve the supplied token")
	}
}

func TestRoutineEventDisplayRedactsCredential(t *testing.T) {
	const sentinel = "sk-test-DISPLAY-SENTINEL-CREDENTIAL-0389"
	configDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configDir)
	t.Setenv("HOME", configDir)
	line := formatEvent(&v1.Event{
		Seq: 1, Actor: "user", Type: "user_input",
		DataJson: `{"text":"my api key is ` + sentinel + `"}`,
	})
	if strings.Contains(line, sentinel) {
		t.Fatal("routine event display contains the credential")
	}
	if !strings.Contains(line, "redacted from display") {
		t.Fatalf("routine event display did not show a redaction warning: %s", line)
	}
}
