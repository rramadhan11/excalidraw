package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/rramadhan11/excalidraw/excalidraw-server/internal/auth"
)

// bearer adds an Authorization header to every request of an MCP client.
type bearer struct {
	token string
	next  http.RoundTripper
}

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return b.next.RoundTrip(r)
}

// token issues an API token for an existing user.
func (e *env) token(email string, expiresAt *time.Time) string {
	e.t.Helper()
	_, raw, err := auth.NewTokens(e.pool).Create(context.Background(), email, "test client", expiresAt)
	if err != nil {
		e.t.Fatal(err)
	}
	return raw
}

// mcpSession connects a real MCP client to /api/mcp with the token.
func (e *env) mcpSession(token string) *mcp.ClientSession {
	e.t.Helper()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	transport := &mcp.StreamableClientTransport{
		Endpoint:   e.srv.URL + "/api/mcp",
		HTTPClient: &http.Client{Transport: bearer{token: token, next: http.DefaultTransport}},
		MaxRetries: -1,
	}
	cs, err := client.Connect(context.Background(), transport, nil)
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() { cs.Close() })
	return cs
}

// call runs a tool and decodes its structured result into out. It returns the
// error text when the tool reports an error.
func call(t *testing.T, cs *mcp.ClientSession, name string, args any, out any) string {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if res.IsError {
		var msgs []string
		for _, c := range res.Content {
			if text, ok := c.(*mcp.TextContent); ok {
				msgs = append(msgs, text.Text)
			}
		}
		return strings.Join(msgs, " ")
	}
	if out != nil {
		b, _ := json.Marshal(res.StructuredContent)
		if err := json.Unmarshal(b, out); err != nil {
			t.Fatalf("%s: decode %s: %v", name, b, err)
		}
	}
	return ""
}

func mustCall(t *testing.T, cs *mcp.ClientSession, name string, args any, out any) {
	t.Helper()
	if msg := call(t, cs, name, args, out); msg != "" {
		t.Fatalf("%s failed: %s", name, msg)
	}
}

type mcpBoard struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	WorkspaceID string `json:"workspaceId"`
	URL         string `json:"url"`
	Match       string `json:"match"`
}

type mcpEdit struct {
	Version    int64    `json:"version"`
	CreatedIDs []string `json:"createdIds"`
	DeletedIDs []string `json:"deletedIds"`
}

type mcpSummary struct {
	Version int64 `json:"version"`
	Summary struct {
		ElementCount int `json:"elementCount"`
		Elements     []struct {
			ID    string  `json:"id"`
			Type  string  `json:"type"`
			X     float64 `json:"x"`
			Y     float64 `json:"y"`
			Label string  `json:"label"`
			Text  string  `json:"text"`
			From  string  `json:"from"`
			To    string  `json:"to"`
		} `json:"elements"`
	} `json:"summary"`
}

func mustURL(raw string) *url.URL {
	u, err := url.Parse(raw)
	if err != nil {
		panic(err)
	}
	return u
}

func mcpPost(e *env, header http.Header) int {
	e.t.Helper()
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`
	req, _ := http.NewRequest("POST", e.srv.URL+"/api/mcp", strings.NewReader(body))
	req.Header = header
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func TestMCPAuthentication(t *testing.T) {
	e := setup(t)
	alice := e.newUser("alice@example.com")
	valid := e.token("alice@example.com", nil)

	expect(t, "no token", mcpPost(e, http.Header{}), 401)
	expect(t, "malformed token", mcpPost(e, http.Header{"Authorization": {"Bearer nope"}}), 401)
	expect(t, "unknown token", mcpPost(e, http.Header{"Authorization": {"Bearer " + auth.APITokenPrefix + strings.Repeat("A", 43)}}), 401)
	expect(t, "session token as bearer", mcpPost(e, http.Header{"Authorization": {"Basic abc"}}), 401)

	// a logged-in browser's cookie is not enough
	var cookies []string
	for _, c := range alice.http.Jar.Cookies(mustURL(e.srv.URL)) {
		cookies = append(cookies, c.String())
	}
	expect(t, "cookie only", mcpPost(e, http.Header{"Cookie": cookies}), 401)

	// no CSRF header is needed with a token...
	expect(t, "valid token", mcpPost(e, http.Header{"Authorization": {"Bearer " + valid}}), 200)
	// ...while the cookie API still demands it
	expect(t, "cookie API without CSRF header",
		alice.raw("POST", "/api/workspaces", strings.NewReader(`{"name":"x"}`), "application/json", nil, false), 403)

	past := time.Now().Add(-time.Minute)
	expired := e.token("alice@example.com", nil)
	if _, err := e.pool.Exec(context.Background(),
		`UPDATE api_tokens SET expires_at = $1 WHERE token_hash = sha256($2::bytea)`, past, expired); err != nil {
		t.Fatal(err)
	}
	expect(t, "expired token", mcpPost(e, http.Header{"Authorization": {"Bearer " + expired}}), 401)

	tokens := auth.NewTokens(e.pool)
	list, err := tokens.List(context.Background(), "alice@example.com")
	if err != nil || len(list) != 2 {
		t.Fatalf("list = %v, %v", list, err)
	}
	if err := tokens.Revoke(context.Background(), list[0].ID); err != nil {
		t.Fatal(err)
	}
	expect(t, "revoked token", mcpPost(e, http.Header{"Authorization": {"Bearer " + valid}}), 401)
	if err := tokens.Revoke(context.Background(), "not-a-uuid"); err == nil {
		t.Error("revoking an unknown token should fail")
	}
	if _, _, err := tokens.Create(context.Background(), "nobody@example.com", "x", nil); err == nil {
		t.Error("creating a token for an unknown user should fail")
	}
}

func TestMCPTools(t *testing.T) {
	e := setup(t)
	e.newUser("alice@example.com")
	cs := e.mcpSession(e.token("alice@example.com", nil))

	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
		if tool.Description == "" || tool.InputSchema == nil {
			t.Errorf("%s lacks a description or input schema", tool.Name)
		}
	}
	slices.Sort(names)
	want := []string{"add_elements", "create_board", "create_collection", "delete_elements", "get_board",
		"list_boards", "list_collections", "list_workspaces", "move_board", "rename_board",
		"rename_collection", "search_boards", "update_elements"}
	if !slices.Equal(names, want) {
		t.Errorf("tools = %v, want %v", names, want)
	}
	for _, tool := range res.Tools {
		if strings.HasPrefix(tool.Name, "delete") && (tool.Annotations == nil || tool.Annotations.DestructiveHint == nil || !*tool.Annotations.DestructiveHint) {
			t.Errorf("%s should be marked destructive", tool.Name)
		}
		if (strings.HasPrefix(tool.Name, "list") || strings.HasPrefix(tool.Name, "get") || strings.HasPrefix(tool.Name, "search")) &&
			(tool.Annotations == nil || !tool.Annotations.ReadOnlyHint) {
			t.Errorf("%s should be read-only", tool.Name)
		}
	}
}

func TestMCPDrawingFlow(t *testing.T) {
	e := setup(t)
	alice := e.newUser("alice@example.com")
	cs := e.mcpSession(e.token("alice@example.com", nil))

	var ws struct {
		Workspaces []struct{ ID, URL string } `json:"workspaces"`
	}
	mustCall(t, cs, "list_workspaces", map[string]any{}, &ws)
	if len(ws.Workspaces) != 1 || ws.Workspaces[0].ID != alice.workspaceID {
		t.Fatalf("workspaces = %+v", ws)
	}

	var col struct {
		Collection struct{ ID string } `json:"collection"`
	}
	mustCall(t, cs, "create_collection", map[string]any{"workspace_id": alice.workspaceID, "name": "Diagrams"}, &col)
	var board mcpBoard
	mustCall(t, cs, "create_board", map[string]any{
		"workspace_id": alice.workspaceID, "name": "Architecture", "collection_id": col.Collection.ID,
	}, &board)
	if board.URL != "/board/"+board.ID {
		t.Errorf("url = %q", board.URL)
	}
	// the browser had rendered a thumbnail; drawing must invalidate it
	expect(t, "put thumbnail",
		alice.raw("PUT", "/api/boards/"+board.ID+"/thumbnail", bytes.NewReader(pngBytes), "image/png", nil, true), 200)

	var added mcpEdit
	mustCall(t, cs, "add_elements", map[string]any{
		"board_id": board.ID,
		"elements": []map[string]any{
			{"id": "api", "type": "rectangle", "x": 0, "y": 0, "label": map[string]any{"text": "API"}},
			{"id": "db", "type": "ellipse", "x": 300, "y": 0, "label": map[string]any{"text": "DB"}},
			{"id": "q", "type": "arrow", "start": map[string]any{"id": "api"}, "end": map[string]any{"id": "db"}},
		},
	}, &added)
	if !slices.Equal(added.CreatedIDs, []string{"api", "db", "q"}) || added.Version != 2 {
		t.Errorf("add = %+v", added)
	}
	expect(t, "stale thumbnail removed",
		alice.raw("GET", "/api/boards/"+board.ID+"/thumbnail", nil, "", nil, false), 404)

	// the browser API sees the same scene
	var stored struct {
		Version int64 `json:"version"`
		Scene   struct {
			Elements []map[string]any `json:"elements"`
		} `json:"scene"`
	}
	alice.mustDo("GET", "/api/boards/"+board.ID, nil, &stored, 200)
	if stored.Version != 2 || len(stored.Scene.Elements) != 5 { // 3 + 2 labels
		t.Errorf("stored version %d with %d elements", stored.Version, len(stored.Scene.Elements))
	}

	var got mcpSummary
	mustCall(t, cs, "get_board", map[string]any{"board_id": board.ID}, &got)
	if got.Summary.ElementCount != 3 {
		t.Fatalf("summary = %+v", got.Summary)
	}
	if arrow := got.Summary.Elements[2]; arrow.From != "api" || arrow.To != "db" {
		t.Errorf("arrow = %+v", arrow)
	}
	if first := got.Summary.Elements[0]; first.Label != "API" {
		t.Errorf("first = %+v", first)
	}

	if msg := call(t, cs, "add_elements", map[string]any{
		"board_id": board.ID,
		"elements": []map[string]any{{"type": "arrow", "start": map[string]any{"id": "api"}, "end": map[string]any{"id": "missing"}}},
	}, nil); !strings.Contains(msg, `"missing" does not exist`) {
		t.Errorf("invalid input error = %q", msg)
	}

	var edit mcpEdit
	mustCall(t, cs, "update_elements", map[string]any{
		"board_id": board.ID,
		"updates":  []map[string]any{{"id": "db", "y": 200, "text": "Postgres"}},
	}, &edit)
	mustCall(t, cs, "get_board", map[string]any{"board_id": board.ID}, &got)
	if db := got.Summary.Elements[1]; db.Y != 200 || db.Label != "Postgres" {
		t.Errorf("db after update = %+v", db)
	}

	mustCall(t, cs, "delete_elements", map[string]any{"board_id": board.ID, "ids": []string{"db"}}, &edit)
	if len(edit.DeletedIDs) != 2 { // db and its label
		t.Errorf("deleted = %v", edit.DeletedIDs)
	}
	mustCall(t, cs, "get_board", map[string]any{"board_id": board.ID}, &got)
	if got.Summary.ElementCount != 2 || got.Summary.Elements[1].To != "" {
		t.Errorf("after delete = %+v", got.Summary)
	}

	var full struct {
		Elements []map[string]any `json:"elements"`
	}
	mustCall(t, cs, "get_board", map[string]any{"board_id": board.ID, "detail": "full"}, &full)
	if len(full.Elements) != 3 { // api, its label, the detached arrow
		t.Errorf("full = %d elements", len(full.Elements))
	}

	var renamed mcpBoard
	mustCall(t, cs, "rename_board", map[string]any{"board_id": board.ID, "name": "System"}, &renamed)
	mustCall(t, cs, "move_board", map[string]any{"board_id": board.ID}, &renamed)
	if renamed.Name != "System" {
		t.Errorf("renamed = %+v", renamed)
	}
	var unsorted struct{ Boards []mcpBoard }
	mustCall(t, cs, "list_boards", map[string]any{"workspace_id": alice.workspaceID, "collection": "unsorted"}, &unsorted)
	if len(unsorted.Boards) != 1 || unsorted.Boards[0].ID != board.ID {
		t.Errorf("unsorted = %+v", unsorted)
	}
}

func TestMCPSearch(t *testing.T) {
	e := setup(t)
	alice := e.newUser("alice@example.com")
	bob := e.newUser("bob@example.com")
	cs := e.mcpSession(e.token("alice@example.com", nil))

	named := alice.createBoard(alice.workspaceID, "Payment flow", nil)
	content := alice.createBoard(alice.workspaceID, "Untitled", nil)
	alice.createBoard(alice.workspaceID, "Unrelated", nil)
	bob.createBoard(bob.workspaceID, "Payment secrets", nil)
	// text matches; JSON keys and deleted elements must not
	alice.mustDo("PUT", "/api/boards/"+content.ID+"/scene", map[string]any{
		"version": 1,
		"scene": map[string]any{"elements": []map[string]any{
			{"id": "t1", "type": "text", "text": "Retry the PAYMENT webhook"},
			{"id": "t2", "type": "text", "text": "rectangle 100% done", "isDeleted": true},
		}},
	}, nil, 200)

	var res struct{ Boards []mcpBoard }
	mustCall(t, cs, "search_boards", map[string]any{"query": "payment"}, &res)
	if len(res.Boards) != 2 || res.Boards[0].ID != named.ID || res.Boards[1].ID != content.ID {
		t.Fatalf("search = %+v", res.Boards)
	}
	if res.Boards[1].Match != "Retry the PAYMENT webhook" {
		t.Errorf("match = %q", res.Boards[1].Match)
	}
	for _, q := range []string{"elements", "type", "100%", "rectangle"} {
		mustCall(t, cs, "search_boards", map[string]any{"query": q}, &res)
		if len(res.Boards) != 0 {
			t.Errorf("%q matched %+v", q, res.Boards)
		}
	}
	if msg := call(t, cs, "search_boards", map[string]any{"query": "payment", "workspace_id": bob.workspaceID}, nil); msg == "" {
		t.Error("searching someone else's workspace should fail")
	}
	if msg := call(t, cs, "search_boards", map[string]any{"query": " "}, nil); msg == "" {
		t.Error("an empty query should fail")
	}
}

func TestMCPIsolation(t *testing.T) {
	e := setup(t)
	alice := e.newUser("alice@example.com")
	e.newUser("bob@example.com")
	private := alice.createBoard(alice.workspaceID, "Private", nil)
	col := alice.createCollection(alice.workspaceID, "Secret")
	bob := e.mcpSession(e.token("bob@example.com", nil))

	attempts := map[string]map[string]any{
		"get_board":         {"board_id": private.ID},
		"add_elements":      {"board_id": private.ID, "elements": []map[string]any{{"type": "rectangle", "x": 0, "y": 0}}},
		"update_elements":   {"board_id": private.ID, "updates": []map[string]any{{"id": "x", "x": 1}}},
		"delete_elements":   {"board_id": private.ID, "ids": []string{"x"}},
		"rename_board":      {"board_id": private.ID, "name": "pwned"},
		"move_board":        {"board_id": private.ID},
		"list_boards":       {"workspace_id": alice.workspaceID},
		"list_collections":  {"workspace_id": alice.workspaceID},
		"create_board":      {"workspace_id": alice.workspaceID},
		"create_collection": {"workspace_id": alice.workspaceID, "name": "x"},
		"rename_collection": {"collection_id": col.ID, "name": "x"},
	}
	for tool, args := range attempts {
		msg := call(t, bob, tool, args, nil)
		if !strings.Contains(msg, "not found") {
			t.Errorf("%s: got %q, want a not-found error", tool, msg)
		}
	}
	var res struct{ Boards []mcpBoard }
	mustCall(t, bob, "search_boards", map[string]any{"query": "Private"}, &res)
	if len(res.Boards) != 0 {
		t.Errorf("bob found %+v", res.Boards)
	}
	var stored board
	alice.mustDo("GET", "/api/boards/"+private.ID, nil, &stored, 200)
	if stored.Name != "Private" {
		t.Errorf("board was changed: %+v", stored)
	}
}

// Concurrent edits both land: a writer that loses the version race re-applies
// its edit to the newer scene. With two writers each can lose at most once,
// which is within the retry budget.
func TestMCPConcurrentEditsAreRetried(t *testing.T) {
	e := setup(t)
	alice := e.newUser("alice@example.com")
	board := alice.createBoard(alice.workspaceID, "Busy", nil)
	sessions := []*mcp.ClientSession{
		e.mcpSession(e.token("alice@example.com", nil)),
		e.mcpSession(e.token("alice@example.com", nil)),
	}
	for round := 0; round < 5; round++ {
		var wg sync.WaitGroup
		errs := make([]string, len(sessions))
		for i, cs := range sessions {
			wg.Add(1)
			go func() {
				defer wg.Done()
				errs[i] = call(t, cs, "add_elements", map[string]any{
					"board_id": board.ID,
					"elements": []map[string]any{{"type": "rectangle", "x": i * 200, "y": round * 200}},
				}, nil)
			}()
		}
		wg.Wait()
		for i, msg := range errs {
			if msg != "" {
				t.Fatalf("round %d writer %d: %s", round, i, msg)
			}
		}
	}
	var got mcpSummary
	mustCall(t, sessions[0], "get_board", map[string]any{"board_id": board.ID}, &got)
	if got.Summary.ElementCount != 10 || got.Version != 11 {
		t.Errorf("%d elements at version %d, want 10 at 11", got.Summary.ElementCount, got.Version)
	}
}
