package httpapi

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

func corsHandler(origins []string) http.Handler {
	server := &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	WithCORS(origins)(server)
	return server.cors(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNoContent)
	}))
}

func TestCORSAllowsOnlyConfiguredOrigins(t *testing.T) {
	handler := corsHandler([]string{"http://localhost:5173", "tauri://localhost/"})

	request := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
	request.Header.Set("Origin", "tauri://localhost")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if got := recorder.Header().Get("Access-Control-Allow-Origin"); got != "tauri://localhost" {
		t.Fatalf("an allowed origin must be echoed, got %q", got)
	}
	if recorder.Header().Get("Access-Control-Allow-Credentials") != "" || recorder.Header().Get("Access-Control-Expose-Headers") == "" {
		t.Fatalf("credentials must never be allowed and rate limit headers must be exposed: %v", recorder.Header())
	}

	request = httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
	request.Header.Set("Origin", "https://evil.example")
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Header().Get("Access-Control-Allow-Origin") != "" || recorder.Header().Get("Vary") != "Origin" {
		t.Fatalf("an unknown origin must get no CORS grant: %v", recorder.Header())
	}

	request = httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Header().Get("Vary") != "" {
		t.Fatalf("a request without an Origin is not a CORS request: %v", recorder.Header())
	}
}

func TestCORSAnswersPreflightWithoutReachingTheAPI(t *testing.T) {
	handler := corsHandler([]string{"http://localhost:5173"})
	preflight := func(origin string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodOptions, "/api/v1/guilds", nil)
		request.Header.Set("Origin", origin)
		request.Header.Set("Access-Control-Request-Method", "PATCH")
		request.Header.Set("Access-Control-Request-Headers", "authorization, content-type")
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		return recorder
	}

	allowed := preflight("http://localhost:5173")
	if allowed.Code != http.StatusNoContent || allowed.Header().Get("Access-Control-Allow-Origin") != "http://localhost:5173" ||
		allowed.Header().Get("Access-Control-Allow-Headers") != "Authorization, Content-Type" ||
		allowed.Header().Get("Access-Control-Allow-Methods") != "GET, POST, PUT, PATCH, DELETE" {
		t.Fatalf("unexpected preflight answer: %d %v", allowed.Code, allowed.Header())
	}
	if denied := preflight("https://evil.example"); denied.Code != http.StatusForbidden || denied.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("a preflight from an unknown origin must be refused: %d %v", denied.Code, denied.Header())
	}
}

func TestCORSIsOffWithoutOrigins(t *testing.T) {
	server := &Server{}
	WithCORS(nil)(server)
	called := false
	handler := server.cors(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set("Origin", "http://localhost:5173")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if !called || recorder.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("without configured origins no CORS headers may be sent")
	}
}
