package http

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// capturedLog is a logger that writes to a buffer, and the buffer.
func capturedLog() (*slog.Logger, *strings.Builder) {
	var out strings.Builder

	return slog.New(slog.NewTextHandler(&out, nil)), &out
}

var okHandler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

// The first request from a browser that hasn't loaded a page yet has no cookie and no header. It's
// refused by routes that require validation, which used to leave no trace on the server.
func TestXsrfRequired_RequestWithNoCookieIsRefusedAndLogged(t *testing.T) {
	log, out := capturedLog()
	handler := xsrfMiddleware(log, xsrfRequiredMiddleware(log, okHandler))
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest("POST", "/plugins/nestthermostat/oauthAttempt", nil)

	handler(recorder, request)

	output := out.String()

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("got status %d", recorder.Code)
	}

	for _, want := range []string{"xsrf: rejecting", "method=POST", "path=/plugins/nestthermostat/oauthAttempt", "no ra-src cookie", "level=WARN"} {
		if !strings.Contains(output, want) {
			t.Errorf("log output is missing %q:\n%s", want, output)
		}
	}

	// ...and it set the cookie, so that the next request can pass.
	if len(recorder.Result().Cookies()) == 0 {
		t.Error("expected the ra-src cookie to be set for next time")
	}
}

func TestXsrfRequired_RequestWithTheCookieIsLetThroughQuietly(t *testing.T) {
	log, out := capturedLog()
	handler := xsrfMiddleware(log, xsrfRequiredMiddleware(log, okHandler))
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest("POST", "/x", nil)
	request.AddCookie(&http.Cookie{Name: "ra-src", Value: "abc"})

	handler(recorder, request)

	output := out.String()

	if recorder.Code != http.StatusOK || output != "" {
		t.Fatalf("got status %d, output %q", recorder.Code, output)
	}
}

func TestXsrf_MismatchedHeaderIsRefusedAndLogged(t *testing.T) {
	log, out := capturedLog()
	handler := xsrfMiddleware(log, okHandler)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest("POST", "/x", nil)
	request.AddCookie(&http.Cookie{Name: "ra-src", Value: "abc"})
	request.Header.Set("ra", "something-else")

	handler(recorder, request)

	output := out.String()

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("got status %d", recorder.Code)
	}

	if !strings.Contains(output, "xsrf: rejecting") || !strings.Contains(output, "method=POST") || !strings.Contains(output, "does not match the ra-src cookie") {
		t.Errorf("unexpected log output:\n%s", output)
	}
}
