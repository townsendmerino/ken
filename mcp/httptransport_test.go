package mcp

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

func TestHTTPConfig_Validate(t *testing.T) {
	if err := (HTTPConfig{AuthToken: ""}).Validate(); !errors.Is(err, ErrHTTPNoAuthToken) {
		t.Errorf("empty token: got %v, want ErrHTTPNoAuthToken", err)
	}
	if err := (HTTPConfig{AuthToken: "   "}).Validate(); !errors.Is(err, ErrHTTPNoAuthToken) {
		t.Errorf("whitespace token must be treated as empty: got %v", err)
	}
	if err := (HTTPConfig{AuthToken: "s3cret"}).Validate(); err != nil {
		t.Errorf("valid token: got %v, want nil", err)
	}
}

func TestBearerAuthMiddleware(t *testing.T) {
	h := bearerAuthMiddleware("s3cret", okHandler())

	cases := []struct {
		name   string
		header string
		want   int
	}{
		{"no header", "", http.StatusUnauthorized},
		{"wrong scheme", "Basic s3cret", http.StatusUnauthorized},
		{"wrong token", "Bearer nope", http.StatusUnauthorized},
		{"token prefix only", "Bearer s3cre", http.StatusUnauthorized},
		{"correct", "Bearer s3cret", http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/", nil)
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Errorf("status = %d, want %d", rec.Code, tc.want)
			}
			if tc.want == http.StatusUnauthorized && rec.Header().Get("WWW-Authenticate") == "" {
				t.Error("401 response missing WWW-Authenticate header")
			}
		})
	}
}

func TestRateLimitMiddleware(t *testing.T) {
	const perMin = 3 // burst = 3
	h := rateLimitMiddleware(perMin, okHandler())

	// Burst of `perMin` from one IP is allowed; the next is throttled.
	for i := 0; i < perMin; i++ {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/", nil)
		req.RemoteAddr = "192.0.2.10:5555"
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d within burst: status %d, want 200", i+1, rec.Code)
		}
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.RemoteAddr = "192.0.2.10:5555"
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("over-burst request: status %d, want 429", rec.Code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Error("429 response missing Retry-After header")
	}

	// A different IP has its own bucket and is not affected.
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodPost, "/", nil)
	req2.RemoteAddr = "192.0.2.99:5555"
	h.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Errorf("fresh IP: status %d, want 200 (per-IP buckets)", rec2.Code)
	}
}
