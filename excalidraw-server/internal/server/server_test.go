package server

// Integration tests against a real Postgres. They are skipped unless
// TEST_DATABASE_URL points at a disposable database, e.g.:
//
//	docker run -d --name excalidraw-test-pg -e POSTGRES_PASSWORD=test -p 55432:5432 postgres:17-alpine
//	TEST_DATABASE_URL=postgres://postgres:test@localhost:55432/postgres?sslmode=disable go test ./...
//
// WARNING: every test wipes all data.

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
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rramadhan11/excalidraw/excalidraw-server/internal/auth"
	"github.com/rramadhan11/excalidraw/excalidraw-server/internal/db"
	"github.com/rramadhan11/excalidraw/excalidraw-server/internal/httpx"
	"github.com/rramadhan11/excalidraw/excalidraw-server/internal/workspaces"
)

const testPassword = "correct horse battery"

var (
	pngBytes  = []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1A, '\n', 1, 2, 3}
	jpegBytes = []byte{0xFF, 0xD8, 0xFF, 0xE0, 4, 5, 6}
)

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
	if _, err := pool.Exec(ctx, `TRUNCATE users, workspaces CASCADE`); err != nil {
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
	// the user's personal workspace
	workspaceID string
}

// newUser creates a user (with their personal workspace, like the CLI) and
// returns a logged-in client.
func (e *env) newUser(email string) *client {
	e.t.Helper()
	if _, err := auth.NewUsers(e.pool).Create(context.Background(), email, "Test", testPassword,
		workspaces.ProvisionPersonal); err != nil {
		e.t.Fatal(err)
	}
	c := e.anonymous()
	c.login(email)
	var res struct {
		Workspaces []workspace `json:"workspaces"`
	}
	c.mustDo("GET", "/api/workspaces", nil, &res, 200)
	if len(res.Workspaces) != 1 {
		e.t.Fatalf("new user should have exactly one workspace, got %+v", res.Workspaces)
	}
	c.workspaceID = res.Workspaces[0].ID
	return c
}

func (e *env) anonymous() *client {
	jar, _ := cookiejar.New(nil)
	return &client{e: e, http: &http.Client{Jar: jar}}
}

// do sends a request with the CSRF header and decodes a JSON response into out.
func (c *client) do(method, path string, body any, out any) int {
	c.e.t.Helper()
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			c.e.t.Fatal(err)
		}
		reader = bytes.NewReader(b)
	}
	return c.raw(method, path, reader, "", out, true)
}

func (c *client) mustDo(method, path string, body any, out any, want int) {
	c.e.t.Helper()
	if got := c.do(method, path, body, out); got != want {
		c.e.t.Fatalf("%s %s: got status %d, want %d", method, path, got, want)
	}
}

func (c *client) raw(method, path string, body io.Reader, contentType string, out any, csrf bool) int {
	c.e.t.Helper()
	resp := c.rawResponse(method, path, body, contentType, csrf)
	defer resp.Body.Close()
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			c.e.t.Fatalf("%s %s: decode response: %v", method, path, err)
		}
	}
	return resp.StatusCode
}

func (c *client) rawResponse(method, path string, body io.Reader, contentType string, csrf bool) *http.Response {
	c.e.t.Helper()
	req, err := http.NewRequest(method, c.e.srv.URL+path, body)
	if err != nil {
		c.e.t.Fatal(err)
	}
	if csrf {
		req.Header.Set(httpx.CSRFHeader, httpx.CSRFHeaderValue)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		c.e.t.Fatal(err)
	}
	return resp
}

func (c *client) login(email string) {
	c.e.t.Helper()
	c.mustDo("POST", "/api/auth/login", map[string]string{"email": email, "password": testPassword}, nil, 200)
}

type workspace struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Role       string `json:"role"`
	BoardCount int    `json:"boardCount"`
}

type collection struct {
	ID          string `json:"id"`
	WorkspaceID string `json:"workspaceId"`
	Name        string `json:"name"`
	BoardCount  int    `json:"boardCount"`
}

type board struct {
	ID           string                 `json:"id"`
	Name         string                 `json:"name"`
	WorkspaceID  string                 `json:"workspaceId"`
	CollectionID *string                `json:"collectionId"`
	CreatedBy    *struct{ Name string } `json:"createdBy"`
	UpdatedAt    time.Time              `json:"updatedAt"`
	Thumbnail    *time.Time             `json:"thumbnailUpdatedAt"`
	Version      int64                  `json:"version"`
	Scene        json.RawMessage        `json:"scene"`
}

type boardList struct {
	Boards     []board `json:"boards"`
	NextCursor string  `json:"nextCursor"`
}

func (c *client) createBoard(workspaceID, name string, collectionID *string) board {
	c.e.t.Helper()
	var b board
	c.mustDo("POST", "/api/workspaces/"+workspaceID+"/boards",
		map[string]any{"name": name, "collectionId": collectionID}, &b, http.StatusCreated)
	return b
}

func (c *client) createCollection(workspaceID, name string) collection {
	c.e.t.Helper()
	var col collection
	c.mustDo("POST", "/api/workspaces/"+workspaceID+"/collections", map[string]string{"name": name}, &col, http.StatusCreated)
	return col
}

func (c *client) listBoards(query string) boardList {
	c.e.t.Helper()
	var l boardList
	c.mustDo("GET", "/api/workspaces/"+c.workspaceID+"/boards"+query, nil, &l, 200)
	return l
}

func names(boards []board) []string {
	out := make([]string, len(boards))
	for i, b := range boards {
		out[i] = b.Name
	}
	return out
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

// ---------------------------------------------------------------------------
// auth

func TestAuth(t *testing.T) {
	e := setup(t)
	e.newUser("Alice@Example.com")
	c := e.anonymous()

	expect(t, "me without session", c.do("GET", "/api/auth/me", nil, nil), 401)
	expect(t, "workspaces without session", c.do("GET", "/api/workspaces", nil, nil), 401)
	expect(t, "wrong password",
		c.do("POST", "/api/auth/login", map[string]string{"email": "alice@example.com", "password": "wrong password"}, nil), 401)
	expect(t, "unknown email",
		c.do("POST", "/api/auth/login", map[string]string{"email": "nobody@example.com", "password": testPassword}, nil), 401)
	body, _ := json.Marshal(map[string]string{"email": "alice@example.com", "password": testPassword})
	expect(t, "login without CSRF header",
		c.raw("POST", "/api/auth/login", bytes.NewReader(body), "application/json", nil, false), 403)

	// email is case-insensitive
	c.mustDo("POST", "/api/auth/login", map[string]string{"email": "ALICE@example.com ", "password": testPassword}, nil, 200)
	var me struct {
		User auth.User `json:"user"`
	}
	c.mustDo("GET", "/api/auth/me", nil, &me, 200)
	if me.User.Email != "alice@example.com" {
		t.Fatalf("me: %+v", me.User)
	}
	c.mustDo("POST", "/api/auth/logout", nil, nil, 204)
	expect(t, "me after logout", c.do("GET", "/api/auth/me", nil, nil), 401)
}

func TestPasswordChangeRevokesSessions(t *testing.T) {
	e := setup(t)
	c := e.newUser("alice@example.com")
	if err := auth.NewUsers(e.pool).SetPassword(context.Background(), "alice@example.com", "a new password"); err != nil {
		t.Fatal(err)
	}
	expect(t, "me after password change", c.do("GET", "/api/auth/me", nil, nil), 401)
}

func TestLoginRateLimit(t *testing.T) {
	e := setup(t)
	e.newUser("alice@example.com")
	c := e.anonymous()
	wrong := map[string]string{"email": "alice@example.com", "password": "wrong password"}
	for i := 0; i < 5; i++ {
		expect(t, "failed login", c.do("POST", "/api/auth/login", wrong, nil), 401)
	}
	right := map[string]string{"email": "alice@example.com", "password": testPassword}
	expect(t, "login after 5 failures", c.do("POST", "/api/auth/login", right, nil), 429)
}

// ---------------------------------------------------------------------------
// workspaces

func TestWorkspaces(t *testing.T) {
	e := setup(t)
	c := e.newUser("alice@example.com")

	var list struct {
		Workspaces []workspace `json:"workspaces"`
	}
	c.mustDo("GET", "/api/workspaces", nil, &list, 200)
	if list.Workspaces[0].Name != "Test's workspace" || list.Workspaces[0].Role != "owner" {
		t.Fatalf("personal workspace: %+v", list.Workspaces[0])
	}

	expect(t, "delete only workspace", c.do("DELETE", "/api/workspaces/"+c.workspaceID, nil, nil), 409)

	var work workspace
	c.mustDo("POST", "/api/workspaces", map[string]string{"name": " Kerja "}, &work, 201)
	if work.Name != "Kerja" || work.Role != "owner" {
		t.Fatalf("created workspace: %+v", work)
	}
	expect(t, "empty name", c.do("POST", "/api/workspaces", map[string]string{"name": " "}, nil), 400)

	var renamed workspace
	c.mustDo("PATCH", "/api/workspaces/"+work.ID, map[string]string{"name": "Office"}, &renamed, 200)
	if renamed.Name != "Office" {
		t.Fatalf("rename: %+v", renamed)
	}

	c.createBoard(work.ID, "Plan", nil)
	c.createCollection(work.ID, "Q4")
	var res struct {
		Error struct{ Code string } `json:"error"`
	}
	c.mustDo("DELETE", "/api/workspaces/"+work.ID, nil, &res, 409)
	if res.Error.Code != "workspace_not_empty" {
		t.Fatalf("delete non-empty workspace: %+v", res)
	}

	l := boardList{}
	c.mustDo("GET", "/api/workspaces/"+work.ID+"/boards", nil, &l, 200)
	c.mustDo("DELETE", "/api/boards/"+l.Boards[0].ID, nil, nil, 204)
	// empty now (collections don't block deletion and go with it)
	c.mustDo("DELETE", "/api/workspaces/"+work.ID, nil, nil, 204)
	expect(t, "deleted workspace boards", c.do("GET", "/api/workspaces/"+work.ID+"/boards", nil, nil), 404)
}

// A member who isn't the owner can use a workspace but not manage it.
func TestWorkspaceMemberRole(t *testing.T) {
	e := setup(t)
	alice := e.newUser("alice@example.com")
	bob := e.newUser("bob@example.com")
	var bobID string
	if err := e.pool.QueryRow(context.Background(), `SELECT id FROM users WHERE email = 'bob@example.com'`).Scan(&bobID); err != nil {
		t.Fatal(err)
	}
	// no invite flow yet: add bob directly
	if _, err := e.pool.Exec(context.Background(),
		`INSERT INTO workspace_members (workspace_id, user_id, role) VALUES ($1, $2, 'member')`,
		alice.workspaceID, bobID); err != nil {
		t.Fatal(err)
	}

	b := bob.createBoard(alice.workspaceID, "Bob's board in Alice's workspace", nil)
	if b.CreatedBy == nil || b.CreatedBy.Name != "Test" {
		t.Fatalf("createdBy: %+v", b.CreatedBy)
	}
	expect(t, "member renames workspace",
		bob.do("PATCH", "/api/workspaces/"+alice.workspaceID, map[string]string{"name": "mine"}, nil), 403)
	expect(t, "member deletes workspace", bob.do("DELETE", "/api/workspaces/"+alice.workspaceID, nil, nil), 403)

	// deleting the creator keeps the board in the workspace
	if _, err := e.pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, bobID); err != nil {
		t.Fatal(err)
	}
	var got board
	alice.mustDo("GET", "/api/boards/"+b.ID, nil, &got, 200)
	if got.CreatedBy != nil {
		t.Fatalf("createdBy should be null after the creator is deleted: %+v", got.CreatedBy)
	}
}

// ---------------------------------------------------------------------------
// boards

func TestBoardLifecycle(t *testing.T) {
	e := setup(t)
	c := e.newUser("alice@example.com")

	b := c.createBoard(c.workspaceID, "  ", nil)
	if b.Name != "Untitled" || b.Version != 1 || b.WorkspaceID != c.workspaceID ||
		string(b.Scene) != `{"elements":[],"appState":{}}` {
		t.Fatalf("new board: %+v", b)
	}
	c.createBoard(c.workspaceID, "Second", nil)

	scene := json.RawMessage(`{"elements":[{"id":"r1","type":"rectangle","text":"<b>&</b>"}],"appState":{"viewBackgroundColor":"#ffffff"}}`)
	var saved struct {
		Version int64 `json:"version"`
	}
	c.mustDo("PUT", "/api/boards/"+b.ID+"/scene", map[string]any{"version": 1, "scene": scene}, &saved, 200)
	if saved.Version != 2 {
		t.Fatalf("version after save: %d", saved.Version)
	}
	var conflict struct {
		Version int64 `json:"version"`
	}
	c.mustDo("PUT", "/api/boards/"+b.ID+"/scene", map[string]any{"version": 1, "scene": scene}, &conflict, 409)
	if conflict.Version != 2 {
		t.Fatalf("conflict should report current version, got %d", conflict.Version)
	}
	expect(t, "save without version", c.do("PUT", "/api/boards/"+b.ID+"/scene", map[string]any{"scene": scene}, nil), 400)
	expect(t, "save invalid scene",
		c.do("PUT", "/api/boards/"+b.ID+"/scene", map[string]any{"version": 2, "scene": map[string]any{"elements": "x"}}, nil), 400)

	var got board
	c.mustDo("GET", "/api/boards/"+b.ID, nil, &got, 200)
	if got.Version != 2 || !jsonEqual(t, got.Scene, scene) {
		t.Fatalf("scene not stored intact:\n got  %s\n want %s", got.Scene, scene)
	}

	var renamed board
	c.mustDo("PATCH", "/api/boards/"+b.ID, map[string]string{"name": "Plan"}, &renamed, 200)
	if renamed.Name != "Plan" {
		t.Fatalf("rename: %+v", renamed)
	}
	expect(t, "rename to empty", c.do("PATCH", "/api/boards/"+b.ID, map[string]string{"name": " "}, nil), 400)
	expect(t, "empty patch", c.do("PATCH", "/api/boards/"+b.ID, map[string]any{}, nil), 400)

	l := c.listBoards("")
	if !reflect.DeepEqual(names(l.Boards), []string{"Plan", "Second"}) || l.NextCursor != "" {
		t.Fatalf("list should be ordered by last update: %+v", l)
	}

	c.mustDo("DELETE", "/api/boards/"+b.ID, nil, nil, 204)
	expect(t, "get deleted", c.do("GET", "/api/boards/"+b.ID, nil, nil), 404)
	expect(t, "malformed id", c.do("GET", "/api/boards/not-a-uuid", nil, nil), 404)
}

func TestBoardPagination(t *testing.T) {
	e := setup(t)
	c := e.newUser("alice@example.com")
	const total = 7
	for i := 0; i < total; i++ {
		c.createBoard(c.workspaceID, fmt.Sprintf("b%d", i), nil)
	}
	// identical updated_at values must still page without gaps or duplicates
	if _, err := e.pool.Exec(context.Background(), `UPDATE boards SET updated_at = '2026-01-01T00:00:00Z'`); err != nil {
		t.Fatal(err)
	}

	seen := map[string]bool{}
	query := "?limit=3"
	pages := 0
	for {
		l := c.listBoards(query)
		pages++
		for _, b := range l.Boards {
			if seen[b.ID] {
				t.Fatalf("board %s returned twice", b.Name)
			}
			seen[b.ID] = true
		}
		if l.NextCursor == "" {
			break
		}
		query = "?limit=3&cursor=" + l.NextCursor
	}
	if len(seen) != total || pages != 3 {
		t.Fatalf("got %d boards over %d pages, want %d over 3", len(seen), pages, total)
	}
	expect(t, "bad cursor", c.do("GET", "/api/workspaces/"+c.workspaceID+"/boards?cursor=garbage", nil, nil), 400)
	expect(t, "bad limit", c.do("GET", "/api/workspaces/"+c.workspaceID+"/boards?limit=0", nil, nil), 400)
}

func TestCollections(t *testing.T) {
	e := setup(t)
	c := e.newUser("alice@example.com")

	work := c.createCollection(c.workspaceID, "Work")
	c.createCollection(c.workspaceID, "art")
	inWork := c.createBoard(c.workspaceID, "In work", &work.ID)
	loose := c.createBoard(c.workspaceID, "Loose", nil)

	var cols struct {
		Collections []collection `json:"collections"`
	}
	c.mustDo("GET", "/api/workspaces/"+c.workspaceID+"/collections", nil, &cols, 200)
	if len(cols.Collections) != 2 || cols.Collections[0].Name != "art" || cols.Collections[1].BoardCount != 1 {
		t.Fatalf("collections (by name, with counts): %+v", cols.Collections)
	}

	if l := c.listBoards("?collection=" + work.ID); !reflect.DeepEqual(names(l.Boards), []string{"In work"}) {
		t.Fatalf("filter by collection: %v", names(l.Boards))
	}
	if l := c.listBoards("?collection=unsorted"); !reflect.DeepEqual(names(l.Boards), []string{"Loose"}) {
		t.Fatalf("filter unsorted: %v", names(l.Boards))
	}

	// moving doesn't count as a modification
	before := c.listBoards("").Boards
	var moved board
	c.mustDo("PATCH", "/api/boards/"+loose.ID, map[string]any{"collectionId": work.ID}, &moved, 200)
	if moved.CollectionID == nil || *moved.CollectionID != work.ID {
		t.Fatalf("move: %+v", moved)
	}
	for _, b := range before {
		if b.ID == loose.ID && !b.UpdatedAt.Equal(moved.UpdatedAt) {
			t.Fatalf("moving changed updated_at: %v -> %v", b.UpdatedAt, moved.UpdatedAt)
		}
	}
	c.mustDo("PATCH", "/api/boards/"+loose.ID, map[string]any{"collectionId": nil}, &moved, 200)
	if moved.CollectionID != nil {
		t.Fatalf("move to unsorted: %+v", moved)
	}

	var renamed collection
	c.mustDo("PATCH", "/api/collections/"+work.ID, map[string]string{"name": "Office"}, &renamed, 200)
	if renamed.Name != "Office" {
		t.Fatalf("rename collection: %+v", renamed)
	}

	// deleting a collection keeps its boards, now unsorted
	c.mustDo("DELETE", "/api/collections/"+work.ID, nil, nil, 204)
	var got board
	c.mustDo("GET", "/api/boards/"+inWork.ID, nil, &got, 200)
	if got.CollectionID != nil {
		t.Fatalf("board should be unsorted after its collection is deleted: %+v", got)
	}

	// a collection from another workspace can't be used
	var other workspace
	c.mustDo("POST", "/api/workspaces", map[string]string{"name": "Other"}, &other, 201)
	foreign := c.createCollection(other.ID, "Foreign")
	expect(t, "move into another workspace's collection",
		c.do("PATCH", "/api/boards/"+inWork.ID, map[string]any{"collectionId": foreign.ID}, nil), 404)
	expect(t, "create in another workspace's collection",
		c.do("POST", "/api/workspaces/"+c.workspaceID+"/boards", map[string]any{"collectionId": foreign.ID}, nil), 404)
}

func TestFiles(t *testing.T) {
	e := setup(t)
	c := e.newUser("alice@example.com")
	b := c.createBoard(c.workspaceID, "Images", nil)
	path := "/api/boards/" + b.ID + "/files/"

	c.mustDo("PUT", path+"img1", map[string]any{"mimeType": "image/png", "dataURL": "data:image/png;base64,AAAA", "created": 1700000000000}, nil, 204)
	c.mustDo("PUT", path+"img1", map[string]any{"mimeType": "image/png", "dataURL": "data:image/png;base64,BBBB", "created": 1700000000000}, nil, 204)

	type filesResp struct {
		Files []struct {
			ID      string `json:"id"`
			DataURL string `json:"dataURL"`
			Version int    `json:"version"`
		} `json:"files"`
		ErroredIds []string `json:"erroredIds"`
	}
	var r filesResp
	c.mustDo("GET", "/api/boards/"+b.ID+"/files?ids=img1,missing", nil, &r, 200)
	if len(r.Files) != 1 || r.Files[0].DataURL != "data:image/png;base64,AAAA" {
		t.Fatalf("same-version re-upload must not overwrite: %+v", r)
	}
	if len(r.ErroredIds) != 1 || r.ErroredIds[0] != "missing" {
		t.Fatalf("erroredIds: %+v", r.ErroredIds)
	}
	c.mustDo("PUT", path+"img1", map[string]any{"mimeType": "image/png", "dataURL": "data:image/png;base64,CCCC", "created": 1700000000000, "version": 2}, nil, 204)
	r = filesResp{}
	c.mustDo("GET", "/api/boards/"+b.ID+"/files?ids=img1", nil, &r, 200)
	if r.Files[0].DataURL != "data:image/png;base64,CCCC" || r.Files[0].Version != 2 {
		t.Fatalf("newer version should replace: %+v", r.Files)
	}

	expect(t, "bad mime", c.do("PUT", path+"img2", map[string]any{"mimeType": "text/html", "dataURL": "data:text/html,<script>", "created": 1}, nil), 400)
	expect(t, "too large", c.do("PUT", path+"img3", map[string]any{
		"mimeType": "image/png", "dataURL": "data:image/png;base64," + strings.Repeat("A", 6<<20), "created": 1,
	}, nil), 413)

	c.mustDo("DELETE", "/api/boards/"+b.ID, nil, nil, 204)
	var count int
	if err := e.pool.QueryRow(context.Background(), `SELECT count(*) FROM board_files WHERE board_id = $1`, b.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("files not deleted with board: %d left", count)
	}
}

func TestThumbnails(t *testing.T) {
	e := setup(t)
	c := e.newUser("alice@example.com")
	b := c.createBoard(c.workspaceID, "Pic", nil)
	path := "/api/boards/" + b.ID + "/thumbnail"

	expect(t, "no thumbnail yet", c.raw("GET", path, nil, "", nil, true), 404)

	var put struct {
		ThumbnailUpdatedAt time.Time `json:"thumbnailUpdatedAt"`
	}
	expect(t, "put png", c.raw("PUT", path, bytes.NewReader(pngBytes), "image/png", &put, true), 200)
	if put.ThumbnailUpdatedAt.IsZero() {
		t.Fatal("put should return thumbnailUpdatedAt")
	}

	resp := c.rawResponse("GET", path+"?v=1", nil, "", true)
	data, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !bytes.Equal(data, pngBytes) {
		t.Fatalf("get thumbnail: %d %v", resp.StatusCode, data)
	}
	for header, want := range map[string]string{
		"Content-Type":            "image/png",
		"X-Content-Type-Options":  "nosniff",
		"Content-Security-Policy": "default-src 'none'",
		"Cache-Control":           "private, max-age=31536000, immutable",
	} {
		if got := resp.Header.Get(header); got != want {
			t.Errorf("%s: got %q, want %q", header, got, want)
		}
	}

	var got board
	c.mustDo("GET", "/api/boards/"+b.ID, nil, &got, 200)
	if got.Thumbnail == nil {
		t.Fatal("board should report thumbnailUpdatedAt")
	}

	expect(t, "replace with jpeg", c.raw("PUT", path, bytes.NewReader(jpegBytes), "image/jpeg", nil, true), 200)
	expect(t, "html as png", c.raw("PUT", path, strings.NewReader("<html>"), "image/png", nil, true), 400)
	expect(t, "svg", c.raw("PUT", path, strings.NewReader("<svg/>"), "image/svg+xml", nil, true), 400)
	big := append(append([]byte{}, jpegBytes...), make([]byte, 600<<10)...)
	expect(t, "too large", c.raw("PUT", path, bytes.NewReader(big), "image/jpeg", nil, true), 413)
	expect(t, "put without CSRF header", c.raw("PUT", path, bytes.NewReader(pngBytes), "image/png", nil, false), 403)

	c.mustDo("DELETE", path, nil, nil, 204)
	expect(t, "deleted thumbnail", c.raw("GET", path, nil, "", nil, true), 404)

	c.raw("PUT", path, bytes.NewReader(pngBytes), "image/png", nil, true)
	c.mustDo("DELETE", "/api/boards/"+b.ID, nil, nil, 204)
	var count int
	if err := e.pool.QueryRow(context.Background(), `SELECT count(*) FROM board_thumbnails`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("thumbnail not deleted with board")
	}
}

// Everything in another user's workspace must look exactly like a missing
// resource, on every route.
func TestIsolation(t *testing.T) {
	e := setup(t)
	alice := e.newUser("alice@example.com")
	bob := e.newUser("bob@example.com")

	col := alice.createCollection(alice.workspaceID, "Private")
	b := alice.createBoard(alice.workspaceID, "Private", &col.ID)
	alice.mustDo("PUT", "/api/boards/"+b.ID+"/files/img1",
		map[string]any{"mimeType": "image/png", "dataURL": "data:image/png;base64,AAAA", "created": 1}, nil, 204)
	alice.raw("PUT", "/api/boards/"+b.ID+"/thumbnail", bytes.NewReader(pngBytes), "image/png", nil, true)

	ws := "/api/workspaces/" + alice.workspaceID
	routes := []struct {
		method, path string
		body         any
	}{
		{"PATCH", ws, map[string]string{"name": "pwned"}},
		{"DELETE", ws, nil},
		{"GET", ws + "/boards", nil},
		{"POST", ws + "/boards", map[string]any{"name": "x"}},
		{"GET", ws + "/collections", nil},
		{"POST", ws + "/collections", map[string]string{"name": "x"}},
		{"PATCH", "/api/collections/" + col.ID, map[string]string{"name": "pwned"}},
		{"DELETE", "/api/collections/" + col.ID, nil},
		{"GET", "/api/boards/" + b.ID, nil},
		{"PATCH", "/api/boards/" + b.ID, map[string]string{"name": "pwned"}},
		{"PUT", "/api/boards/" + b.ID + "/scene", map[string]any{"version": 1, "scene": map[string]any{"elements": []any{}}}},
		{"GET", "/api/boards/" + b.ID + "/files?ids=img1", nil},
		{"PUT", "/api/boards/" + b.ID + "/files/img2", map[string]any{"mimeType": "image/png", "dataURL": "data:image/png;base64,AAAA", "created": 1}},
		{"GET", "/api/boards/" + b.ID + "/thumbnail", nil},
		{"DELETE", "/api/boards/" + b.ID + "/thumbnail", nil},
		{"DELETE", "/api/boards/" + b.ID, nil},
	}
	for _, r := range routes {
		expect(t, fmt.Sprintf("bob %s %s", r.method, r.path), bob.do(r.method, r.path, r.body, nil), 404)
	}
	expect(t, "bob puts alice's thumbnail",
		bob.raw("PUT", "/api/boards/"+b.ID+"/thumbnail", bytes.NewReader(pngBytes), "image/png", nil, true), 404)
	// bob can't move his own board into alice's collection
	bobBoard := bob.createBoard(bob.workspaceID, "Bob's", nil)
	expect(t, "bob moves into alice's collection",
		bob.do("PATCH", "/api/boards/"+bobBoard.ID, map[string]any{"collectionId": col.ID}, nil), 404)

	var list struct {
		Workspaces []workspace `json:"workspaces"`
	}
	bob.mustDo("GET", "/api/workspaces", nil, &list, 200)
	if len(list.Workspaces) != 1 || list.Workspaces[0].ID != bob.workspaceID {
		t.Fatalf("bob sees other workspaces: %+v", list.Workspaces)
	}

	var got board
	alice.mustDo("GET", "/api/boards/"+b.ID, nil, &got, 200)
	if got.Name != "Private" || got.Version != 1 || got.CollectionID == nil {
		t.Fatalf("board was modified by another user: %+v", got)
	}
}
