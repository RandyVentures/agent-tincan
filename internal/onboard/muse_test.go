package onboard

import (
	"strings"
	"testing"
)

func TestMuseRecipeApprovesRouteBeforeInvite(t *testing.T) {
	k := build(t, Options{RelayURL: "http://tincan-relay", Roster: []Member{{Name: "muse", Kind: KindProxySandbox}}})
	steps := strings.Join(recipe(t, k, KindProxySandbox).Steps, "\n")
	check := strings.Index(steps, "/v1/hello?nonce=")
	invite := strings.Index(steps, "tincan invite")
	if check < 0 || invite < 0 || check >= invite {
		t.Fatalf("route approval must precede invitation creation:\n%s", steps)
	}
	for _, want := range []string{"HTTP 200", "service", "first-use", "--proxy-credentials-from-env", "docs/adapters/proxy-sandbox.md"} {
		if !strings.Contains(steps, want) {
			t.Errorf("recipe missing %q:\n%s", want, steps)
		}
	}
	if strings.Contains(steps, "<user>:<pass>@") {
		t.Error("recipe still puts proxy credentials in command arguments")
	}
}

func TestMuseOnboardingDistinguishesPilotAndOngoingWake(t *testing.T) {
	k := build(t, Options{RelayURL: "http://tincan-relay", Roster: []Member{{Name: "muse", Kind: KindProxySandbox}}})
	a := block(t, k, "muse")
	text := blockText(a)
	for _, want := range []string{"--proxy-credentials-from-env", "--timeout 3m", "407", "fresh shell", "one wait", "owner enables", "Do not resend"} {
		if !strings.Contains(text, want) {
			t.Errorf("onboarding missing %q:\n%s", want, text)
		}
	}
	for _, stale := range []string{"<user>:<pass>@", "almost never your tunnel", "Also keep a scheduled check every 5 minutes", "choose \"Always allow this site\""} {
		if strings.Contains(text, stale) {
			t.Errorf("onboarding still contains %q", stale)
		}
	}
}
