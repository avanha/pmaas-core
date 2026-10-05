package http

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

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

var okHandler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

// The first request from a browser that hasn't loaded a page yet has no cookie and no header. It's
// refused by routes that require validation, which used to leave no trace on the server.
func TestXsrfRequired_RequestWithNoCookieIsRefusedAndLogged(t *testing.T) {
	handler := xsrfMiddleware(xsrfRequiredMiddleware(okHandler))
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest("POST", "/plugins/nestthermostat/oauthAttempt", nil)

	output := captureStdout(t, func() { handler(recorder, request) })

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("got status %d", recorder.Code)
	}

	for _, want := range []string{"xsrf: rejecting POST", `"/plugins/nestthermostat/oauthAttempt"`, "no ra-src cookie"} {
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
	handler := xsrfMiddleware(xsrfRequiredMiddleware(okHandler))
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest("POST", "/x", nil)
	request.AddCookie(&http.Cookie{Name: "ra-src", Value: "abc"})

	output := captureStdout(t, func() { handler(recorder, request) })

	if recorder.Code != http.StatusOK || output != "" {
		t.Fatalf("got status %d, output %q", recorder.Code, output)
	}
}

func TestXsrf_MismatchedHeaderIsRefusedAndLogged(t *testing.T) {
	handler := xsrfMiddleware(okHandler)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest("POST", "/x", nil)
	request.AddCookie(&http.Cookie{Name: "ra-src", Value: "abc"})
	request.Header.Set("ra", "something-else")

	output := captureStdout(t, func() { handler(recorder, request) })

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("got status %d", recorder.Code)
	}

	if !strings.Contains(output, "xsrf: rejecting POST") || !strings.Contains(output, "does not match the ra-src cookie") {
		t.Errorf("unexpected log output:\n%s", output)
	}
}
