package middleware

import (
	"context"
	"net/http"
	"strings"

	"github.com/golang-jwt/jwt/v5"
)

type userIDKey struct{}

type CustomClaims struct {
	ID string `json:"id"`
	jwt.RegisteredClaims
}

var hmacMethods = []string{
	jwt.SigningMethodHS256.Alg(),
	jwt.SigningMethodHS384.Alg(),
	jwt.SigningMethodHS512.Alg(),
}

type AuthMiddleware struct {
	key    []byte
	parser *jwt.Parser
}

func NewAuthMiddleware(secret string) *AuthMiddleware {
	return &AuthMiddleware{
		key:    []byte(secret),
		parser: jwt.NewParser(jwt.WithValidMethods(hmacMethods)),
	}
}

func (am *AuthMiddleware) Verify(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader := r.Header.Get("Authorization")
		if authHeader == "" {
			http.Error(w, "missing authorization header", http.StatusUnauthorized)
			return
		}

		tokenString, ok := strings.CutPrefix(authHeader, "Bearer ")
		if !ok || tokenString == "" || strings.ContainsRune(tokenString, ' ') {
			http.Error(w, "invalid authorization header format", http.StatusUnauthorized)
			return
		}

		claims := &CustomClaims{}
		if _, err := am.parser.ParseWithClaims(tokenString, claims, am.keyFunc); err != nil || strings.TrimSpace(claims.ID) == "" {
			http.Error(w, "invalid token", http.StatusUnauthorized)
			return
		}

		ctx := context.WithValue(r.Context(), userIDKey{}, claims.ID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// AcceptQueryToken lets a `?token=<jwt>` query parameter stand in for the
// Authorization header, which browsers cannot set on a WebSocket handshake.
// A request that already carries the header is left untouched.
func AcceptQueryToken(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if token := r.URL.Query().Get("token"); r.Header.Get("Authorization") == "" && token != "" {
			r = r.Clone(r.Context())
			r.Header.Set("Authorization", "Bearer "+token)
		}
		next.ServeHTTP(w, r)
	})
}

func (am *AuthMiddleware) keyFunc(*jwt.Token) (any, error) {
	return am.key, nil
}

func GetUserID(r *http.Request) (string, bool) {
	userID, ok := r.Context().Value(userIDKey{}).(string)
	return userID, ok
}
