package sim_test

import (
	"encoding/hex"
	"fmt"
	"strings"
	"testing"

	"github.com/anhydrous99/satlink/ppp"
	"github.com/anhydrous99/satlink/sim"
)

// A configuration printed with fmt never shows the password the gateway
// demands.

// gatewayPassword is the secret the formatting must keep to itself.
const gatewayPassword = "hunter2"

// configDemandingCredentials returns the default configuration with a
// gateway that demands a name and gatewayPassword.
func configDemandingCredentials() sim.Config {
	config := sim.DefaultConfig()
	config.Authentication = &sim.Authentication{
		Protocol:    ppp.AuthProtocolCHAPMD5,
		Credentials: sim.Credentials{Username: "pilot", Password: gatewayPassword},
	}
	return config
}

// showsPassword reports whether text shows gatewayPassword in any of the
// ways a verb could write it: as it is, or in hexadecimal, as %x and %X
// write strings.
func showsPassword(text string) bool {
	encoded := hex.EncodeToString([]byte(gatewayPassword))
	for _, form := range []string{gatewayPassword, encoded, strings.ToUpper(encoded)} {
		if strings.Contains(text, form) {
			return true
		}
	}
	return false
}

func TestFormattingAConfigNeverShowsThePassword(t *testing.T) {
	t.Parallel()
	config := configDemandingCredentials()
	values := map[string]any{
		"a Config":                    config,
		"a *Config":                   &config,
		"a struct holding a Config":   struct{ Config sim.Config }{config},
		"a struct holding a *Config":  struct{ Config *sim.Config }{&config},
		"a slice of Configs":          []sim.Config{config},
		"a struct holding a []Config": struct{ Configs []sim.Config }{[]sim.Config{config}},
	}
	verbs := []string{"%v", "%+v", "%#v", "%s", "%q", "%x", "%X", "%d", "%t"}

	for name, value := range values {
		for _, verb := range verbs {
			if text := fmt.Sprintf(verb, value); showsPassword(text) {
				t.Errorf("%s printed with %s shows the password: %s", name, verb, text)
			}
		}
	}
}

func TestAPrintedConfigStillShowsEverythingButThePassword(t *testing.T) {
	t.Parallel()
	config := configDemandingCredentials()

	plain, withFieldNames, goSyntax := fmt.Sprint(config), fmt.Sprintf("%+v", config), fmt.Sprintf("%#v", config)

	for _, want := range []string{"9575", "300234010000000", "echo.test", "&{CHAPMD5 {pilot <redacted>}}", "1s"} {
		if !strings.Contains(plain, want) {
			t.Errorf("%%v printed %q; want it to show %q", plain, want)
		}
	}
	for _, want := range []string{
		"{Model:9575 IMEI:300234010000000 SignalBars:4 Link:{CallSetup:0s", "Network:{GatewayAddress:10.64.0.1",
		"{Name:echo.test Address:192.0.2.7}", "Authentication:&{Protocol:CHAPMD5 Credentials:{Username:pilot Password:<redacted>}}",
		"EscapeGuard:1s}",
	} {
		if !strings.Contains(withFieldNames, want) {
			t.Errorf("%%+v printed %q; want it to show %q", withFieldNames, want)
		}
	}
	for _, want := range []string{"sim.Config{Model:\"9575\"", "Username:\"pilot\"", "Password:\"<redacted>\""} {
		if !strings.Contains(goSyntax, want) {
			t.Errorf("%%#v printed %q; want it to show %q", goSyntax, want)
		}
	}
	for _, verb := range []string{"%s", "%q", "%x", "%X", "%d", "%t"} {
		if text := fmt.Sprintf(verb, config); text != plain {
			t.Errorf("%s printed %q; want what %%v prints, %q", verb, text, plain)
		}
	}
}

func TestAConfigWithoutAuthenticationPrintsNoCredentials(t *testing.T) {
	t.Parallel()

	plain, goSyntax := fmt.Sprint(sim.DefaultConfig()), fmt.Sprintf("%#v", sim.DefaultConfig())

	if !strings.Contains(plain, "<nil>") || !strings.Contains(goSyntax, "Authentication:(*ppp.Authentication)(nil)") {
		t.Errorf("printed %q and %q; want the missing authentication shown as nil", plain, goSyntax)
	}
}
