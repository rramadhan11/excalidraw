import { exportScene, THEME } from "@excalidraw/excalidraw";
import { cleanAppStateForExport } from "@excalidraw/excalidraw/appState";

import type { NonDeletedExcalidrawElement } from "@excalidraw/element/types";
import type { AppState, BinaryFiles } from "@excalidraw/excalidraw/types";

const THUMBNAIL_MAX_SIZE = 640;

/**
 * Renders a board preview for the dashboard. Always rendered light; dark mode
 * inverts it with the same filter the editor uses for the canvas.
 */
export const renderThumbnail = (
  elements: readonly NonDeletedExcalidrawElement[],
  appState: AppState,
  files: BinaryFiles,
): Promise<Blob> =>
  exportScene({
    type: "jpeg",
    data: { elements, appState: cleanAppStateForExport(appState), files },
    config: {
      maxWidthOrHeight: THUMBNAIL_MAX_SIZE,
      quality: 0.8,
      theme: THEME.LIGHT,
      padding: 24,
    },
  });
