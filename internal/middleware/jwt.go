package middleware

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
)

type contextKey string

const (
	UserContextKey contextKey = "user"
)

type UserClaims struct {
	ID     string   `json:"id"`
	Groups []string `json:"groups"`
}

func JWTAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		authHeader := r.Header.Get("Authorization")
		if authHeader == "" {
			http.Error(w, "missing authorization header", http.StatusUnauthorized)
			return
		}

		parts := strings.Split(authHeader, " ")
		if len(parts) != 2 || parts[0] != "Bearer" {
			http.Error(w, "invalid authorization header format", http.StatusUnauthorized)
			return
		}

		token := parts[1]
		claims, err := parseJWT(token)
		if err != nil {
			http.Error(w, "invalid token", http.StatusUnauthorized)
			return
		}

		ctx := context.WithValue(r.Context(), UserContextKey, claims)
		next.ServeHTTP(w, r.WithContext(ctx))
	}
}

func parseJWT(token string) (*UserClaims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, http.ErrNotSupported
	}

	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, err
	}

	var payload map[string]interface{}
	if err := json.Unmarshal(payloadBytes, &payload); err != nil {
		return nil, err
	}

	claims := &UserClaims{}

	if id, ok := payload["id"].(string); ok {
		claims.ID = id
	}

	if groups, ok := payload["groups"].([]interface{}); ok {
		for _, g := range groups {
			if groupStr, ok := g.(string); ok {
				claims.Groups = append(claims.Groups, groupStr)
			}
		}
	}

	return claims, nil
}

func GetUserClaims(r *http.Request) *UserClaims {
	claims, ok := r.Context().Value(UserContextKey).(*UserClaims)
	if !ok {
		return nil
	}
	return claims
}

func CanAccessChannel(claims *UserClaims, channelID string) bool {
	if claims == nil {
		return false
	}

	if claims.ID == channelID {
		return true
	}

	for _, group := range claims.Groups {
		if group == channelID {
			return true
		}
	}

	return false
}
