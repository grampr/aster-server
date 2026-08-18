package httpapi

import (
	"net/url"
	"strings"
	"testing"

	"github.com/grampr/aster-server/internal/auth"
)

func TestGoogleCallbackLocationBuildsOnlyAllowedDeepLink(t *testing.T) {
	state := strings.Repeat("s", 43)
	location, err := googleCallbackLocation(auth.GoogleCallbackResult{
		RedirectURI: "aster://auth/callback", ClientState: state, ExchangeCode: "aster_ec_example",
	})
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(location)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Scheme != "aster" || parsed.Host != "auth" || parsed.Path != "/callback" || parsed.Query().Get("state") != state || parsed.Query().Get("code") != "aster_ec_example" {
		t.Fatalf("unexpected deep link: %s", location)
	}
	if _, err := googleCallbackLocation(auth.GoogleCallbackResult{
		RedirectURI: "https://attacker.example/callback", ClientState: state, ExchangeCode: "code",
	}); err == nil {
		t.Fatal("untrusted redirect URI must be rejected")
	}
	if _, err := googleCallbackLocation(auth.GoogleCallbackResult{
		RedirectURI: "aster://auth/callback", ClientState: state, ExchangeCode: "code", ErrorCode: "provider_error",
	}); err == nil {
		t.Fatal("ambiguous callback result must be rejected")
	}
}
