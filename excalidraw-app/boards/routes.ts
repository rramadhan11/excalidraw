import { appJotaiStore } from "../app-jotai";

import { BoardStorage } from "./BoardStorage";
import { boardRouteAtom } from "./state";

const BOARD_PATH_RE = /^\/board\/([0-9a-fA-F-]{36})\/?$/;

export const boardIdFromPath = (pathname: string): string | null =>
  pathname.match(BOARD_PATH_RE)?.[1] ?? null;

export const boardPath = (boardId: string) => `/board/${boardId}`;

/** Points the URL at a board without adding a history entry. */
export const replaceBoardRoute = (boardId: string | null) => {
  window.history.replaceState(null, "", boardId ? boardPath(boardId) : "/");
  appJotaiStore.set(boardRouteAtom, boardId);
};

/**
 * Opens another board. Pending changes are saved first; if they can't be
 * (offline, conflict, expired session) the user has to confirm losing them.
 */
export const navigateToBoard = async (boardId: string | null) => {
  if (boardId === appJotaiStore.get(boardRouteAtom)) {
    return;
  }
  await BoardStorage.flushAndWait();
  if (
    BoardStorage.hasUnsavedChanges() &&
    !window.confirm(
      "Changes on this board could not be saved and will be lost. Leave anyway?",
    )
  ) {
    return;
  }
  window.history.pushState(null, "", boardId ? boardPath(boardId) : "/");
  appJotaiStore.set(boardRouteAtom, boardId);
};
