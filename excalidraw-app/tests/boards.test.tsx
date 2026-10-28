import { CaptureUpdateAction } from "@excalidraw/excalidraw";
import { API } from "@excalidraw/excalidraw/tests/helpers/api";
import {
  act,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@excalidraw/excalidraw/tests/test-utils";
import { render as renderDOM } from "@testing-library/react";
import { vi } from "vitest";

import ExcalidrawApp from "../App";
import { appJotaiStore } from "../app-jotai";
import { BoardStorage } from "../boards/BoardStorage";
import { authStateAtom, boardRouteAtom, saveStatusAtom } from "../boards/state";

const { h } = window;

type FakeBoard = {
  id: string;
  name: string;
  version: number;
  scene: { elements: any[]; appState: Record<string, unknown> };
  createdAt: string;
  updatedAt: string;
};

const BOARD_ID = "11111111-1111-4111-8111-111111111111";

/** In-memory stand-in for excalidraw-server, served through `fetch`. */
class FakeServer {
  loggedIn = true;
  boards = new Map<string, FakeBoard>();
  sceneSaves: { boardId: string; version: number; scene: any }[] = [];
  conflictOnNextSave = false;

  addBoard(board: Partial<FakeBoard> & { id: string }) {
    this.boards.set(board.id, {
      name: "Untitled",
      version: 1,
      scene: { elements: [], appState: {} },
      createdAt: new Date(0).toISOString(),
      updatedAt: new Date(0).toISOString(),
      ...board,
    });
  }

  fetch = async (input: RequestInfo | URL, init: RequestInit = {}) => {
    const url = new URL(String(input), "http://localhost");
    const method = init.method ?? "GET";
    const body = init.body ? JSON.parse(String(init.body)) : undefined;
    const json = (status: number, payload?: unknown) =>
      new Response(payload === undefined ? null : JSON.stringify(payload), {
        status,
        headers: { "Content-Type": "application/json" },
      });

    if (!url.pathname.startsWith("/api/")) {
      return json(404);
    }
    if (
      method !== "GET" &&
      (init.headers as any)?.["X-Requested-With"] !== "excalidraw"
    ) {
      return json(403, { error: { code: "csrf", message: "csrf" } });
    }
    const path = url.pathname.slice("/api".length);

    if (path === "/auth/login" && method === "POST") {
      this.loggedIn = true;
      return json(200, { user: { id: "u1", email: body.email, name: "Me" } });
    }
    if (!this.loggedIn) {
      return json(401, {
        error: { code: "unauthorized", message: "login required" },
      });
    }
    if (path === "/auth/me") {
      return json(200, {
        user: { id: "u1", email: "me@example.com", name: "Me" },
      });
    }
    if (path === "/boards" && method === "GET") {
      return json(200, { boards: [...this.boards.values()].reverse() });
    }
    if (path === "/boards" && method === "POST") {
      const id = "22222222-2222-4222-8222-222222222222";
      this.addBoard({ id });
      return json(201, this.boards.get(id));
    }

    const match = path.match(/^\/boards\/([^/]+)(\/scene|\/files.*)?$/);
    const board = match && this.boards.get(match[1]);
    if (!board) {
      return json(404, {
        error: { code: "not_found", message: "board not found" },
      });
    }
    if (!match[2] && method === "GET") {
      return json(200, board);
    }
    if (match[2] === "/scene" && method === "PUT") {
      if (this.conflictOnNextSave || body.version !== board.version) {
        this.conflictOnNextSave = false;
        return json(409, {
          error: { code: "conflict", message: "board was modified elsewhere" },
          version: board.version + 1,
        });
      }
      board.version += 1;
      board.scene = body.scene;
      this.sceneSaves.push({
        boardId: board.id,
        version: body.version,
        scene: body.scene,
      });
      return json(200, {
        version: board.version,
        updatedAt: new Date().toISOString(),
      });
    }
    if (match[2]?.startsWith("/files") && method === "GET") {
      return json(200, { files: [], erroredIds: [] });
    }
    return json(404, { error: { code: "not_found", message: "no route" } });
  };
}

let server: FakeServer;

const addRectangle = (id: string) => {
  act(() => {
    API.updateScene({
      elements: [
        ...h.elements,
        API.createElement({ type: "rectangle", id, x: 0, y: 0 }),
      ],
      captureUpdate: CaptureUpdateAction.IMMEDIATELY,
    });
  });
};

describe("boards mode", () => {
  beforeAll(() => {
    vi.stubEnv("VITE_APP_BOARDS_API", "/api");
  });

  afterAll(() => {
    vi.unstubAllEnvs();
  });

  beforeEach(() => {
    server = new FakeServer();
    vi.stubGlobal("fetch", server.fetch);
    BoardStorage.close();
    appJotaiStore.set(authStateAtom, { status: "loading" });
    appJotaiStore.set(boardRouteAtom, undefined);
    window.history.replaceState(null, "", "/");
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("shows the login screen without a session, then opens the editor", async () => {
    server.loggedIn = false;
    server.addBoard({ id: BOARD_ID, name: "Plans" });

    const { container } = renderDOM(<ExcalidrawApp />);

    await screen.findByText("Log in to open your boards.");
    expect(container.querySelector("canvas.static")).toBeNull();

    fireEvent.change(screen.getByLabelText("Email"), {
      target: { value: "me@example.com" },
    });
    fireEvent.change(screen.getByLabelText("Password"), {
      target: { value: "secret password" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Log in" }));

    await waitFor(() => {
      expect(container.querySelector("canvas.static")).not.toBeNull();
      expect(window.location.pathname).toBe(`/board/${BOARD_ID}`);
    });
  });

  it("opens the latest board on / and autosaves only real changes", async () => {
    server.addBoard({
      id: BOARD_ID,
      name: "Plans",
      scene: {
        elements: [API.createElement({ type: "ellipse", id: "existing" })],
        appState: { viewBackgroundColor: "#fafafa" },
      },
    });

    await render(<ExcalidrawApp />);

    await waitFor(() => {
      expect(window.location.pathname).toBe(`/board/${BOARD_ID}`);
      expect(h.elements.map((el) => el.id)).toEqual(["existing"]);
    });
    expect(h.state.viewBackgroundColor).toBe("#fafafa");
    expect(h.state.name).toBe("Plans");

    // opening a board must not write it back
    await act(() => BoardStorage.flushAndWait());
    expect(server.sceneSaves).toHaveLength(0);

    addRectangle("added");
    await act(() => BoardStorage.flushAndWait());

    expect(server.sceneSaves).toHaveLength(1);
    expect(server.sceneSaves[0].version).toBe(1);
    expect(server.sceneSaves[0].scene.elements.map((el: any) => el.id)).toEqual(
      ["existing", "added"],
    );
    expect(server.sceneSaves[0].scene.appState.viewBackgroundColor).toBe(
      "#fafafa",
    );
    expect(appJotaiStore.get(saveStatusAtom)).toBe("saved");

    // selection changes are not persisted, so they don't trigger a save
    act(() => {
      API.setSelectedElements([h.elements[0]]);
    });
    await act(() => BoardStorage.flushAndWait());
    expect(server.sceneSaves).toHaveLength(1);
  });

  it("stops autosaving after a conflict instead of overwriting", async () => {
    server.addBoard({ id: BOARD_ID });
    window.history.replaceState(null, "", `/board/${BOARD_ID}`);

    await render(<ExcalidrawApp />);
    await waitFor(() => {
      expect(appJotaiStore.get(saveStatusAtom)).toBe("saved");
    });

    server.conflictOnNextSave = true;
    addRectangle("first");
    await act(() => BoardStorage.flushAndWait());
    expect(appJotaiStore.get(saveStatusAtom)).toBe("conflict");
    expect(BoardStorage.hasUnsavedChanges()).toBe(true);

    addRectangle("second");
    await act(() => BoardStorage.flushAndWait());
    expect(server.sceneSaves).toHaveLength(0);
  });

  it("keeps edits and asks to log in again when the session expires", async () => {
    server.addBoard({ id: BOARD_ID });
    window.history.replaceState(null, "", `/board/${BOARD_ID}`);

    await render(<ExcalidrawApp />);
    await waitFor(() => {
      expect(appJotaiStore.get(saveStatusAtom)).toBe("saved");
    });

    server.loggedIn = false;
    addRectangle("offline-edit");
    await act(() => BoardStorage.flushAndWait());

    expect(appJotaiStore.get(saveStatusAtom)).toBe("unauthorized");
    await screen.findByText("Session expired");
    expect(h.elements.map((el) => el.id)).toEqual(["offline-edit"]);

    fireEvent.change(screen.getByLabelText("Email"), {
      target: { value: "me@example.com" },
    });
    fireEvent.change(screen.getByLabelText("Password"), {
      target: { value: "secret password" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Log in" }));

    await waitFor(() => {
      expect(server.sceneSaves).toHaveLength(1);
    });
    expect(server.sceneSaves[0].scene.elements[0].id).toBe("offline-edit");
  });

  it("redirects an unknown board id to the default board", async () => {
    server.addBoard({ id: BOARD_ID });
    window.history.replaceState(
      null,
      "",
      "/board/99999999-9999-4999-8999-999999999999",
    );

    await render(<ExcalidrawApp />);

    await waitFor(() => {
      expect(window.location.pathname).toBe(`/board/${BOARD_ID}`);
    });
  });
});
