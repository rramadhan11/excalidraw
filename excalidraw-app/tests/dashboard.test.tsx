import {
  act,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import { vi } from "vitest";

import ExcalidrawApp from "../App";
import { appJotaiStore } from "../app-jotai";
import { BoardStorage } from "../boards/BoardStorage";
import { authStateAtom, dashboardNoticeAtom, routeAtom } from "../boards/state";

import { FakeBoardsServer, WORKSPACE_ID } from "./helpers/fakeBoardsServer";

vi.mock("../boards/thumbnail", () => ({
  renderThumbnail: vi.fn(
    async () => new Blob(["jpeg"], { type: "image/jpeg" }),
  ),
}));

let server: FakeBoardsServer;

const renderDashboard = async (url = "/") => {
  window.history.replaceState(null, "", url);
  render(<ExcalidrawApp />);
  await waitForData();
};

/** the dashboard renders before its lists arrive */
const waitForData = () =>
  waitFor(() => {
    const workspaceName = document.querySelector(".boards-ws__name");
    expect(workspaceName?.textContent ?? "…").not.toBe("…");
    expect(screen.queryByText("Loading…")).toBeNull();
  });

const boardNames = () =>
  screen
    .queryAllByRole("button", { name: /^Open / })
    .map((el) => el.getAttribute("aria-label")!.slice("Open ".length));

const openMenu = (label: string) =>
  fireEvent.click(screen.getByRole("button", { name: label }));

const typeAndSubmit = (label: string, value: string) => {
  const input = screen.getByLabelText(label);
  fireEvent.change(input, { target: { value } });
  fireEvent.keyDown(input, { key: "Enter" });
};

describe("boards mode: dashboard", () => {
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
    localStorage.clear();
    appJotaiStore.set(authStateAtom, { status: "loading" });
    appJotaiStore.set(routeAtom, undefined);
    appJotaiStore.set(dashboardNoticeAtom, null);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("opens the last used workspace from /", async () => {
    const other = server.addWorkspace("Kerja");
    server.addBoard({ name: "Work board", workspaceId: other });
    localStorage.setItem("excalidraw-boards-last-workspace", other);

    await renderDashboard("/");

    await waitFor(() => expect(boardNames()).toEqual(["Work board"]));
    expect(window.location.pathname).toBe(`/w/${other}`);
  });

  it("lists boards newest first, with a recently modified section", async () => {
    for (const name of ["b1", "b2", "b3", "b4", "b5"]) {
      server.addBoard({ name });
    }
    await renderDashboard(`/w/${WORKSPACE_ID}`);

    const recent = screen.getByRole("heading", { name: "Recently modified" })
      .parentElement!;
    await waitFor(() =>
      expect(
        within(recent)
          .getAllByRole("button", { name: /^Open / })
          .map((el) => el.getAttribute("aria-label")),
      ).toEqual(["Open b5", "Open b4", "Open b3", "Open b2"]),
    );
  });

  it("filters by collection and starts drawing inside it", async () => {
    const work = server.addCollection("Work");
    server.addBoard({ name: "In work", collectionId: work });
    server.addBoard({ name: "Loose" });
    await renderDashboard(`/w/${WORKSPACE_ID}`);

    fireEvent.click(
      await screen.findByRole("button", { name: /^Work\s*\d*$/ }),
    );
    await waitFor(() => expect(boardNames()).toEqual(["In work"]));
    expect(window.location.search).toBe(`?collection=${work}`);

    fireEvent.click(screen.getByRole("button", { name: /^Unsorted$/ }));
    await waitFor(() => expect(boardNames()).toEqual(["Loose"]));

    fireEvent.click(screen.getByRole("button", { name: /^Work\s*\d*$/ }));
    await waitFor(() => expect(boardNames()).toEqual(["In work"]));
    fireEvent.click(screen.getByRole("button", { name: "Start drawing" }));

    await waitFor(() => expect(window.location.pathname).toMatch(/^\/board\//));
    const created = [...server.boards.values()].find(
      (b) => b.id === window.location.pathname.split("/")[2],
    );
    expect(created?.collectionId).toBe(work);
  });

  it("moves a board to a collection from its menu", async () => {
    const work = server.addCollection("Work");
    const board = server.addBoard({ name: "Plan" });
    await renderDashboard(`/w/${WORKSPACE_ID}`);

    openMenu("Plan options");
    fireEvent.click(screen.getByRole("menuitem", { name: /Move to/ }));
    fireEvent.click(screen.getByRole("menuitem", { name: "Work" }));

    await waitFor(() =>
      expect(server.boardPatches).toEqual([
        { boardId: board, patch: { collectionId: work } },
      ]),
    );
  });

  it("renames and deletes a board from its menu", async () => {
    const board = server.addBoard({ name: "Plan" });
    await renderDashboard(`/w/${WORKSPACE_ID}`);

    openMenu("Plan options");
    fireEvent.click(screen.getByRole("menuitem", { name: /Rename/ }));
    typeAndSubmit("Board name", "Roadmap");
    await screen.findByRole("button", { name: "Open Roadmap" });

    openMenu("Roadmap options");
    fireEvent.click(screen.getByRole("menuitem", { name: /Delete/ }));
    fireEvent.click(screen.getByRole("button", { name: "Delete" }));
    await waitFor(() => expect(server.boards.has(board)).toBe(false));
    await screen.findByText("No boards here yet.");
  });

  it("creates collections, and deleting one keeps its boards as unsorted", async () => {
    server.addBoard({ name: "Plan" });
    await renderDashboard(`/w/${WORKSPACE_ID}`);

    fireEvent.click(screen.getByRole("button", { name: "New collection" }));
    typeAndSubmit("New collection name", "Ideas");
    // creating a collection opens it
    await waitFor(() =>
      expect(window.location.search).toMatch(/^\?collection=/),
    );
    const ideas = [...server.collections.values()].find(
      (c) => c.name === "Ideas",
    )!;

    const board = [...server.boards.values()][0];
    board.collectionId = ideas.id;

    openMenu("Ideas options");
    fireEvent.click(screen.getByRole("menuitem", { name: /Delete/ }));
    expect(screen.getByText(/to\s+Unsorted/)).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Delete" }));

    await waitFor(() => expect(server.collections.size).toBe(0));
    expect(board.collectionId).toBeNull();
    expect(server.boards.size).toBe(1);
    await waitFor(() => expect(window.location.search).toBe(""));
  });

  it("creates, renames and switches workspaces", async () => {
    server.addBoard({ name: "Personal board" });
    await renderDashboard(`/w/${WORKSPACE_ID}`);

    fireEvent.click(screen.getByRole("button", { name: /Me's workspace/ }));
    fireEvent.click(screen.getByRole("menuitem", { name: /Create workspace/ }));
    typeAndSubmit("New workspace name", "Kerja");

    await waitFor(() =>
      expect(window.location.pathname).not.toBe(`/w/${WORKSPACE_ID}`),
    );
    await screen.findByText("No boards here yet.");
    expect([...server.workspaces.values()].map((w) => w.name).sort()).toEqual([
      "Kerja",
      "Me's workspace",
    ]);

    fireEvent.click(screen.getByRole("button", { name: /Kerja/ }));
    fireEvent.click(screen.getByRole("menuitem", { name: /Rename workspace/ }));
    typeAndSubmit("Workspace name", "Office");
    await screen.findByRole("button", { name: /Office/ });

    fireEvent.click(screen.getByRole("button", { name: /Office/ }));
    fireEvent.click(screen.getByRole("menuitem", { name: /Me's workspace/ }));
    await waitFor(() => expect(boardNames()).toEqual(["Personal board"]));
  });

  it("shows why a non-empty workspace can't be deleted", async () => {
    server.addBoard({ name: "Plan" });
    await renderDashboard(`/w/${WORKSPACE_ID}`);

    fireEvent.click(screen.getByRole("button", { name: /Me's workspace/ }));
    fireEvent.click(screen.getByRole("menuitem", { name: /Delete workspace/ }));
    fireEvent.click(screen.getByRole("button", { name: "Delete" }));

    await screen.findByText(/still has boards/);
    expect(server.workspaces.has(WORKSPACE_ID)).toBe(true);
  });

  it("loads more boards page by page", async () => {
    for (let i = 0; i < 65; i++) {
      server.addBoard({ name: `b${i}` });
    }
    await renderDashboard(`/w/${WORKSPACE_ID}?collection=unsorted`);

    await waitFor(() => expect(boardNames()).toHaveLength(60));
    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: "Load more" }));
    });
    await waitFor(() => expect(boardNames()).toHaveLength(65));
    expect(screen.queryByRole("button", { name: "Load more" })).toBeNull();
  });
});
