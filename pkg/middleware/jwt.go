package middleware

/*

func JWTMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := r.Header.Get("Authorization")
		if token == "" || !strings.HasPrefix(token, "Bearer ") {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		token = strings.TrimPrefix(token, "Bearer ")

		developerLicense := "example_developer_license"

		ctx := context.WithValue(r.Context(), "developer_license", developerLicense)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
*/
