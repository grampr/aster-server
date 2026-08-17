package httpapi

import (
	"context"
	"net/http"
	"runtime/debug"

	"github.com/google/uuid"
)

type requestIDContextKey struct{}

func (s *Server) requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		id, err := uuid.NewV7()
		if err != nil {
			s.logger.Error("failed to generate request ID", "error", err)
			http.Error(writer, "Internal server error", http.StatusInternalServerError)
			return
		}
		value := id.String()
		writer.Header().Set("X-Request-ID", value)
		contextWithID := context.WithValue(request.Context(), requestIDContextKey{}, value)
		next.ServeHTTP(writer, request.WithContext(contextWithID))
	})
}

func (s *Server) recoverPanic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		defer func() {
			if recovered := recover(); recovered != nil {
				s.logger.Error("request panic",
					"request_id", requestIDFromContext(request),
					"method", request.Method,
					"path", request.URL.Path,
					"panic", recovered,
					"stack", string(debug.Stack()),
				)
				s.writeError(writer, request, http.StatusInternalServerError, "INTERNAL_ERROR", "Internal server error", nil)
			}
		}()
		next.ServeHTTP(writer, request)
	})
}

func requestIDFromContext(request *http.Request) string {
	value, _ := request.Context().Value(requestIDContextKey{}).(string)
	return value
}
