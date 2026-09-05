package httpapi

import (
	"net/http"
	"strings"
)

// AllowOrigins enables browser and Tauri WebView clients to call the API from
// explicitly trusted origins. Requests without Origin are unchanged.
func AllowOrigins(next http.Handler, origins []string) http.Handler {
	allowed := make(map[string]struct{}, len(origins))
	for _, origin := range origins {
		if normalized := strings.TrimSuffix(strings.TrimSpace(origin), "/"); normalized != "" {
			allowed[normalized] = struct{}{}
		}
	}
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		origin := strings.TrimSuffix(request.Header.Get("Origin"), "/")
		if origin == "" {
			next.ServeHTTP(writer, request)
			return
		}
		if _, ok := allowed[origin]; !ok {
			if request.Method == http.MethodOptions {
				http.Error(writer, "origin is not allowed", http.StatusForbidden)
				return
			}
			next.ServeHTTP(writer, request)
			return
		}

		header := writer.Header()
		header.Set("Access-Control-Allow-Origin", origin)
		header.Set("Access-Control-Allow-Methods", "GET, HEAD, POST, PUT, PATCH, DELETE, OPTIONS")
		header.Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
		header.Set("Access-Control-Expose-Headers", "Location, X-Request-ID, X-RateLimit-Limit, X-RateLimit-Remaining, X-RateLimit-Reset")
		header.Set("Access-Control-Max-Age", "600")
		header.Add("Vary", "Origin")
		if request.Method == http.MethodOptions {
			writer.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(writer, request)
	})
}
