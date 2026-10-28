import { CaptureUpdateAction } from "@excalidraw/excalidraw";
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

import { NotFoundError } from "./api";
import { BoardStorage } from "./BoardStorage";
import { navigate } from "./routes";
import { dashboardNoticeAtom, routeAtom, saveStatusAtom } from "./state";

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
 * Boards mode: loads the board named by the URL (`/board/:id`) into the editor,
 * and switches boards in place when the route changes to another board. No-op
 * outside boards mode.
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
  const route = useAtomValue(routeAtom);
  const routeBoardId = route?.page === "board" ? route.boardId : null;
  const isInitialLoadRef = useRef(true);

  useEffect(() => {
    if (!isBoardsMode() || !excalidrawAPI || !routeBoardId) {
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
      if (!cancelled && !board.thumbnailUpdatedAt && elements.length) {
        // boards from before thumbnails existed get one once opened
        BoardStorage.queueThumbnail(
          excalidrawAPI.getSceneElementsIncludingDeleted(),
          excalidrawAPI.getAppState(),
          excalidrawAPI.getFiles(),
        );
      }
    };

    load().catch((error) => {
      if (cancelled) {
        return;
      }
      if (error instanceof NotFoundError) {
        appJotaiStore.set(
          dashboardNoticeAtom,
          "That board doesn't exist, was deleted, or isn't shared with you.",
        );
        navigate({ page: "home" }, { replace: true });
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
};
