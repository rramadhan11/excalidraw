/**
 * Persists the open board to excalidraw-server in boards mode. Exposes the
 * same save interface as LocalData so App's onChange can use either.
 *
 * - saves are debounced and serialized: at most one save is in flight, and
 *   changes made meanwhile are coalesced into the next one
 * - a save only hits the server when the persisted scene actually changed
 * - every save carries the version it is based on. A 409 means another tab or
 *   device saved first; autosave then stops instead of overwriting that work
 * - image files are uploaded before the scene that references them
 */
import { clearAppStateForDatabase } from "@excalidraw/excalidraw/appState";
import { debounce } from "@excalidraw/common";
import { getNonDeletedElements } from "@excalidraw/element";

import type { ExcalidrawElement, FileId } from "@excalidraw/element/types";
import type {
  AppState,
  BinaryFileData,
  BinaryFiles,
} from "@excalidraw/excalidraw/types";

import { appJotaiStore } from "../app-jotai";
import {
  BOARDS_SAVE_RETRY_TIMEOUT,
  BOARDS_THUMBNAIL_INTERVAL,
  SAVE_TO_BOARDS_SERVER_TIMEOUT,
} from "../app_constants";
import { FileManager } from "../data/FileManager";
import { FileStatusStore } from "../data/fileStatusStore";

import {
  boardsApi,
  ConflictError,
  NotFoundError,
  thumbnailsApi,
  UnauthorizedError,
  type Board,
} from "./api";
import { renderThumbnail } from "./thumbnail";
import {
  authStateAtom,
  boardsListVersionAtom,
  currentBoardAtom,
  saveStatusAtom,
  type SaveStatus,
} from "./state";

type PendingSave = {
  elements: readonly ExcalidrawElement[];
  appState: AppState;
  files: BinaryFiles;
  onFilesSaved: () => void;
};

type BlockReason = "conflict" | "deleted" | "unauthorized";

type ThumbnailJob = {
  boardId: string;
  elements: readonly ExcalidrawElement[];
  appState: AppState;
  files: BinaryFiles;
};

/**
 * Thumbnails are a convenience for the dashboard: failures are logged, never
 * surfaced as a save error.
 */
const uploadThumbnail = async (job: ThumbnailJob) => {
  const elements = getNonDeletedElements(job.elements);
  if (!elements.length) {
    await thumbnailsApi.delete(job.boardId);
    return;
  }
  const image = await renderThumbnail(elements, job.appState, job.files);
  await thumbnailsApi.put(job.boardId, image);
};

/** the part of the editor state that is stored on the server */
const serializeScene = (
  elements: readonly ExcalidrawElement[],
  appState: AppState,
) =>
  JSON.stringify({
    elements: getNonDeletedElements(elements),
    appState: clearAppStateForDatabase(appState),
  });

const createFileManager = (boardId: string) =>
  new FileManager({
    onFileStatusChange: FileStatusStore.updateStatuses.bind(FileStatusStore),
    async getFiles(ids) {
      const { files, erroredIds } = await boardsApi.getFiles(boardId, ids);
      return {
        loadedFiles: files,
        erroredFiles: new Map(erroredIds.map((id) => [id, true as const])),
      };
    },
    async saveFiles({ addedFiles }) {
      const savedFiles = new Map<FileId, BinaryFileData>();
      const erroredFiles = new Map<FileId, BinaryFileData>();
      await Promise.all(
        [...addedFiles].map(async ([id, fileData]) => {
          try {
            await boardsApi.putFile(boardId, fileData);
            savedFiles.set(id, fileData);
          } catch (error) {
            console.error(error);
            erroredFiles.set(id, fileData);
          }
        }),
      );
      return { savedFiles, erroredFiles };
    },
  });

const setStatus = (status: SaveStatus) =>
  appJotaiStore.set(saveStatusAtom, status);

export class BoardStorage {
  private static board: { id: string; version: number } | null = null;
  /** serialized scene as last confirmed by the server; null until the first
   * onChange after opening a board establishes the baseline */
  private static lastSavedScene: string | null = null;
  private static pending: PendingSave | null = null;
  private static inFlight: Promise<void> | null = null;
  private static blocked: BlockReason | null = null;
  private static retryTimer: ReturnType<typeof setTimeout> | null = null;
  private static openSeq = 0;

  private static thumbnailJob: ThumbnailJob | null = null;
  private static thumbnailTimer: ReturnType<typeof setTimeout> | null = null;
  private static thumbnailInFlight: Promise<void> = Promise.resolve();
  private static lastThumbnailAt = 0;

  /** replaced per board, since its saved-file bookkeeping is per board */
  static fileStorage: FileManager = createFileManager("");

  private static _debouncedFlush = debounce(() => {
    BoardStorage.flush();
  }, SAVE_TO_BOARDS_SERVER_TIMEOUT);

  /**
   * Loads a board and makes it the save target. The previous board's pending
   * changes are saved first.
   */
  static async open(boardId: string): Promise<Board> {
    const seq = ++this.openSeq;
    await this.flushAndWait();
    this.close();

    const board = await boardsApi.getBoard(boardId);
    if (seq !== this.openSeq) {
      // superseded by a later open() (fast navigation); that one wins
      return board;
    }
    this.board = { id: board.id, version: board.version };
    this.fileStorage = createFileManager(board.id);
    const { scene, version, ...summary } = board;
    appJotaiStore.set(currentBoardAtom, summary);
    setStatus("saved");
    return board;
  }

  /** Stops saving to the current board, discarding unsaved changes (used when
   * the board itself is being deleted). */
  static close() {
    this._debouncedFlush.cancel();
    this.clearRetry();
    if (this.thumbnailTimer) {
      clearTimeout(this.thumbnailTimer);
      this.thumbnailTimer = null;
    }
    this.thumbnailJob = null;
    this.board = null;
    this.pending = null;
    this.blocked = null;
    this.lastSavedScene = null;
    this.fileStorage.reset();
    appJotaiStore.set(currentBoardAtom, null);
    setStatus("idle");
  }

  static save = (
    elements: readonly ExcalidrawElement[],
    appState: AppState,
    files: BinaryFiles,
    onFilesSaved: () => void,
  ) => {
    if (this.isSavePaused()) {
      return;
    }
    if (this.lastSavedScene === null) {
      // The first onChange after opening reflects the board as loaded (after
      // restore normalization), so it becomes the baseline rather than being
      // saved back, which would needlessly bump the version on every open.
      this.lastSavedScene = serializeScene(elements, appState);
      return;
    }
    this.pending = { elements, appState, files, onFilesSaved };
    this._debouncedFlush();
  };

  /** Starts saving pending changes now (does not wait). */
  static flushSave = () => {
    this._debouncedFlush.flush();
  };

  /** Saves pending changes and the pending thumbnail, and waits for both. */
  static async flushAndWait() {
    this._debouncedFlush.flush();
    while (this.inFlight) {
      await this.inFlight;
    }
    await this.flushThumbnail();
  }

  /**
   * Queues a dashboard thumbnail of the scene. Rendering is throttled to once
   * per BOARDS_THUMBNAIL_INTERVAL; flushThumbnail() renders it right away.
   */
  static queueThumbnail(
    elements: readonly ExcalidrawElement[],
    appState: AppState,
    files: BinaryFiles,
  ) {
    if (!this.board) {
      return;
    }
    this.thumbnailJob = { boardId: this.board.id, elements, appState, files };
    if (this.thumbnailTimer) {
      return;
    }
    const delay = Math.max(
      0,
      this.lastThumbnailAt + BOARDS_THUMBNAIL_INTERVAL - Date.now(),
    );
    this.thumbnailTimer = setTimeout(() => {
      this.thumbnailTimer = null;
      this.renderQueuedThumbnail();
    }, delay);
  }

  static flushThumbnail(): Promise<void> {
    if (this.thumbnailTimer) {
      clearTimeout(this.thumbnailTimer);
      this.thumbnailTimer = null;
    }
    return this.renderQueuedThumbnail();
  }

  static isSavePaused = () => {
    return !this.board || !!this.blocked;
  };

  /** Whether closing the page now could lose work. */
  static hasUnsavedChanges = () => {
    if (!this.board) {
      return false;
    }
    if (this.inFlight || this.blocked || this.retryTimer) {
      return true;
    }
    return (
      !!this.pending &&
      serializeScene(this.pending.elements, this.pending.appState) !==
        this.lastSavedScene
    );
  };

  /** Resumes autosave after the user logged in again. */
  static resumeAfterLogin() {
    if (this.blocked === "unauthorized") {
      this.blocked = null;
      this.flush();
    }
  }

  /** Retries a failed save right away (e.g. when the browser comes online). */
  static retryNow() {
    if (this.retryTimer) {
      this.clearRetry();
      this.flush();
    }
  }

  static setBoardName(name: string) {
    const current = appJotaiStore.get(currentBoardAtom);
    if (current) {
      appJotaiStore.set(currentBoardAtom, { ...current, name });
    }
  }

  // ---------------------------------------------------------------------------

  /** Uploads run one after another, so an older thumbnail never wins. */
  private static renderQueuedThumbnail(): Promise<void> {
    const job = this.thumbnailJob;
    this.thumbnailJob = null;
    if (job) {
      this.lastThumbnailAt = Date.now();
      this.thumbnailInFlight = this.thumbnailInFlight.then(() =>
        uploadThumbnail(job).catch((error) => {
          console.warn("board thumbnail not updated", error);
        }),
      );
    }
    return this.thumbnailInFlight;
  }

  private static flush(): Promise<void> {
    if (!this.inFlight) {
      this.inFlight = this.drain().finally(() => {
        this.inFlight = null;
      });
    }
    return this.inFlight;
  }

  private static async drain() {
    // a scheduled retry owns the next attempt, so a failing server isn't
    // hammered in a loop
    while (this.pending && this.board && !this.blocked && !this.retryTimer) {
      const save = this.pending;
      this.pending = null;
      await this.persist(this.board, save);
    }
  }

  private static async persist(
    board: { id: string; version: number },
    save: PendingSave,
  ) {
    const scene = serializeScene(save.elements, save.appState);
    const fileStorage = this.fileStorage;
    try {
      const { erroredFiles } = await fileStorage.saveFiles({
        elements: save.elements,
        files: save.files,
      });
      save.onFilesSaved();

      if (scene !== this.lastSavedScene) {
        setStatus("saving");
        const res = await boardsApi.saveScene(board.id, board.version, scene);
        board.version = res.version;
        this.lastSavedScene = scene;
        appJotaiStore.set(boardsListVersionAtom, (v) => v + 1);
        this.queueThumbnail(save.elements, save.appState, save.files);
      }
      if (erroredFiles.size) {
        throw new Error(`failed to upload ${erroredFiles.size} image(s)`);
      }
      setStatus("saved");
    } catch (error) {
      if (error instanceof ConflictError) {
        this.blocked = "conflict";
        setStatus("conflict");
        return;
      }
      if (error instanceof NotFoundError) {
        // deleted from another tab or device
        this.blocked = "deleted";
        setStatus("deleted");
        return;
      }
      // keep the changes so they are saved on the next attempt
      this.pending ??= save;
      if (error instanceof UnauthorizedError) {
        this.blocked = "unauthorized";
        setStatus("unauthorized");
        const auth = appJotaiStore.get(authStateAtom);
        if (auth.status === "authenticated") {
          appJotaiStore.set(authStateAtom, {
            status: "expired",
            user: auth.user,
          });
        }
        return;
      }
      console.error(error);
      setStatus("error");
      this.scheduleRetry();
    }
  }

  private static scheduleRetry() {
    this.clearRetry();
    this.retryTimer = setTimeout(() => {
      this.retryTimer = null;
      this.flush();
    }, BOARDS_SAVE_RETRY_TIMEOUT);
  }

  private static clearRetry() {
    if (this.retryTimer) {
      clearTimeout(this.retryTimer);
      this.retryTimer = null;
    }
  }
}
