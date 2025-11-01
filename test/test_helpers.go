package test

import (
	"chat-microservice/internal/middleware"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func GenerateTestJWT(userID, secret string) (string, error) {
	claims := middleware.CustomClaims{
		ID: userID,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(1 * time.Hour)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			Issuer:    "test-issuer",
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(secret))
}
