package middleware

import (
	"context"
	"net/http"
	"strings"
)

func JWTMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := r.Header.Get("Authorization")
		if token == "" || !strings.HasPrefix(token, "Bearer ") {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		// Extract token (remove "Bearer " prefix)
		token = strings.TrimPrefix(token, "Bearer ")

		// Parse and validate token (replace with actual logic)
		// Assuming a static `developer_license` for testing.
		developerLicense := "example_developer_license"

		// Add developer_license to context
		ctx := context.WithValue(r.Context(), "developer_license", developerLicense)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
