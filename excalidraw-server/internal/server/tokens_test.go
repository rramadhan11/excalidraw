package server

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type apiToken struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	CreatedAt  time.Time  `json:"createdAt"`
	LastUsedAt *time.Time `json:"lastUsedAt"`
	ExpiresAt  *time.Time `json:"expiresAt"`
	RevokedAt  *time.Time `json:"revokedAt"`
}

type createdToken struct {
	Token  apiToken `json:"token"`
	Secret string   `json:"secret"`
}

func (c *client) createToken(body map[string]any, want int) createdToken {
	c.e.t.Helper()
	if _, ok := body["password"]; !ok {
		body["password"] = testPassword
	}
	var res createdToken
	var out any = &res
	if want != http.StatusCreated {
		out = nil
	}
	c.mustDo("POST", "/api/tokens", body, out, want)
	return res
}

func (c *client) tokens() []apiToken {
	c.e.t.Helper()
	var res struct {
		Tokens []apiToken `json:"tokens"`
	}
	c.mustDo("GET", "/api/tokens", nil, &res, 200)
	if res.Tokens == nil {
		c.e.t.Fatal("tokens should be an array, got null")
	}
	return res.Tokens
}

func TestTokensSelfService(t *testing.T) {
	e := setup(t)
	alice := e.newUser("alice@example.com")
	bob := e.newUser("bob@example.com")

	if got := alice.tokens(); len(got) != 0 {
		t.Fatalf("new user has tokens: %+v", got)
	}

	// default lifetime is 90 days
	created := alice.createToken(map[string]any{"name": "  Claude Code  "}, 201)
	if !strings.HasPrefix(created.Secret, "exa_") || created.Token.Name != "Claude Code" {
		t.Fatalf("created = %+v", created)
	}
	if created.Token.ExpiresAt == nil ||
		created.Token.ExpiresAt.Sub(time.Now().AddDate(0, 0, 90)).Abs() > time.Minute {
		t.Errorf("expiresAt = %v, want ~90 days from now", created.Token.ExpiresAt)
	}
	// the secret works on the MCP endpoint straight away
	expect(t, "new token", mcpPost(e, http.Header{"Authorization": {"Bearer " + created.Secret}}), 200)

	never := alice.createToken(map[string]any{"name": "laptop", "expiresInDays": nil}, 201)
	if never.Token.ExpiresAt != nil {
		t.Errorf("null expiresInDays should never expire, got %v", never.Token.ExpiresAt)
	}
	year := alice.createToken(map[string]any{"name": "ci", "expiresInDays": 365}, 201)
	if year.Token.ExpiresAt == nil {
		t.Error("365 days should expire")
	}

	// the list carries metadata only, never the secret or its hash
	resp := alice.rawResponse("GET", "/api/tokens", nil, "", true)
	b, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	listed := string(b)
	for _, secret := range []string{created.Secret, never.Secret, year.Secret} {
		if strings.Contains(listed, secret) || strings.Contains(listed, strings.TrimPrefix(secret, "exa_")) {
			t.Fatal("token list leaks a secret")
		}
	}
	if strings.Contains(listed, "hash") {
		t.Fatalf("token list exposes hashes: %s", listed)
	}
	if got := alice.tokens(); len(got) != 3 {
		t.Fatalf("alice has %d tokens, want 3", len(got))
	}

	// isolation: bob neither sees nor revokes alice's tokens
	if got := bob.tokens(); len(got) != 0 {
		t.Fatalf("bob sees alice's tokens: %+v", got)
	}
	bob.mustDo("DELETE", "/api/tokens/"+created.Token.ID, nil, nil, 404)
	bob.mustDo("DELETE", "/api/tokens/not-a-uuid", nil, nil, 404)
	expect(t, "token after bob's revoke", mcpPost(e, http.Header{"Authorization": {"Bearer " + created.Secret}}), 200)

	// revoke takes effect at once and is idempotent
	alice.mustDo("DELETE", "/api/tokens/"+created.Token.ID, nil, nil, 204)
	expect(t, "revoked token", mcpPost(e, http.Header{"Authorization": {"Bearer " + created.Secret}}), 401)
	alice.mustDo("DELETE", "/api/tokens/"+created.Token.ID, nil, nil, 204)
	for _, tok := range alice.tokens() {
		if tok.ID == created.Token.ID && tok.RevokedAt == nil {
			t.Error("revoked token is listed without revokedAt")
		}
	}

	// validation
	alice.createToken(map[string]any{"name": "x", "expiresInDays": 7}, 400)
	alice.createToken(map[string]any{"name": "x", "expiresInDays": "90"}, 400)
	alice.createToken(map[string]any{"name": "   "}, 400)
	alice.createToken(map[string]any{"name": strings.Repeat("n", 101)}, 400)
	alice.createToken(map[string]any{"name": "x", "extra": true}, 400)
}

func TestTokensRequireSessionAndCSRF(t *testing.T) {
	e := setup(t)
	alice := e.newUser("alice@example.com")
	anon := e.anonymous()

	anon.mustDo("GET", "/api/tokens", nil, nil, 401)
	anon.mustDo("POST", "/api/tokens", map[string]any{"name": "x", "password": testPassword}, nil, 401)
	anon.mustDo("DELETE", "/api/tokens/x", nil, nil, 401)

	body := strings.NewReader(`{"name":"x","password":"` + testPassword + `"}`)
	if got := alice.raw("POST", "/api/tokens", body, "application/json", nil, false); got != 403 {
		t.Errorf("create without CSRF header: got %d, want 403", got)
	}
	if got := alice.raw("DELETE", "/api/tokens/x", nil, "", nil, false); got != 403 {
		t.Errorf("revoke without CSRF header: got %d, want 403", got)
	}

	// a bearer token cannot manage tokens: the browser API is cookie-only
	created := alice.createToken(map[string]any{"name": "x"}, 201)
	req, _ := http.NewRequest("GET", e.srv.URL+"/api/tokens", nil)
	req.Header.Set("Authorization", "Bearer "+created.Secret)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Errorf("bearer token on /api/tokens: got %d, want 401", resp.StatusCode)
	}
}

func TestTokensPasswordCheck(t *testing.T) {
	e := setup(t)
	alice := e.newUser("alice@example.com")

	// a wrong password is 403 (the session stays valid) and mints nothing
	alice.createToken(map[string]any{"name": "x", "password": "wrong password"}, 403)
	if got := alice.tokens(); len(got) != 0 {
		t.Fatalf("wrong password created a token: %+v", got)
	}
	alice.mustDo("GET", "/api/auth/me", nil, nil, 200)

	// it shares the login limiter: after the allowed failures even the right
	// password is refused, and so is logging in
	for range 4 {
		alice.createToken(map[string]any{"name": "x", "password": "wrong password"}, 403)
	}
	alice.createToken(map[string]any{"name": "x"}, 429)
	e.anonymous().mustDo("POST", "/api/auth/login",
		map[string]string{"email": "alice@example.com", "password": testPassword}, nil, 429)
}

func TestTokensActiveLimit(t *testing.T) {
	e := setup(t)
	alice := e.newUser("alice@example.com")

	var first createdToken
	for i := range 20 {
		c := alice.createToken(map[string]any{"name": "t"}, 201)
		if i == 0 {
			first = c
		}
	}
	alice.createToken(map[string]any{"name": "one too many"}, 409)

	// revoked and expired tokens don't count
	alice.mustDo("DELETE", "/api/tokens/"+first.Token.ID, nil, nil, 204)
	second := alice.createToken(map[string]any{"name": "replacement"}, 201)
	alice.createToken(map[string]any{"name": "one too many"}, 409)
	if _, err := e.pool.Exec(context.Background(),
		`UPDATE api_tokens SET expires_at = now() - interval '1 day' WHERE id = $1`, second.Token.ID); err != nil {
		t.Fatal(err)
	}
	alice.createToken(map[string]any{"name": "after expiry"}, 201)
}
