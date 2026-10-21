package middleware

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const secret = "jwt-secret"

func sign(t testing.TB, method jwt.SigningMethod, key any, claims jwt.MapClaims) string {
	t.Helper()
	token, err := jwt.NewWithClaims(method, claims).SignedString(key)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return token
}

type result struct {
	status int
	body   string
	userID string
	called bool
}

func verify(header string, set bool) result {
	var res result
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		res.called = true
		res.userID, _ = GetUserID(r)
	})
	req := httptest.NewRequest(http.MethodGet, "/ws", nil)
	if set {
		req.Header.Set("Authorization", header)
	}
	rec := httptest.NewRecorder()
	NewAuthMiddleware(secret).Verify(next).ServeHTTP(rec, req)
	res.status, res.body = rec.Code, rec.Body.String()
	return res
}

func TestVerifyAcceptsHMACTokens(t *testing.T) {
	for _, method := range []jwt.SigningMethod{jwt.SigningMethodHS256, jwt.SigningMethodHS384, jwt.SigningMethodHS512} {
		token := sign(t, method, []byte(secret), jwt.MapClaims{"id": "alice", "exp": time.Now().Add(time.Hour).Unix()})
		res := verify("Bearer "+token, true)
		if !res.called || res.userID != "alice" {
			t.Errorf("%s: called=%v user=%q status=%d", method.Alg(), res.called, res.userID, res.status)
		}
	}
}

func TestVerifyAcceptsTokenWithoutExpiry(t *testing.T) {
	res := verify("Bearer "+sign(t, jwt.SigningMethodHS256, []byte(secret), jwt.MapClaims{"id": "bob"}), true)
	if !res.called || res.userID != "bob" {
		t.Fatalf("called=%v user=%q", res.called, res.userID)
	}
}

func TestVerifyRejects(t *testing.T) {
	rsaKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	ecKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	key := []byte(secret)
	valid := sign(t, jwt.SigningMethodHS256, key, jwt.MapClaims{"id": "alice"})
	parts := strings.Split(valid, ".")

	tests := []struct {
		name   string
		header string
		set    bool
		body   string
	}{
		{"missing header", "", false, "missing authorization header"},
		{"empty header", "", true, "missing authorization header"},
		{"no scheme", valid, true, "invalid authorization header format"},
		{"basic scheme", "Basic " + valid, true, "invalid authorization header format"},
		{"lowercase bearer", "bearer " + valid, true, "invalid authorization header format"},
		{"bearer without token", "Bearer ", true, "invalid authorization header format"},
		{"bearer only", "Bearer", true, "invalid authorization header format"},
		{"extra segment", "Bearer " + valid + " extra", true, "invalid authorization header format"},
		{"double space", "Bearer  " + valid, true, "invalid authorization header format"},
		{"garbage", "Bearer garbage", true, "invalid token"},
		{"wrong secret", "Bearer " + sign(t, jwt.SigningMethodHS256, []byte("guess"), jwt.MapClaims{"id": "alice"}), true, "invalid token"},
		{"empty secret", "Bearer " + sign(t, jwt.SigningMethodHS256, []byte(""), jwt.MapClaims{"id": "alice"}), true, "invalid token"},
		{"alg none", "Bearer " + sign(t, jwt.SigningMethodNone, jwt.UnsafeAllowNoneSignatureType, jwt.MapClaims{"id": "admin"}), true, "invalid token"},
		{"rs256", "Bearer " + sign(t, jwt.SigningMethodRS256, rsaKey, jwt.MapClaims{"id": "alice"}), true, "invalid token"},
		{"es256", "Bearer " + sign(t, jwt.SigningMethodES256, ecKey, jwt.MapClaims{"id": "alice"}), true, "invalid token"},
		{"expired", "Bearer " + sign(t, jwt.SigningMethodHS256, key, jwt.MapClaims{"id": "alice", "exp": time.Now().Add(-time.Minute).Unix()}), true, "invalid token"},
		{"not yet valid", "Bearer " + sign(t, jwt.SigningMethodHS256, key, jwt.MapClaims{"id": "alice", "nbf": time.Now().Add(time.Hour).Unix()}), true, "invalid token"},
		{"missing id", "Bearer " + sign(t, jwt.SigningMethodHS256, key, jwt.MapClaims{"sub": "alice"}), true, "invalid token"},
		{"empty id", "Bearer " + sign(t, jwt.SigningMethodHS256, key, jwt.MapClaims{"id": ""}), true, "invalid token"},
		{"blank id", "Bearer " + sign(t, jwt.SigningMethodHS256, key, jwt.MapClaims{"id": "   "}), true, "invalid token"},
		{"numeric id", "Bearer " + sign(t, jwt.SigningMethodHS256, key, jwt.MapClaims{"id": 42}), true, "invalid token"},
		{"tampered payload", "Bearer " + parts[0] + "." + base64.RawURLEncoding.EncodeToString([]byte(`{"id":"admin"}`)) + "." + parts[2], true, "invalid token"},
		{"stripped signature", "Bearer " + parts[0] + "." + parts[1] + ".", true, "invalid token"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := verify(tt.header, tt.set)
			if res.status != http.StatusUnauthorized || res.called {
				t.Fatalf("status=%d called=%v; want 401 and no call", res.status, res.called)
			}
			if !strings.Contains(res.body, tt.body) {
				t.Errorf("body %q does not contain %q", res.body, tt.body)
			}
			if strings.Contains(res.body, secret) {
				t.Error("response leaks the secret")
			}
		})
	}
}

func verifyWithQuery(rawQuery, header string) result {
	var res result
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		res.called = true
		res.userID, _ = GetUserID(r)
	})
	req := httptest.NewRequest(http.MethodGet, "/ws?"+rawQuery, nil)
	if header != "" {
		req.Header.Set("Authorization", header)
	}
	rec := httptest.NewRecorder()
	AcceptQueryToken(NewAuthMiddleware(secret).Verify(next)).ServeHTTP(rec, req)
	res.status, res.body = rec.Code, rec.Body.String()
	return res
}

func TestAcceptQueryToken(t *testing.T) {
	key := []byte(secret)
	alice := sign(t, jwt.SigningMethodHS256, key, jwt.MapClaims{"id": "alice"})
	bob := sign(t, jwt.SigningMethodHS256, key, jwt.MapClaims{"id": "bob"})
	expired := sign(t, jwt.SigningMethodHS256, key, jwt.MapClaims{"id": "alice", "exp": time.Now().Add(-time.Minute).Unix()})
	none := sign(t, jwt.SigningMethodNone, jwt.UnsafeAllowNoneSignatureType, jwt.MapClaims{"id": "admin"})

	tests := []struct {
		name, query, header string
		wantUser            string
	}{
		{"query token", "token=" + alice, "", "alice"},
		{"header wins over query", "token=" + bob, "Bearer " + alice, "alice"},
		{"bad header is not rescued by query", "token=" + alice, "Bearer garbage", ""},
		{"forged query", "token=garbage", "", ""},
		{"expired query", "token=" + expired, "", ""},
		{"alg none query", "token=" + none, "", ""},
		{"empty query", "token=", "", ""},
		{"other parameter name", "access_token=" + alice, "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := verifyWithQuery(tt.query, tt.header)
			if tt.wantUser == "" {
				if res.status != http.StatusUnauthorized || res.called {
					t.Fatalf("status=%d called=%v; want 401 and no call", res.status, res.called)
				}
				return
			}
			if !res.called || res.userID != tt.wantUser {
				t.Fatalf("called=%v user=%q status=%d; want user %q", res.called, res.userID, res.status, tt.wantUser)
			}
		})
	}
}

func TestAcceptQueryTokenDoesNotMutateTheOriginalRequest(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/ws?token=abc", nil)
	AcceptQueryToken(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})).ServeHTTP(httptest.NewRecorder(), req)
	if got := req.Header.Get("Authorization"); got != "" {
		t.Errorf("original request header = %q", got)
	}
}

func TestGetUserIDWithoutMiddleware(t *testing.T) {
	if id, ok := GetUserID(httptest.NewRequest(http.MethodGet, "/", nil)); ok || id != "" {
		t.Errorf("GetUserID = %q, %v", id, ok)
	}
}

func FuzzVerify(f *testing.F) {
	f.Add("Bearer " + sign(f, jwt.SigningMethodHS256, []byte(secret), jwt.MapClaims{"id": "a"}))
	f.Add("Bearer a.b.c")
	f.Add("")
	auth := NewAuthMiddleware(secret)
	f.Fuzz(func(t *testing.T, header string) {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header["Authorization"] = []string{header}
		auth.Verify(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})).ServeHTTP(httptest.NewRecorder(), req)
	})
}

func BenchmarkVerify(b *testing.B) {
	token := sign(b, jwt.SigningMethodHS256, []byte(secret), jwt.MapClaims{"id": "alice"})
	handler := NewAuthMiddleware(secret).Verify(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	b.ReportAllocs()
	for b.Loop() {
		handler.ServeHTTP(httptest.NewRecorder(), req)
	}
}
