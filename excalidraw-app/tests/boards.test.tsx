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
import { navigate } from "../boards/routes";
import {
  authStateAtom,
  dashboardNoticeAtom,
  routeAtom,
  saveStatusAtom,
} from "../boards/state";

import {
  BOARD_ID,
  FakeBoardsServer,
  WORKSPACE_ID,
} from "./helpers/fakeBoardsServer";

vi.mock("../boards/thumbnail", () => ({
  renderThumbnail: vi.fn(
    async () => new Blob(["jpeg"], { type: "image/jpeg" }),
  ),
}));

const { h } = window;

let server: FakeBoardsServer;

const addRectangle = (id: string) => {
  const rectangle = API.createElement({ type: "rectangle", id, x: 0, y: 0 });
  act(() => {
    API.updateScene({
      elements: [...h.elements, rectangle],
      captureUpdate: CaptureUpdateAction.IMMEDIATELY,
    });
  });
  return rectangle;
};

const openBoard = async (boardId = BOARD_ID) => {
  window.history.replaceState(null, "", `/board/${boardId}`);
  await render(<ExcalidrawApp />);
  await waitFor(() => {
    expect(appJotaiStore.get(saveStatusAtom)).toBe("saved");
  });
};

const logIn = () => {
  fireEvent.change(screen.getByLabelText("Email"), {
    target: { value: "me@example.com" },
  });
  fireEvent.change(screen.getByLabelText("Password"), {
    target: { value: "secret password" },
  });
  fireEvent.click(screen.getByRole("button", { name: "Log in" }));
};

describe("boards mode: editor", () => {
  beforeAll(() => {
    vi.stubEnv("VITE_APP_BOARDS_API", "/api");
  });

  afterAll(() => {
    vi.unstubAllEnvs();
  });

  beforeEach(() => {
    server = new FakeBoardsServer();
    vi.stubGlobal("fetch", server.fetch);
    BoardStorage.close();
    appJotaiStore.set(authStateAtom, { status: "loading" });
    appJotaiStore.set(routeAtom, undefined);
    appJotaiStore.set(dashboardNoticeAtom, null);
    window.history.replaceState(null, "", "/");
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  });

  it("shows the login screen without a session, then the dashboard", async () => {
    server.loggedIn = false;
    server.addBoard({ id: BOARD_ID, name: "Plans" });

    renderDOM(<ExcalidrawApp />);

    await screen.findByText("Log in to open your boards.");
    logIn();

    await screen.findByRole("button", { name: "Open Plans" });
    expect(window.location.pathname).toBe(`/w/${WORKSPACE_ID}`);
  });

  it("opens a board and autosaves only real changes", async () => {
    server.addBoard({
      id: BOARD_ID,
      name: "Plans",
      scene: {
        elements: [API.createElement({ type: "ellipse", id: "existing" })],
        appState: { viewBackgroundColor: "#fafafa" },
      },
    });

    await openBoard();

    expect(h.elements.map((el) => el.id)).toEqual(["existing"]);
    expect(h.state.viewBackgroundColor).toBe("#fafafa");
    expect(h.state.name).toBe("Plans");

    // opening a board must not write it back
    await act(() => BoardStorage.flushAndWait());
    expect(server.sceneSaves).toHaveLength(0);

    const added = addRectangle("added");
    await act(() => BoardStorage.flushAndWait());

    expect(server.sceneSaves).toHaveLength(1);
    expect(server.sceneSaves[0].version).toBe(1);
    expect(server.sceneSaves[0].scene.elements.map((el: any) => el.id)).toEqual(
      ["existing", "added"],
    );
    expect(server.sceneSaves[0].scene.appState.viewBackgroundColor).toBe(
      "#fafafa",
    );

    // selection changes are not persisted, so they don't trigger a save
    act(() => {
      API.setSelectedElements([added]);
    });
    await act(() => BoardStorage.flushAndWait());
    expect(server.sceneSaves).toHaveLength(1);
  });

  it("uploads a thumbnail after saving and removes it when the board is emptied", async () => {
    server.addBoard({ id: BOARD_ID });
    await openBoard();

    addRectangle("a");
    await act(() => BoardStorage.flushAndWait());
    expect(server.thumbnailPuts).toEqual([
      { boardId: BOARD_ID, type: "image/jpeg" },
    ]);

    act(() => {
      API.updateScene({
        elements: [],
        captureUpdate: CaptureUpdateAction.IMMEDIATELY,
      });
    });
    await act(() => BoardStorage.flushAndWait());
    expect(server.thumbnailDeletes).toEqual([BOARD_ID]);
  });

  it("gives boards from before thumbnails existed one when opened", async () => {
    server.addBoard({
      id: BOARD_ID,
      scene: {
        elements: [API.createElement({ type: "ellipse", id: "old" })],
        appState: {},
      },
    });
    await openBoard();
    await act(() => BoardStorage.flushThumbnail());
    expect(server.thumbnailPuts).toHaveLength(1);
    expect(server.sceneSaves).toHaveLength(0);
  });

  it("saves pending changes before leaving the editor for the dashboard", async () => {
    server.addBoard({ id: BOARD_ID });
    await openBoard();

    addRectangle("unsaved");
    // no flush: leaving must save it
    await act(() =>
      navigate({
        page: "dashboard",
        workspaceId: WORKSPACE_ID,
        collection: null,
      }),
    );

    expect(server.sceneSaves).toHaveLength(1);
    expect(server.sceneSaves[0].scene.elements[0].id).toBe("unsaved");
    await screen.findByRole("heading", { name: "All boards" });
  });

  it("stops autosaving after a conflict and asks before leaving", async () => {
    server.addBoard({ id: BOARD_ID });
    await openBoard();

    server.conflictOnNextSave = true;
    addRectangle("first");
    await act(() => BoardStorage.flushAndWait());
    expect(appJotaiStore.get(saveStatusAtom)).toBe("conflict");

    addRectangle("second");
    await act(() => BoardStorage.flushAndWait());
    expect(server.sceneSaves).toHaveLength(0);

    const confirm = vi.spyOn(window, "confirm").mockReturnValue(false);
    let left = true;
    await act(async () => {
      left = await navigate({
        page: "dashboard",
        workspaceId: WORKSPACE_ID,
        collection: null,
      });
    });
    expect(confirm).toHaveBeenCalled();
    expect(left).toBe(false);
    expect(window.location.pathname).toBe(`/board/${BOARD_ID}`);
  });

  it("keeps edits and asks to log in again when the session expires", async () => {
    server.addBoard({ id: BOARD_ID });
    await openBoard();

    server.loggedIn = false;
    addRectangle("offline-edit");
    await act(() => BoardStorage.flushAndWait());

    expect(appJotaiStore.get(saveStatusAtom)).toBe("unauthorized");
    await screen.findByText("Session expired");
    expect(h.elements.map((el) => el.id)).toEqual(["offline-edit"]);

    logIn();

    await waitFor(() => {
      expect(server.sceneSaves).toHaveLength(1);
    });
    expect(server.sceneSaves[0].scene.elements[0].id).toBe("offline-edit");
  });

  it("sends an unknown board id to the dashboard with a notice", async () => {
    window.history.replaceState(
      null,
      "",
      "/board/99999999-9999-4999-8999-999999999999",
    );

    renderDOM(<ExcalidrawApp />);

    await screen.findByText(/That board doesn't exist/);
    expect(window.location.pathname).toBe(`/w/${WORKSPACE_ID}`);
  });
});
