/**
 * Typed client for excalidraw-server (see excalidraw-server/README.md).
 */

import type { FileId } from "@excalidraw/element/types";
import type { ImportedDataState } from "@excalidraw/excalidraw/data/types";
import type { BinaryFileData } from "@excalidraw/excalidraw/types";

import { getBoardsApiBase } from "../app_constants";

export type BoardUser = {
  id: string;
  email: string;
  name: string;
};

export type WorkspaceRole = "owner" | "member";

export type Workspace = {
  id: string;
  name: string;
  role: WorkspaceRole;
  boardCount: number;
  createdAt: string;
  updatedAt: string;
};

export type Collection = {
  id: string;
  workspaceId: string;
  name: string;
  boardCount: number;
  createdAt: string;
  updatedAt: string;
};

/** a collection id, "unsorted" (boards in no collection), or null (all) */
export type CollectionFilter = string | null;
export const UNSORTED = "unsorted";

export type BoardSummary = {
  id: string;
  name: string;
  workspaceId: string;
  collectionId: string | null;
  /** null once the creator's account is deleted */
  createdBy: { id: string; name: string } | null;
  createdAt: string;
  /** last change to the content or name */
  updatedAt: string;
  thumbnailUpdatedAt: string | null;
};

export type Board = BoardSummary & {
  version: number;
  scene: {
    elements: ImportedDataState["elements"];
    appState: ImportedDataState["appState"];
  };
};

export class ApiError extends Error {
  constructor(readonly status: number, readonly code: string, message: string) {
    super(message);
    this.name = "ApiError";
  }
}

export class UnauthorizedError extends ApiError {}
export class NotFoundError extends ApiError {}
export class ConflictError extends ApiError {
  constructor(message: string, readonly currentVersion: number) {
    super(409, "conflict", message);
  }
}

// the server caps ids per files request
const MAX_FILE_IDS_PER_REQUEST = 200;

/**
 * `body` is an already serialized JSON string, or a Blob sent as-is with its
 * own type.
 */
const request = async <T>(
  method: "GET" | "POST" | "PUT" | "PATCH" | "DELETE",
  path: string,
  body?: string | Blob,
): Promise<T> => {
  const contentType =
    body === undefined
      ? undefined
      : typeof body === "string"
      ? "application/json"
      : body.type;
  const res = await fetch(`${getBoardsApiBase()}${path}`, {
    method,
    credentials: "same-origin",
    headers: {
      // CSRF guard, required by the server on state-changing requests
      "X-Requested-With": "excalidraw",
      ...(contentType && { "Content-Type": contentType }),
    },
    body,
  });
  if (res.ok) {
    return (res.status === 204 ? undefined : await res.json()) as T;
  }

  let payload: any = null;
  try {
    payload = await res.json();
  } catch {
    // non-JSON error body (e.g. from a proxy)
  }
  const code: string = payload?.error?.code ?? "http_error";
  const message: string =
    payload?.error?.message ?? `${res.status} ${res.statusText}`;
  switch (res.status) {
    case 401:
      throw new UnauthorizedError(res.status, code, message);
    case 404:
      throw new NotFoundError(res.status, code, message);
    case 409:
      // other 409s (e.g. workspace_not_empty) are plain ApiErrors
      if (code === "conflict") {
        throw new ConflictError(message, payload?.version);
      }
      throw new ApiError(res.status, code, message);
    default:
      throw new ApiError(res.status, code, message);
  }
};

const boardPath = (boardId: string) => `/boards/${encodeURIComponent(boardId)}`;
const workspacePath = (workspaceId: string) =>
  `/workspaces/${encodeURIComponent(workspaceId)}`;
const collectionPath = (collectionId: string) =>
  `/collections/${encodeURIComponent(collectionId)}`;

export const boardsApi = {
  me: () =>
    request<{ user: BoardUser }>("GET", "/auth/me").then((res) => res.user),

  login: (email: string, password: string) =>
    request<{ user: BoardUser }>(
      "POST",
      "/auth/login",
      JSON.stringify({ email, password }),
    ).then((res) => res.user),

  logout: () => request<void>("POST", "/auth/logout"),

  // workspaces -------------------------------------------------------------

  listWorkspaces: () =>
    request<{ workspaces: Workspace[] }>("GET", "/workspaces").then(
      (res) => res.workspaces,
    ),

  createWorkspace: (name: string) =>
    request<Workspace>("POST", "/workspaces", JSON.stringify({ name })),

  renameWorkspace: (workspaceId: string, name: string) =>
    request<Workspace>(
      "PATCH",
      workspacePath(workspaceId),
      JSON.stringify({ name }),
    ),

  deleteWorkspace: (workspaceId: string) =>
    request<void>("DELETE", workspacePath(workspaceId)),

  // collections ------------------------------------------------------------

  listCollections: (workspaceId: string) =>
    request<{ collections: Collection[] }>(
      "GET",
      `${workspacePath(workspaceId)}/collections`,
    ).then((res) => res.collections),

  createCollection: (workspaceId: string, name: string) =>
    request<Collection>(
      "POST",
      `${workspacePath(workspaceId)}/collections`,
      JSON.stringify({ name }),
    ),

  renameCollection: (collectionId: string, name: string) =>
    request<Collection>(
      "PATCH",
      collectionPath(collectionId),
      JSON.stringify({ name }),
    ),

  deleteCollection: (collectionId: string) =>
    request<void>("DELETE", collectionPath(collectionId)),

  // boards -----------------------------------------------------------------

  /** One page of a workspace's boards, most recently updated first. */
  listBoards: (
    workspaceId: string,
    opts: {
      collection?: CollectionFilter;
      cursor?: string;
      limit?: number;
    } = {},
  ) => {
    const params = new URLSearchParams();
    if (opts.collection) {
      params.set("collection", opts.collection);
    }
    if (opts.cursor) {
      params.set("cursor", opts.cursor);
    }
    if (opts.limit) {
      params.set("limit", String(opts.limit));
    }
    const query = params.toString();
    return request<{ boards: BoardSummary[]; nextCursor: string }>(
      "GET",
      `${workspacePath(workspaceId)}/boards${query ? `?${query}` : ""}`,
    );
  },

  createBoard: (
    workspaceId: string,
    opts: { name?: string; collectionId?: string | null } = {},
  ) =>
    request<Board>(
      "POST",
      `${workspacePath(workspaceId)}/boards`,
      JSON.stringify({
        name: opts.name ?? "",
        collectionId: opts.collectionId ?? null,
      }),
    ),

  getBoard: (boardId: string) => request<Board>("GET", boardPath(boardId)),

  /** `collectionId: null` moves the board to unsorted. */
  updateBoard: (
    boardId: string,
    patch: { name?: string; collectionId?: string | null },
  ) =>
    request<BoardSummary>("PATCH", boardPath(boardId), JSON.stringify(patch)),

  deleteBoard: (boardId: string) => request<void>("DELETE", boardPath(boardId)),

  /** `sceneJSON` is the serialized `{elements, appState}`. */
  saveScene: (boardId: string, version: number, sceneJSON: string) =>
    request<{ version: number; updatedAt: string }>(
      "PUT",
      `${boardPath(boardId)}/scene`,
      `{"version":${version},"scene":${sceneJSON}}`,
    ),

  putFile: (boardId: string, file: BinaryFileData) =>
    request<void>(
      "PUT",
      `${boardPath(boardId)}/files/${encodeURIComponent(file.id)}`,
      JSON.stringify({
        mimeType: file.mimeType,
        dataURL: file.dataURL,
        created: file.created,
        version: file.version,
      }),
    ),

  getFiles: async (boardId: string, fileIds: readonly FileId[]) => {
    const files: BinaryFileData[] = [];
    const erroredIds: FileId[] = [];
    for (let i = 0; i < fileIds.length; i += MAX_FILE_IDS_PER_REQUEST) {
      const ids = fileIds.slice(i, i + MAX_FILE_IDS_PER_REQUEST);
      const res = await request<{
        files: BinaryFileData[];
        erroredIds: FileId[];
      }>(
        "GET",
        `${boardPath(boardId)}/files?ids=${ids
          .map(encodeURIComponent)
          .join(",")}`,
      );
      files.push(...res.files);
      erroredIds.push(...res.erroredIds);
    }
    return { files, erroredIds };
  },
};

// thumbnails ----------------------------------------------------------------

export const thumbnailsApi = {
  put: (boardId: string, image: Blob) =>
    request<{ thumbnailUpdatedAt: string }>(
      "PUT",
      `${boardPath(boardId)}/thumbnail`,
      image,
    ),

  delete: (boardId: string) =>
    request<void>("DELETE", `${boardPath(boardId)}/thumbnail`),

  /** The URL changes whenever the thumbnail does, so it's cached forever. */
  url: (board: Pick<BoardSummary, "id" | "thumbnailUpdatedAt">) =>
    board.thumbnailUpdatedAt
      ? `${getBoardsApiBase()}${boardPath(
          board.id,
        )}/thumbnail?v=${encodeURIComponent(board.thumbnailUpdatedAt)}`
      : null,
};
