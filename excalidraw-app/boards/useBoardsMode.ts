import { CaptureUpdateAction } from "@excalidraw/excalidraw";
import { EVENT, preventUnload } from "@excalidraw/common";
import { isInitializedImageElement } from "@excalidraw/element";
import {
  restoreAppState,
  restoreElements,
} from "@excalidraw/excalidraw/data/restore";
import { useEffect, useRef } from "react";

import type { FileId } from "@excalidraw/element/types";
import type { RestoredDataState } from "@excalidraw/excalidraw/data/restore";
import type {
  ExcalidrawImperativeAPI,
  ExcalidrawInitialDataState,
} from "@excalidraw/excalidraw/types";
import type { ResolvablePromise } from "@excalidraw/common/utils";

import { appJotaiStore, useAtomValue } from "../app-jotai";
import { isBoardsMode } from "../app_constants";
import { updateStaleImageStatuses } from "../data/FileManager";

import { boardsApi, NotFoundError } from "./api";
import { BoardStorage } from "./BoardStorage";
import { boardIdFromPath, replaceBoardRoute } from "./routes";
import { boardRouteAtom, saveStatusAtom } from "./state";

/** The most recently updated board, or a new one if the user has none. */
const getDefaultBoardId = async () => {
  const [latest] = await boardsApi.listBoards();
  return latest ? latest.id : (await boardsApi.createBoard()).id;
};

const loadBoardImages = async (
  excalidrawAPI: ExcalidrawImperativeAPI,
  elements: ExcalidrawInitialDataState["elements"],
) => {
  const fileIds = (elements ?? []).reduce((acc, element) => {
    if (isInitializedImageElement(element)) {
      acc.push(element.fileId);
    }
    return acc;
  }, [] as FileId[]);
  if (!fileIds.length) {
    return;
  }
  const { loadedFiles, erroredFiles } = await BoardStorage.fileStorage.getFiles(
    fileIds,
  );
  if (loadedFiles.length) {
    excalidrawAPI.addFiles(loadedFiles);
  }
  updateStaleImageStatuses({
    excalidrawAPI,
    erroredFiles,
    elements: excalidrawAPI.getSceneElementsIncludingDeleted(),
  });
};

/**
 * Boards mode: opens the board named by the URL (`/board/:id`, or the latest
 * board on `/`), keeps it in sync with browser navigation, and saves pending
 * changes when the page is hidden or closed. No-op outside boards mode.
 */
export const useBoardsMode = ({
  excalidrawAPI,
  initialStatePromise,
  setErrorMessage,
}: {
  excalidrawAPI: ExcalidrawImperativeAPI | null;
  initialStatePromise: ResolvablePromise<ExcalidrawInitialDataState | null>;
  setErrorMessage: (message: string) => void;
}) => {
  const routeBoardId = useAtomValue(boardRouteAtom);
  const isInitialLoadRef = useRef(true);

  useEffect(() => {
    if (!isBoardsMode()) {
      return;
    }
    appJotaiStore.set(
      boardRouteAtom,
      boardIdFromPath(window.location.pathname),
    );
    const onPopState = () => {
      appJotaiStore.set(
        boardRouteAtom,
        boardIdFromPath(window.location.pathname),
      );
    };
    window.addEventListener("popstate", onPopState);
    return () => window.removeEventListener("popstate", onPopState);
  }, []);

  useEffect(() => {
    if (!isBoardsMode() || !excalidrawAPI || routeBoardId === undefined) {
      return;
    }
    let cancelled = false;

    /** `scene` is null when the board failed to load */
    const finishLoading = (scene: Omit<RestoredDataState, "files"> | null) => {
      if (isInitialLoadRef.current) {
        isInitialLoadRef.current = false;
        initialStatePromise.resolve(
          scene
            ? { ...scene, scrollToContent: true }
            : { appState: { isLoading: false } },
        );
      } else if (!scene) {
        excalidrawAPI.updateScene({ appState: { isLoading: false } });
      } else {
        excalidrawAPI.updateScene({
          elements: scene.elements,
          appState: { ...scene.appState, isLoading: false },
          captureUpdate: CaptureUpdateAction.NEVER,
        });
        // undo must not cross into the previous board
        excalidrawAPI.history.clear();
        const elements = excalidrawAPI.getSceneElements();
        if (elements.length) {
          excalidrawAPI.setViewport({ target: elements, fit: "none" });
        }
      }
    };

    const load = async () => {
      if (!routeBoardId) {
        // `/` resolves to a board; the route change re-runs this effect
        replaceBoardRoute(await getDefaultBoardId());
        return;
      }
      if (!isInitialLoadRef.current) {
        excalidrawAPI.updateScene({ appState: { isLoading: true } });
      }
      const board = await BoardStorage.open(routeBoardId);
      if (cancelled) {
        return;
      }
      const elements = restoreElements(board.scene.elements, null, {
        repairBindings: true,
        deleteInvisibleElements: true,
      });
      const appState = {
        ...restoreAppState(
          board.scene.appState,
          isInitialLoadRef.current ? null : excalidrawAPI.getAppState(),
        ),
        // used as the file name when exporting
        name: board.name,
      };
      finishLoading({ elements, appState });
      await loadBoardImages(excalidrawAPI, elements);
    };

    load().catch((error) => {
      if (cancelled) {
        return;
      }
      if (error instanceof NotFoundError) {
        setErrorMessage("This board doesn't exist or was deleted.");
        replaceBoardRoute(null);
        return;
      }
      console.error(error);
      appJotaiStore.set(saveStatusAtom, "error");
      setErrorMessage(
        `Couldn't open the board: ${error.message}. Changes are not saved until it loads — reload the page to retry.`,
      );
      finishLoading(null);
    });

    return () => {
      cancelled = true;
    };
  }, [excalidrawAPI, routeBoardId, initialStatePromise, setErrorMessage]);

  useEffect(() => {
    if (!isBoardsMode()) {
      return;
    }
    const onVisibilityChange = () => {
      if (document.hidden) {
        BoardStorage.flushSave();
      }
    };
    const onBlur = () => BoardStorage.flushSave();
    const onBeforeUnload = (event: BeforeUnloadEvent) => {
      BoardStorage.flushSave();
      if (BoardStorage.hasUnsavedChanges()) {
        preventUnload(event);
      }
    };
    const onOnline = () => BoardStorage.retryNow();

    window.addEventListener(EVENT.BLUR, onBlur);
    document.addEventListener(EVENT.VISIBILITY_CHANGE, onVisibilityChange);
    window.addEventListener(EVENT.BEFORE_UNLOAD, onBeforeUnload);
    window.addEventListener("online", onOnline);
    return () => {
      window.removeEventListener(EVENT.BLUR, onBlur);
      document.removeEventListener(EVENT.VISIBILITY_CHANGE, onVisibilityChange);
      window.removeEventListener(EVENT.BEFORE_UNLOAD, onBeforeUnload);
      window.removeEventListener("online", onOnline);
    };
  }, []);
};
