package server

// Integration tests against a real Postgres. They are skipped unless
// TEST_DATABASE_URL points at a disposable database, e.g.:
//
//	docker run -d --name excalidraw-test-pg -e POSTGRES_PASSWORD=test -p 55432:5432 postgres:17-alpine
//	TEST_DATABASE_URL=postgres://postgres:test@localhost:55432/postgres?sslmode=disable go test ./...
//
// WARNING: every test wipes all users, sessions, boards and files.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rramadhan11/excalidraw/excalidraw-server/internal/auth"
	"github.com/rramadhan11/excalidraw/excalidraw-server/internal/db"
	"github.com/rramadhan11/excalidraw/excalidraw-server/internal/httpx"
)

const testPassword = "correct horse battery"

type env struct {
	t    *testing.T
	srv  *httptest.Server
	pool *pgxpool.Pool
}

func setup(t *testing.T) *env {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	if _, err := db.Migrate(ctx, url); err != nil {
		t.Fatal(err)
	}
	pool, err := db.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(ctx, `TRUNCATE users CASCADE`); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := httptest.NewServer(NewHandler(pool, Config{}, logger))
	t.Cleanup(srv.Close)
	return &env{t: t, srv: srv, pool: pool}
}

type client struct {
	e    *env
	http *http.Client
}

func (e *env) newUser(email string) *client {
	e.t.Helper()
	if _, err := auth.NewUsers(e.pool).Create(context.Background(), email, "Test", testPassword); err != nil {
		e.t.Fatal(err)
	}
	return e.anonymous()
}

func (e *env) anonymous() *client {
	jar, _ := cookiejar.New(nil)
	return &client{e: e, http: &http.Client{Jar: jar}}
}

// do sends a request with the CSRF header and decodes a JSON response into out.
func (c *client) do(method, path string, body any, out any) int {
	c.e.t.Helper()
	return c.doRaw(method, path, body, out, map[string]string{httpx.CSRFHeader: httpx.CSRFHeaderValue})
}

func (c *client) doRaw(method, path string, body any, out any, headers map[string]string) int {
	c.e.t.Helper()
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			c.e.t.Fatal(err)
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.e.srv.URL+path, reader)
	if err != nil {
		c.e.t.Fatal(err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		c.e.t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			c.e.t.Fatalf("%s %s: decode response: %v", method, path, err)
		}
	}
	return resp.StatusCode
}

func (c *client) login(email string) {
	c.e.t.Helper()
	if code := c.do("POST", "/api/auth/login", map[string]string{"email": email, "password": testPassword}, nil); code != 200 {
		c.e.t.Fatalf("login: %d", code)
	}
}

type board struct {
	ID      string          `json:"id"`
	Name    string          `json:"name"`
	Version int64           `json:"version"`
	Scene   json.RawMessage `json:"scene"`
}

func (c *client) createBoard(name string) board {
	c.e.t.Helper()
	var b board
	if code := c.do("POST", "/api/boards", map[string]string{"name": name}, &b); code != http.StatusCreated {
		c.e.t.Fatalf("create board: %d", code)
	}
	return b
}

func jsonEqual(t *testing.T, a, b json.RawMessage) bool {
	t.Helper()
	var va, vb any
	if err := json.Unmarshal(a, &va); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &vb); err != nil {
		t.Fatal(err)
	}
	return reflect.DeepEqual(va, vb)
}

func expect(t *testing.T, what string, got, want int) {
	t.Helper()
	if got != want {
		t.Fatalf("%s: got status %d, want %d", what, got, want)
	}
}

func TestAuth(t *testing.T) {
	e := setup(t)
	c := e.newUser("Alice@Example.com")

	expect(t, "me without session", c.do("GET", "/api/auth/me", nil, nil), 401)
	expect(t, "boards without session", c.do("GET", "/api/boards", nil, nil), 401)

	expect(t, "wrong password",
		c.do("POST", "/api/auth/login", map[string]string{"email": "alice@example.com", "password": "wrong password"}, nil), 401)
	expect(t, "unknown email",
		c.do("POST", "/api/auth/login", map[string]string{"email": "nobody@example.com", "password": testPassword}, nil), 401)
	expect(t, "login without CSRF header",
		c.doRaw("POST", "/api/auth/login", map[string]string{"email": "alice@example.com", "password": testPassword}, nil, nil), 403)

	// email is case-insensitive
	c.do("POST", "/api/auth/login", map[string]string{"email": "ALICE@example.com ", "password": testPassword}, nil)
	var me struct {
		User auth.User `json:"user"`
	}
	expect(t, "me", c.do("GET", "/api/auth/me", nil, &me), 200)
	if me.User.Email != "alice@example.com" {
		t.Fatalf("me: %+v", me.User)
	}

	expect(t, "logout", c.do("POST", "/api/auth/logout", nil, nil), 204)
	expect(t, "me after logout", c.do("GET", "/api/auth/me", nil, nil), 401)
}

func TestPasswordChangeRevokesSessions(t *testing.T) {
	e := setup(t)
	c := e.newUser("alice@example.com")
	c.login("alice@example.com")

	if err := auth.NewUsers(e.pool).SetPassword(context.Background(), "alice@example.com", "a new password"); err != nil {
		t.Fatal(err)
	}
	expect(t, "me after password change", c.do("GET", "/api/auth/me", nil, nil), 401)
}

func TestLoginRateLimit(t *testing.T) {
	e := setup(t)
	c := e.newUser("alice@example.com")
	wrong := map[string]string{"email": "alice@example.com", "password": "wrong password"}
	for i := 0; i < 5; i++ {
		expect(t, "failed login", c.do("POST", "/api/auth/login", wrong, nil), 401)
	}
	right := map[string]string{"email": "alice@example.com", "password": testPassword}
	expect(t, "login after 5 failures", c.do("POST", "/api/auth/login", right, nil), 429)
}

func TestBoardLifecycle(t *testing.T) {
	e := setup(t)
	c := e.newUser("alice@example.com")
	c.login("alice@example.com")

	b := c.createBoard("  ")
	if b.Name != "Untitled" || b.Version != 1 || string(b.Scene) != `{"elements":[],"appState":{}}` {
		t.Fatalf("new board: %+v", b)
	}
	second := c.createBoard("Second")

	// save, then a stale save conflicts
	scene := json.RawMessage(`{"elements":[{"id":"r1","type":"rectangle","text":"<b>&</b>"}],"appState":{"viewBackgroundColor":"#ffffff"}}`)
	var saved struct {
		Version int64 `json:"version"`
	}
	expect(t, "save scene", c.do("PUT", "/api/boards/"+b.ID+"/scene", map[string]any{"version": 1, "scene": scene}, &saved), 200)
	if saved.Version != 2 {
		t.Fatalf("version after save: %d", saved.Version)
	}
	var conflict struct {
		Version int64 `json:"version"`
	}
	expect(t, "stale save", c.do("PUT", "/api/boards/"+b.ID+"/scene", map[string]any{"version": 1, "scene": scene}, &conflict), 409)
	if conflict.Version != 2 {
		t.Fatalf("conflict should report current version, got %d", conflict.Version)
	}
	expect(t, "save without version", c.do("PUT", "/api/boards/"+b.ID+"/scene", map[string]any{"scene": scene}, nil), 400)
	expect(t, "save invalid scene", c.do("PUT", "/api/boards/"+b.ID+"/scene", map[string]any{"version": 2, "scene": map[string]any{"elements": "x"}}, nil), 400)

	var got board
	expect(t, "get board", c.do("GET", "/api/boards/"+b.ID, nil, &got), 200)
	if got.Version != 2 || !jsonEqual(t, got.Scene, scene) {
		t.Fatalf("scene not stored intact:\n got  %s\n want %s", got.Scene, scene)
	}

	var renamed board
	expect(t, "rename", c.do("PATCH", "/api/boards/"+b.ID, map[string]string{"name": "Plan"}, &renamed), 200)
	if renamed.Name != "Plan" {
		t.Fatalf("rename: %+v", renamed)
	}
	expect(t, "rename to empty", c.do("PATCH", "/api/boards/"+b.ID, map[string]string{"name": " "}, nil), 400)

	var list struct {
		Boards []board `json:"boards"`
	}
	expect(t, "list", c.do("GET", "/api/boards", nil, &list), 200)
	if len(list.Boards) != 2 || list.Boards[0].ID != b.ID || list.Boards[1].ID != second.ID {
		t.Fatalf("list should be ordered by last update: %+v", list.Boards)
	}

	expect(t, "delete", c.do("DELETE", "/api/boards/"+b.ID, nil, nil), 204)
	expect(t, "get deleted", c.do("GET", "/api/boards/"+b.ID, nil, nil), 404)
	expect(t, "malformed id", c.do("GET", "/api/boards/not-a-uuid", nil, nil), 404)
}

func TestFiles(t *testing.T) {
	e := setup(t)
	c := e.newUser("alice@example.com")
	c.login("alice@example.com")
	b := c.createBoard("Images")
	path := "/api/boards/" + b.ID + "/files/"

	v1 := map[string]any{"mimeType": "image/png", "dataURL": "data:image/png;base64,AAAA", "created": 1700000000000}
	expect(t, "upload", c.do("PUT", path+"img1", v1, nil), 204)
	expect(t, "re-upload same version", c.do("PUT", path+"img1", map[string]any{
		"mimeType": "image/png", "dataURL": "data:image/png;base64,BBBB", "created": 1700000000000,
	}, nil), 204)

	type filesResp struct {
		Files []struct {
			ID      string `json:"id"`
			DataURL string `json:"dataURL"`
			Version int    `json:"version"`
			Created int64  `json:"created"`
		} `json:"files"`
		ErroredIds []string `json:"erroredIds"`
	}
	var r filesResp
	expect(t, "get files", c.do("GET", "/api/boards/"+b.ID+"/files?ids=img1,missing", nil, &r), 200)
	if len(r.Files) != 1 || r.Files[0].DataURL != "data:image/png;base64,AAAA" || r.Files[0].Created != 1700000000000 {
		t.Fatalf("same-version re-upload must not overwrite: %+v", r)
	}
	if len(r.ErroredIds) != 1 || r.ErroredIds[0] != "missing" {
		t.Fatalf("erroredIds: %+v", r.ErroredIds)
	}

	expect(t, "upload newer version", c.do("PUT", path+"img1", map[string]any{
		"mimeType": "image/png", "dataURL": "data:image/png;base64,CCCC", "created": 1700000000000, "version": 2,
	}, nil), 204)
	r = filesResp{}
	c.do("GET", "/api/boards/"+b.ID+"/files?ids=img1", nil, &r)
	if r.Files[0].DataURL != "data:image/png;base64,CCCC" || r.Files[0].Version != 2 {
		t.Fatalf("newer version should replace: %+v", r.Files)
	}

	expect(t, "bad mime", c.do("PUT", path+"img2", map[string]any{
		"mimeType": "text/html", "dataURL": "data:text/html,<script>", "created": 1,
	}, nil), 400)
	expect(t, "too large", c.do("PUT", path+"img3", map[string]any{
		"mimeType": "image/png", "dataURL": "data:image/png;base64," + strings.Repeat("A", 6<<20), "created": 1,
	}, nil), 413)

	expect(t, "delete board", c.do("DELETE", "/api/boards/"+b.ID, nil, nil), 204)
	var count int
	if err := e.pool.QueryRow(context.Background(), `SELECT count(*) FROM board_files WHERE board_id = $1`, b.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("files not deleted with board: %d left", count)
	}
}

// Another user's board must look exactly like a missing one on every route.
func TestBoardIsolation(t *testing.T) {
	e := setup(t)
	alice := e.newUser("alice@example.com")
	alice.login("alice@example.com")
	bob := e.newUser("bob@example.com")
	bob.login("bob@example.com")

	b := alice.createBoard("Private")
	alice.do("PUT", "/api/boards/"+b.ID+"/files/img1",
		map[string]any{"mimeType": "image/png", "dataURL": "data:image/png;base64,AAAA", "created": 1}, nil)

	routes := []struct {
		method, path string
		body         any
	}{
		{"GET", "/api/boards/" + b.ID, nil},
		{"PATCH", "/api/boards/" + b.ID, map[string]string{"name": "pwned"}},
		{"PUT", "/api/boards/" + b.ID + "/scene", map[string]any{"version": 1, "scene": map[string]any{"elements": []any{}}}},
		{"GET", "/api/boards/" + b.ID + "/files?ids=img1", nil},
		{"PUT", "/api/boards/" + b.ID + "/files/img2", map[string]any{"mimeType": "image/png", "dataURL": "data:image/png;base64,AAAA", "created": 1}},
		{"DELETE", "/api/boards/" + b.ID, nil},
	}
	for _, r := range routes {
		expect(t, fmt.Sprintf("bob %s %s", r.method, r.path), bob.do(r.method, r.path, r.body, nil), 404)
	}

	var list struct {
		Boards []board `json:"boards"`
	}
	bob.do("GET", "/api/boards", nil, &list)
	if len(list.Boards) != 0 {
		t.Fatalf("bob sees alice's boards: %+v", list.Boards)
	}
	var got board
	expect(t, "alice still has her board", alice.do("GET", "/api/boards/"+b.ID, nil, &got), 200)
	if got.Name != "Private" || got.Version != 1 {
		t.Fatalf("board was modified by another user: %+v", got)
	}
}
