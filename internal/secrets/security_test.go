package secrets

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

const fakeSentinelCredential = "sk-test-SENTINEL-CREDENTIAL-0389"

func TestAuthorizationIsWorkspaceToolAndUseScoped(t *testing.T) {
	setupDir(t)
	workspace := t.TempDir()
	if err := Set("EXA_API_KEY", fakeSentinelCredential); err != nil {
		t.Fatal(err)
	}
	if err := Authorize("EXA_API_KEY", workspace, "web_search"); err != nil {
		t.Fatal(err)
	}

	if _, err := ConsumeAuthorized("EXA_API_KEY", workspace, "fetch_page"); !errors.Is(err, ErrNotAuthorized) {
		t.Fatalf("wrong-tool consume error = %v, want ErrNotAuthorized", err)
	}
	got, err := ConsumeAuthorized("EXA_API_KEY", workspace, "web_search")
	if err != nil {
		t.Fatal(err)
	}
	if got != fakeSentinelCredential {
		t.Fatal("authorized consumer did not receive stored credential")
	}
	if _, err := ConsumeAuthorized("EXA_API_KEY", workspace, "web_search"); !errors.Is(err, ErrNotAuthorized) {
		t.Fatalf("second consume error = %v, want ErrNotAuthorized", err)
	}

	auths := Authorizations()
	if len(auths) != 1 || auths[0].UsedAt.IsZero() {
		t.Fatalf("authorization audit = %#v, want one used record", auths)
	}
	if strings.Contains(fmt.Sprintf("%+v", auths), fakeSentinelCredential) {
		t.Fatal("reference-only authorization audit contains the credential value")
	}
}

func TestDisclosureGuardAndPresentationRedaction(t *testing.T) {
	setupDir(t)
	if err := Set("EXA_API_KEY", fakeSentinelCredential); err != nil {
		t.Fatal(err)
	}
	input := "my api key is " + fakeSentinelCredential
	if !LooksLikeCredential(input) {
		t.Fatal("obvious credential was not detected")
	}
	redacted, changed := RedactForPresentation("diagnostic: " + input)
	if !changed {
		t.Fatal("presentation was not marked changed")
	}
	if strings.Contains(redacted, fakeSentinelCredential) {
		t.Fatal("presentation contains the credential value")
	}
	if !strings.Contains(input, fakeSentinelCredential) {
		t.Fatal("redaction unexpectedly modified source text")
	}
	if LooksLikeCredential("please use the credential named EXA_API_KEY") {
		t.Fatal("a credential reference was mistaken for a raw value")
	}
}
