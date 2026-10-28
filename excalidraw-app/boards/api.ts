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

export type BoardSummary = {
  id: string;
  name: string;
  createdAt: string;
  updatedAt: string;
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

/** `body` is an already serialized JSON string. */
const request = async <T>(
  method: "GET" | "POST" | "PUT" | "PATCH" | "DELETE",
  path: string,
  body?: string,
): Promise<T> => {
  const res = await fetch(`${getBoardsApiBase()}${path}`, {
    method,
    credentials: "same-origin",
    headers: {
      // CSRF guard, required by the server on state-changing requests
      "X-Requested-With": "excalidraw",
      ...(body !== undefined && { "Content-Type": "application/json" }),
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
      throw new ConflictError(message, payload?.version);
    default:
      throw new ApiError(res.status, code, message);
  }
};

const boardPath = (boardId: string) => `/boards/${encodeURIComponent(boardId)}`;

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

  listBoards: () =>
    request<{ boards: BoardSummary[] }>("GET", "/boards").then(
      (res) => res.boards,
    ),

  createBoard: (name = "") =>
    request<Board>("POST", "/boards", JSON.stringify({ name })),

  getBoard: (boardId: string) => request<Board>("GET", boardPath(boardId)),

  renameBoard: (boardId: string, name: string) =>
    request<BoardSummary>(
      "PATCH",
      boardPath(boardId),
      JSON.stringify({ name }),
    ),

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
