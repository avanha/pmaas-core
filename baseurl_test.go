package core

import (
	"crypto/tls"
	"io"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/avanha/pmaas-core/config"
)

// captureStdout returns everything f printed to stdout. The server logs with fmt.Printf, so this is how
// a test sees what an operator would.
func captureStdout(t *testing.T, f func()) string {
	t.Helper()

	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}

	original := os.Stdout
	os.Stdout = writer

	defer func() { os.Stdout = original }()

	done := make(chan string)
	go func() {
		output, _ := io.ReadAll(reader)
		done <- string(output)
	}()

	f()

	_ = writer.Close()
	os.Stdout = original

	return <-done
}

func newBaseUrlTestServer(baseUrls ...string) *PMAAS {
	return &PMAAS{config: &config.Config{BaseURLs: baseUrls}}
}

func TestGetBaseUrl_MatchingHostReturnsTheConfiguredURLQuietly(t *testing.T) {
	pmaas := newBaseUrlTestServer("http://localhost:8090", "https://home.example.com")
	request := httptest.NewRequest("POST", "https://home.example.com/plugins/x", nil)

	var got string
	var err error

	output := captureStdout(t, func() { got, err = pmaas.getBaseUrl(request) })

	if err != nil || got != "https://home.example.com" {
		t.Fatalf("got %q, %v", got, err)
	}

	if output != "" {
		t.Fatalf("expected a match to log nothing, got %q", output)
	}
}

// This is the mistake that motivated the logging: changing the HTTP port without updating the base
// URLs. The request fails, and without a log line nobody can tell why.
func TestGetBaseUrl_MismatchIsLoggedWithWhatToDoAboutIt(t *testing.T) {
	pmaas := newBaseUrlTestServer("http://localhost:8090")
	request := httptest.NewRequest("POST", "http://localhost:8443/plugins/nestthermostat/oauthAttempt", nil)

	var err error

	output := captureStdout(t, func() { _, err = pmaas.getBaseUrl(request) })

	if err == nil {
		t.Fatal("expected an error")
	}

	for _, want := range []string{
		`"localhost:8443"`,                       // what the client actually used
		`"/plugins/nestthermostat/oauthAttempt"`, // which request it was
		`["http://localhost:8090"]`,              // what's configured
		`add "http://localhost:8443" to`,         // what to do
		"config.Config.BaseURLs",
	} {
		if !strings.Contains(output, want) {
			t.Errorf("log output is missing %s:\n%s", want, output)
		}
	}
}

func TestGetBaseUrl_MismatchOverTLSSuggestsHttps(t *testing.T) {
	pmaas := newBaseUrlTestServer("http://localhost:8090")
	request := httptest.NewRequest("POST", "https://localhost:8443/x", nil)
	request.TLS = &tls.ConnectionState{}

	output := captureStdout(t, func() { _, _ = pmaas.getBaseUrl(request) })

	if !strings.Contains(output, `add "https://localhost:8443" to`) {
		t.Errorf("expected an https suggestion, got:\n%s", output)
	}
}

// Host is chosen by the client, so it must not be able to inject log lines.
func TestDescribeBaseUrlMismatch_QuotesWhatCameFromTheRequest(t *testing.T) {
	request := httptest.NewRequest("POST", "http://localhost/x", nil)
	request.Host = "evil\nFAKE LOG LINE"

	message := describeBaseUrlMismatch(request, []string{"http://localhost:8090"})

	if strings.Contains(message, "\n") {
		t.Errorf("message contains a raw newline: %q", message)
	}
}

func newDefaultedServer(port int, withTlsCertificate bool) *PMAAS {
	pmaas := &PMAAS{config: &config.Config{HttpPort: port}}

	if withTlsCertificate {
		pmaas.tlsCertificateProvider = func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return nil, nil }
	}

	return pmaas
}

func TestNewConfig_LeavesBaseURLsToBeDefaulted(t *testing.T) {
	if got := config.NewConfig().BaseURLs; len(got) != 0 {
		t.Fatalf("expected no base URLs by default, so they follow the port and TLS, got %q", got)
	}
}

func TestBaseUrls_DefaultFollowsThePortAndWhetherTlsIsProvided(t *testing.T) {
	for _, c := range []struct {
		name string
		port int
		tls  bool
		want string
	}{
		{"plain http on the old default port", 8090, false, "http://localhost:8090"},
		{"plain http on another port", 8443, false, "http://localhost:8443"},
		{"tls on another port", 8443, true, "https://localhost:8443"},
		{"http on its default port", 80, false, "http://localhost"},
		{"https on its default port", 443, true, "https://localhost"},
		{"https on the http default port keeps it", 80, true, "https://localhost:80"},
		{"http on the https default port keeps it", 443, false, "http://localhost:443"},
	} {
		got := newDefaultedServer(c.port, c.tls).baseUrls()

		if len(got) != 1 || got[0] != c.want {
			t.Errorf("%s: got %q, want [%q]", c.name, got, c.want)
		}
	}
}

func TestBaseUrls_ExplicitConfigurationWinsOverTheDefault(t *testing.T) {
	pmaas := newDefaultedServer(8443, true)
	pmaas.config.BaseURLs = []string{"https://home.example.com"}

	got := pmaas.baseUrls()

	if len(got) != 1 || got[0] != "https://home.example.com" {
		t.Fatalf("got %q", got)
	}
}

// The mistake that started this: the HTTP port was changed after the config was created, which left
// the base URL pointing at the old one.
func TestGetBaseUrl_DefaultedURLMatchesTheServersActualPort(t *testing.T) {
	for _, tlsProvided := range []bool{false, true} {
		pmaas := newDefaultedServer(8443, tlsProvided)
		request := httptest.NewRequest("POST", "https://localhost:8443/plugins/nestthermostat/oauthAttempt", nil)

		var got string
		var err error

		output := captureStdout(t, func() { got, err = pmaas.getBaseUrl(request) })

		want := "http://localhost:8443"
		if tlsProvided {
			want = "https://localhost:8443"
		}

		if err != nil || got != want || output != "" {
			t.Errorf("tls=%v: got %q, %v, log %q; want %q quietly", tlsProvided, got, err, output, want)
		}
	}
}

func TestGetBaseUrl_DefaultedURLStillRejectsOtherHosts(t *testing.T) {
	pmaas := newDefaultedServer(8443, true)
	request := httptest.NewRequest("POST", "https://attacker.example.com:8443/x", nil)

	var err error

	output := captureStdout(t, func() { _, err = pmaas.getBaseUrl(request) })

	if err == nil {
		t.Fatal("a host the server wasn't configured for must not match")
	}

	// The log says what the defaulted list was, so it's clear where it came from.
	if !strings.Contains(output, `["https://localhost:8443"]`) {
		t.Errorf("expected the effective base URLs in the log, got:\n%s", output)
	}
}
