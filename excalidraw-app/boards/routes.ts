import { appJotaiStore } from "../app-jotai";

import { UNSORTED } from "./api";
import { BoardStorage } from "./BoardStorage";
import { routeAtom } from "./state";

import type { Route } from "./state";

const UUID =
  "[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}";
const BOARD_PATH_RE = new RegExp(`^/board/(${UUID})/?$`);
const WORKSPACE_PATH_RE = new RegExp(`^/w/(${UUID})/?$`);
const COLLECTION_RE = new RegExp(`^(${UUID}|${UNSORTED})$`);

const LAST_WORKSPACE_KEY = "excalidraw-boards-last-workspace";

export const parseRoute = (
  location: Pick<Location, "pathname" | "search">,
): Route => {
  const board = location.pathname.match(BOARD_PATH_RE);
  if (board) {
    return { page: "board", boardId: board[1] };
  }
  const workspace = location.pathname.match(WORKSPACE_PATH_RE);
  if (workspace) {
    const collection = new URLSearchParams(location.search).get("collection");
    return {
      page: "dashboard",
      workspaceId: workspace[1],
      collection:
        collection && COLLECTION_RE.test(collection) ? collection : null,
    };
  }
  return { page: "home" };
};

export const routeToUrl = (route: Route) => {
  switch (route.page) {
    case "home":
      return "/";
    case "board":
      return `/board/${route.boardId}`;
    case "dashboard":
      return `/w/${route.workspaceId}${
        route.collection
          ? `?collection=${encodeURIComponent(route.collection)}`
          : ""
      }`;
  }
};

export const getLastWorkspaceId = () => {
  try {
    return localStorage.getItem(LAST_WORKSPACE_KEY);
  } catch {
    return null;
  }
};

export const setLastWorkspaceId = (workspaceId: string | null) => {
  try {
    if (workspaceId) {
      localStorage.setItem(LAST_WORKSPACE_KEY, workspaceId);
    } else {
      localStorage.removeItem(LAST_WORKSPACE_KEY);
    }
  } catch {
    // storage unavailable (private mode); only the convenience is lost
  }
};

const isSamePage = (a: Route, b: Route) => routeToUrl(a) === routeToUrl(b);

/**
 * Called before leaving the open board: saves its pending changes, and if they
 * can't be saved (offline, conflict, expired session) asks the user whether to
 * leave anyway. Returns false if the user chose to stay.
 */
const leaveBoard = async (next: Route) => {
  await BoardStorage.flushAndWait();
  if (
    BoardStorage.hasUnsavedChanges() &&
    !window.confirm(
      "Changes on this board could not be saved and will be lost. Leave anyway?",
    )
  ) {
    return false;
  }
  if (next.page !== "board") {
    // the editor unmounts; nothing more to save
    BoardStorage.close();
  }
  return true;
};

/**
 * The only way to change page from the app. Resolves to false if the user
 * chose to stay on a board with unsaved changes.
 */
export const navigate = async (
  route: Route,
  { replace = false }: { replace?: boolean } = {},
): Promise<boolean> => {
  const current = appJotaiStore.get(routeAtom);
  if (current?.page === "board" && !isSamePage(current, route)) {
    if (!(await leaveBoard(route))) {
      return false;
    }
  }
  const url = routeToUrl(route);
  if (replace) {
    window.history.replaceState(null, "", url);
  } else {
    window.history.pushState(null, "", url);
  }
  appJotaiStore.set(routeAtom, route);
  return true;
};

/**
 * Browser back/forward. The URL has already changed, so if the user chooses to
 * stay on the board, its URL is pushed back.
 */
export const handlePopState = async () => {
  const next = parseRoute(window.location);
  const current = appJotaiStore.get(routeAtom);
  if (current?.page === "board" && !isSamePage(current, next)) {
    if (!(await leaveBoard(next))) {
      window.history.pushState(null, "", routeToUrl(current));
      return;
    }
  }
  appJotaiStore.set(routeAtom, next);
};
