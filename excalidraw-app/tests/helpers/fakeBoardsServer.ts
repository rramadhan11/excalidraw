/**
 * In-memory stand-in for excalidraw-server, served through a `fetch` stub.
 * Mirrors the API contract in excalidraw-server/README.md closely enough for
 * the app's boards-mode tests.
 */

type FakeWorkspace = { id: string; name: string; role: "owner" | "member" };
type FakeCollection = { id: string; workspaceId: string; name: string };
export type FakeBoard = {
  id: string;
  name: string;
  workspaceId: string;
  collectionId: string | null;
  version: number;
  scene: { elements: any[]; appState: Record<string, unknown> };
  createdAt: string;
  updatedAt: string;
  thumbnailUpdatedAt: string | null;
};

export const WORKSPACE_ID = "aaaaaaaa-0000-4000-8000-000000000001";
export const BOARD_ID = "bbbbbbbb-0000-4000-8000-000000000001";

let idCounter = 0;
const newId = (prefix: string) =>
  `${prefix}-0000-4000-8000-${String(++idCounter).padStart(12, "0")}`;

export class FakeBoardsServer {
  loggedIn = true;
  user = { id: "u1", email: "me@example.com", name: "Me" };
  workspaces = new Map<string, FakeWorkspace>();
  collections = new Map<string, FakeCollection>();
  boards = new Map<string, FakeBoard>();

  sceneSaves: { boardId: string; version: number; scene: any }[] = [];
  boardPatches: { boardId: string; patch: any }[] = [];
  thumbnailPuts: { boardId: string; type: string }[] = [];
  thumbnailDeletes: string[] = [];
  conflictOnNextSave = false;

  constructor() {
    this.workspaces.set(WORKSPACE_ID, {
      id: WORKSPACE_ID,
      name: "Me's workspace",
      role: "owner",
    });
  }

  addWorkspace(name: string, id = newId("aaaaaaaa")) {
    this.workspaces.set(id, { id, name, role: "owner" });
    return id;
  }

  addCollection(
    name: string,
    workspaceId = WORKSPACE_ID,
    id = newId("cccccccc"),
  ) {
    this.collections.set(id, { id, workspaceId, name });
    return id;
  }

  /** later-added boards count as more recently updated */
  addBoard(board: Partial<FakeBoard> & { id?: string } = {}) {
    const id = board.id ?? newId("bbbbbbbb");
    const updatedAt = new Date(
      Date.UTC(2026, 0, 1) + this.boards.size * 60_000,
    ).toISOString();
    this.boards.set(id, {
      id,
      name: "Untitled",
      workspaceId: WORKSPACE_ID,
      collectionId: null,
      version: 1,
      scene: { elements: [], appState: {} },
      createdAt: updatedAt,
      updatedAt,
      thumbnailUpdatedAt: null,
      ...board,
    });
    return id;
  }

  private summary(board: FakeBoard) {
    const { scene, version, ...summary } = board;
    return {
      ...summary,
      createdBy: { id: this.user.id, name: this.user.name },
    };
  }

  private boardCount(filter: (b: FakeBoard) => boolean) {
    return [...this.boards.values()].filter(filter).length;
  }

  fetch = async (input: RequestInfo | URL, init: RequestInit = {}) => {
    const url = new URL(String(input), "http://localhost");
    const method = init.method ?? "GET";
    const headers = (init.headers ?? {}) as Record<string, string>;
    const body =
      typeof init.body === "string" ? JSON.parse(init.body) : init.body;
    const json = (status: number, payload?: unknown) =>
      new Response(payload === undefined ? null : JSON.stringify(payload), {
        status,
        headers: { "Content-Type": "application/json" },
      });
    const notFound = (message = "not found") =>
      json(404, { error: { code: "not_found", message } });

    if (!url.pathname.startsWith("/api/")) {
      return json(404);
    }
    if (method !== "GET" && headers["X-Requested-With"] !== "excalidraw") {
      return json(403, { error: { code: "csrf", message: "csrf" } });
    }
    const path = url.pathname.slice("/api".length);
    let m: RegExpMatchArray | null;

    if (path === "/auth/login" && method === "POST") {
      this.loggedIn = true;
      return json(200, { user: { ...this.user, email: body.email } });
    }
    if (path === "/auth/logout") {
      this.loggedIn = false;
      return json(204);
    }
    if (!this.loggedIn) {
      return json(401, {
        error: { code: "unauthorized", message: "login required" },
      });
    }
    if (path === "/auth/me") {
      return json(200, { user: this.user });
    }

    // workspaces
    if (path === "/workspaces" && method === "GET") {
      return json(200, {
        workspaces: [...this.workspaces.values()].map((w) => ({
          ...w,
          boardCount: this.boardCount((b) => b.workspaceId === w.id),
        })),
      });
    }
    if (path === "/workspaces" && method === "POST") {
      const id = this.addWorkspace(body.name);
      return json(201, { ...this.workspaces.get(id), boardCount: 0 });
    }
    if ((m = path.match(/^\/workspaces\/([^/]+)$/))) {
      const ws = this.workspaces.get(m[1]);
      if (!ws) {
        return notFound();
      }
      if (method === "PATCH") {
        ws.name = body.name;
        return json(200, { ...ws, boardCount: 0 });
      }
      if (method === "DELETE") {
        if (this.boardCount((b) => b.workspaceId === ws.id)) {
          return json(409, {
            error: {
              code: "workspace_not_empty",
              message: "this workspace still has boards; delete them first",
            },
          });
        }
        this.workspaces.delete(ws.id);
        return json(204);
      }
    }

    // collections
    if ((m = path.match(/^\/workspaces\/([^/]+)\/collections$/))) {
      if (!this.workspaces.has(m[1])) {
        return notFound();
      }
      if (method === "POST") {
        const id = this.addCollection(body.name, m[1]);
        return json(201, { ...this.collections.get(id), boardCount: 0 });
      }
      const workspaceId = m[1];
      return json(200, {
        collections: [...this.collections.values()]
          .filter((c) => c.workspaceId === workspaceId)
          .sort((a, b) => a.name.localeCompare(b.name))
          .map((c) => ({
            ...c,
            boardCount: this.boardCount((b) => b.collectionId === c.id),
          })),
      });
    }
    if ((m = path.match(/^\/collections\/([^/]+)$/))) {
      const col = this.collections.get(m[1]);
      if (!col) {
        return notFound();
      }
      if (method === "PATCH") {
        col.name = body.name;
        return json(200, { ...col, boardCount: 0 });
      }
      if (method === "DELETE") {
        this.collections.delete(col.id);
        for (const board of this.boards.values()) {
          if (board.collectionId === col.id) {
            board.collectionId = null;
          }
        }
        return json(204);
      }
    }

    // boards in a workspace
    if ((m = path.match(/^\/workspaces\/([^/]+)\/boards$/))) {
      const workspaceId = m[1];
      if (!this.workspaces.has(workspaceId)) {
        return notFound();
      }
      if (method === "POST") {
        const id = this.addBoard({
          workspaceId,
          name: body.name || "Untitled",
          collectionId: body.collectionId ?? null,
        });
        const board = this.boards.get(id)!;
        return json(201, {
          ...this.summary(board),
          version: board.version,
          scene: board.scene,
        });
      }
      const collection = url.searchParams.get("collection");
      const limit = Number(url.searchParams.get("limit") || 60);
      const offset = Number(url.searchParams.get("cursor") || 0);
      const list = [...this.boards.values()]
        .filter((b) => b.workspaceId === workspaceId)
        .filter((b) =>
          collection === "unsorted"
            ? b.collectionId === null
            : collection
            ? b.collectionId === collection
            : true,
        )
        .sort((a, b) => b.updatedAt.localeCompare(a.updatedAt));
      const page = list.slice(offset, offset + limit);
      return json(200, {
        boards: page.map((b) => this.summary(b)),
        nextCursor: offset + limit < list.length ? String(offset + limit) : "",
      });
    }

    // a single board
    m = path.match(/^\/boards\/([^/]+)(\/scene|\/files|\/thumbnail)?$/);
    const board = m && this.boards.get(m[1]);
    if (!m || !board) {
      return notFound("board not found");
    }
    const sub = m[2];
    if (!sub && method === "GET") {
      return json(200, {
        ...this.summary(board),
        version: board.version,
        scene: board.scene,
      });
    }
    if (!sub && method === "PATCH") {
      this.boardPatches.push({ boardId: board.id, patch: body });
      if (body.name !== undefined) {
        board.name = body.name;
      }
      if (body.collectionId !== undefined) {
        board.collectionId = body.collectionId;
      }
      return json(200, this.summary(board));
    }
    if (!sub && method === "DELETE") {
      this.boards.delete(board.id);
      return json(204);
    }
    if (sub === "/scene" && method === "PUT") {
      if (this.conflictOnNextSave || body.version !== board.version) {
        this.conflictOnNextSave = false;
        return json(409, {
          error: { code: "conflict", message: "board was modified elsewhere" },
          version: board.version + 1,
        });
      }
      board.version += 1;
      board.scene = body.scene;
      board.updatedAt = new Date().toISOString();
      this.sceneSaves.push({
        boardId: board.id,
        version: body.version,
        scene: body.scene,
      });
      return json(200, { version: board.version, updatedAt: board.updatedAt });
    }
    if (sub === "/files" && method === "GET") {
      return json(200, { files: [], erroredIds: [] });
    }
    if (sub === "/thumbnail" && method === "PUT") {
      this.thumbnailPuts.push({
        boardId: board.id,
        type: headers["Content-Type"],
      });
      board.thumbnailUpdatedAt = new Date().toISOString();
      return json(200, { thumbnailUpdatedAt: board.thumbnailUpdatedAt });
    }
    if (sub === "/thumbnail" && method === "DELETE") {
      this.thumbnailDeletes.push(board.id);
      board.thumbnailUpdatedAt = null;
      return json(204);
    }
    return json(404, { error: { code: "not_found", message: "no route" } });
  };
}
