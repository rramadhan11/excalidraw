import { atom } from "../app-jotai";

import type { BoardSummary, BoardUser } from "./api";

export type AuthState =
  | { status: "loading" }
  | { status: "anonymous" }
  | { status: "unreachable"; message: string }
  | { status: "authenticated"; user: BoardUser }
  /** the session ended while the editor was open; edits are kept in memory
   * until the user logs in again */
  | { status: "expired"; user: BoardUser };

export const authStateAtom = atom<AuthState>({ status: "loading" });

export type SaveStatus =
  /** no board open yet */
  | "idle"
  | "saved"
  | "saving"
  /** last save failed (network/server); retried automatically */
  | "error"
  /** another tab or device saved first; autosave is stopped */
  | "conflict"
  /** the board was deleted elsewhere; autosave is stopped */
  | "deleted"
  /** session expired; autosave resumes after logging in */
  | "unauthorized";

export const saveStatusAtom = atom<SaveStatus>("idle");

export const currentBoardAtom = atom<BoardSummary | null>(null);

/** board id from the URL (`/board/:id`), null on `/`, undefined until the
 * URL has been read */
export const boardRouteAtom = atom<string | null | undefined>(undefined);

/** bumped to make the boards list refetch (after create/rename/delete/save) */
export const boardsListVersionAtom = atom(0);
