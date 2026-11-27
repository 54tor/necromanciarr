package doctor

import (
	"context"
	"strings"
	"testing"

	"necromanciarr/internal/config"
)

func TestAuthCheck_warnsOnANonLoopbackListenWithoutAnHttpsPublicUrl(t *testing.T) {
	testCases := []struct{ name, publicURL string }{
		{"no public url", ""},
		{"plain http public url", "http://necro.example.com"},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			settings := config.Config{ListenAddr: "0.0.0.0:8787", AuthToken: "t", PublicURL: testCase.publicURL}

			got := checkAuth(settings)

			if got.Status != Warn || got.Code != CodeAuthPublicURLMissing {
				t.Fatalf("valeurObtenue %v %q, valeurAttendue warn %q", got.Status, got.Code, CodeAuthPublicURLMissing)
			}
			if !strings.Contains(hintOf(got), "publicUrl") {
				t.Fatalf("valeurObtenue hint %q, valeurAttendue a hint naming publicUrl", hintOf(got))
			}
		})
	}
}

func TestAuthCheck_isQuietOnLoopback(t *testing.T) {
	got := checkAuth(config.Config{ListenAddr: "127.0.0.1:8787"})

	if got.Status != OK {
		t.Fatalf("valeurObtenue %v, valeurAttendue ok", got.Status)
	}
}

func TestAuthCheck_isQuietBehindAnHttpsPublicUrl(t *testing.T) {
	got := checkAuth(config.Config{ListenAddr: "0.0.0.0:8787", AuthToken: "t", PublicURL: "https://necro.example.com"})

	if got.Status != OK {
		t.Fatalf("valeurObtenue %v, valeurAttendue ok", got.Status)
	}
}

func TestAuthCheck_keepsTheMissingTokenFailure(t *testing.T) {
	got := checkAuth(config.Config{ListenAddr: "0.0.0.0:8787"})

	if got.Status != Fail || got.Code != CodeNoAuthToken {
		t.Fatalf("valeurObtenue %v %q, valeurAttendue fail %q", got.Status, got.Code, CodeNoAuthToken)
	}
}

func TestAuthCheck_runsFromBuildWithZeroValueDeps(t *testing.T) {
	checks := Build(config.Config{ListenAddr: "0.0.0.0:8787", AuthToken: "t"}, Deps{})

	for _, check := range checks {
		if check.ID != IDAuth {
			continue
		}
		if got := check.Run(context.Background()); got.Code != CodeAuthPublicURLMissing {
			t.Fatalf("valeurObtenue %q, valeurAttendue %q", got.Code, CodeAuthPublicURLMissing)
		}
		return
	}
	t.Fatal("no check with id auth")
}
