package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAllowOriginsHandlesTrustedPreflight(t *testing.T) {
	handler := AllowOrigins(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("preflight must not reach the application handler")
	}), []string{"http://127.0.0.1:5173"})
	request := httptest.NewRequest(http.MethodOptions, "/api/v1/guilds", nil)
	request.Header.Set("Origin", "http://127.0.0.1:5173")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusNoContent || response.Header().Get("Access-Control-Allow-Origin") != "http://127.0.0.1:5173" {
		t.Fatalf("unexpected preflight response: status=%d headers=%v", response.Code, response.Header())
	}
}

func TestAllowOriginsRejectsUntrustedPreflight(t *testing.T) {
	handler := AllowOrigins(http.NotFoundHandler(), []string{"http://127.0.0.1:5173"})
	request := httptest.NewRequest(http.MethodOptions, "/api/v1/guilds", nil)
	request.Header.Set("Origin", "https://untrusted.example")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusForbidden || response.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("unexpected rejected preflight response: status=%d headers=%v", response.Code, response.Header())
	}
}
