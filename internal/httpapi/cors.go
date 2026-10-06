package httpapi

import (
	"net/http"
	"net/url"
	"strings"
)

// WithCORS lets browser-based Clients served from the listed origins call the API.
// The API authenticates with Bearer tokens and never cookies, so credentials are not
// allowed and a wildcard is never sent.
func WithCORS(origins []string) Option {
	return func(server *Server) {
		allowed := make(map[string]struct{}, len(origins))
		for _, origin := range origins {
			origin = strings.TrimSuffix(strings.TrimSpace(origin), "/")
			if parsed, err := url.Parse(origin); err == nil && parsed.Scheme != "" && parsed.Host != "" {
				allowed[origin] = struct{}{}
			}
		}
		server.corsOrigins = allowed
	}
}

func (s *Server) cors(next http.Handler) http.Handler {
	if len(s.corsOrigins) == 0 {
		return next
	}
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		origin := request.Header.Get("Origin")
		if origin == "" {
			next.ServeHTTP(writer, request)
			return
		}
		header := writer.Header()
		header.Add("Vary", "Origin")
		if _, ok := s.corsOrigins[origin]; !ok {
			// No CORS headers: the browser refuses the response.
			if isPreflight(request) {
				writer.WriteHeader(http.StatusForbidden)
				return
			}
			next.ServeHTTP(writer, request)
			return
		}
		header.Set("Access-Control-Allow-Origin", origin)
		if isPreflight(request) {
			header.Add("Vary", "Access-Control-Request-Method")
			header.Add("Vary", "Access-Control-Request-Headers")
			header.Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE")
			header.Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
			header.Set("Access-Control-Max-Age", "600")
			writer.WriteHeader(http.StatusNoContent)
			return
		}
		header.Set("Access-Control-Expose-Headers", "X-Request-ID, Retry-After, X-RateLimit-Limit, X-RateLimit-Remaining, X-RateLimit-Reset, X-RateLimit-Bucket")
		next.ServeHTTP(writer, request)
	})
}

func isPreflight(request *http.Request) bool {
	return request.Method == http.MethodOptions && request.Header.Get("Access-Control-Request-Method") != ""
}
