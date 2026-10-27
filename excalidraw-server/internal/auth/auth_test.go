package auth

import (
	"strings"
	"testing"
	"time"
)

func TestLimiter(t *testing.T) {
	now := time.Unix(0, 0)
	l := NewLimiter(2, time.Minute)
	l.now = func() time.Time { return now }

	l.Fail("k")
	if !l.Allow("k") {
		t.Fatal("1 failure should be allowed")
	}
	l.Fail("k")
	if l.Allow("k") {
		t.Fatal("2 failures should block")
	}
	if !l.Allow("other") {
		t.Fatal("keys must be independent")
	}

	now = now.Add(time.Minute)
	if !l.Allow("k") {
		t.Fatal("window expiry should unblock")
	}

	l.Fail("k")
	l.Fail("k")
	l.Reset("k")
	if !l.Allow("k") {
		t.Fatal("reset should unblock")
	}
}

func TestValidatePassword(t *testing.T) {
	if ValidatePassword("short") == nil {
		t.Fatal("short password accepted")
	}
	if ValidatePassword(strings.Repeat("a", MaxPasswordBytes+1)) == nil {
		t.Fatal("password over bcrypt's 72-byte limit accepted")
	}
	if err := ValidatePassword("correct horse"); err != nil {
		t.Fatal(err)
	}
}

func TestValidateEmail(t *testing.T) {
	for _, ok := range []string{"a@b.co", "rama.yuhu+x@example.com"} {
		if err := ValidateEmail(ok); err != nil {
			t.Errorf("%s rejected: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "nope", "Name <a@b.co>", "a@b.co extra"} {
		if ValidateEmail(bad) == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestTokenHashing(t *testing.T) {
	token, err := newToken()
	if err != nil {
		t.Fatal(err)
	}
	if len(token) != tokenEncoded {
		t.Fatalf("token length %d, want %d", len(token), tokenEncoded)
	}
	if string(hashToken(token)) == token || len(hashToken(token)) != 32 {
		t.Fatal("token must be stored as sha256")
	}
}
