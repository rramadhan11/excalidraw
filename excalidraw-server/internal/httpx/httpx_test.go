package httpx

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDecodeJSON(t *testing.T) {
	type body struct {
		Name string `json:"name"`
	}
	decode := func(raw string, max int64) error {
		r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(raw))
		var dst body
		return DecodeJSON(httptest.NewRecorder(), r, max, &dst)
	}

	if err := decode(`{"name":"a"}`, 1024); err != nil {
		t.Fatal(err)
	}
	if err := decode(`{"name":"a","extra":1}`, 1024); err == nil {
		t.Fatal("unknown field accepted")
	}
	if err := decode(`{"name":"a"}{"name":"b"}`, 1024); err == nil {
		t.Fatal("trailing data accepted")
	}
	if err := decode(`{"name":"`+strings.Repeat("a", 100)+`"}`, 50); !errors.Is(err, ErrBodyTooLarge) {
		t.Fatalf("want ErrBodyTooLarge, got %v", err)
	}
}

func TestRequireCSRFHeader(t *testing.T) {
	h := RequireCSRFHeader(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	cases := []struct {
		method, header string
		want           int
	}{
		{http.MethodGet, "", http.StatusOK},
		{http.MethodPost, "", http.StatusForbidden},
		{http.MethodPut, "wrong", http.StatusForbidden},
		{http.MethodDelete, CSRFHeaderValue, http.StatusOK},
	}
	for _, c := range cases {
		r := httptest.NewRequest(c.method, "/", nil)
		if c.header != "" {
			r.Header.Set(CSRFHeader, c.header)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != c.want {
			t.Errorf("%s with %q: got %d, want %d", c.method, c.header, w.Code, c.want)
		}
	}
}

func TestClientIP(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "10.0.0.1:1234"
	r.Header.Set("X-Real-IP", "1.2.3.4")
	if got := ClientIP(r, false); got != "10.0.0.1" {
		t.Fatalf("untrusted proxy header used: %s", got)
	}
	if got := ClientIP(r, true); got != "1.2.3.4" {
		t.Fatalf("trusted proxy header ignored: %s", got)
	}
}
