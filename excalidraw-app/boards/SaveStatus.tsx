import { DEFAULT_SIDEBAR } from "@excalidraw/common";
import { useExcalidrawAPI } from "@excalidraw/excalidraw";
import clsx from "clsx";

import { useAtomValue } from "../app-jotai";

import { currentBoardAtom, saveStatusAtom } from "./state";

import type { SaveStatus as SaveStatusType } from "./state";

export const BOARDS_SIDEBAR_TAB = "boards";

const LABELS: Record<SaveStatusType, string> = {
  idle: "",
  saved: "Saved",
  saving: "Saving…",
  error: "Not saved — retrying",
  conflict: "Changed elsewhere — reload",
  deleted: "Board was deleted",
  unauthorized: "Not saved — log in",
};

const HINTS: Partial<Record<SaveStatusType, string>> = {
  error:
    "The server couldn't be reached. Saving is retried automatically; keep this tab open.",
  conflict:
    "This board was saved from another tab or device. Autosave is paused so nothing gets overwritten. Export your changes if you need them, then reload the page.",
  deleted:
    "This board was deleted from another tab or device. Export your changes if you need them.",
};

/** Board name + save status, shown top right. Opens the boards sidebar. */
export const SaveStatus = () => {
  const excalidrawAPI = useExcalidrawAPI();
  const board = useAtomValue(currentBoardAtom);
  const status = useAtomValue(saveStatusAtom);

  return (
    <button
      type="button"
      className={clsx("boards-save-status", `boards-save-status--${status}`)}
      title={HINTS[status] ?? "Show boards"}
      onClick={() =>
        excalidrawAPI?.toggleSidebar({
          name: DEFAULT_SIDEBAR.name,
          tab: BOARDS_SIDEBAR_TAB,
          force: true,
        })
      }
    >
      <span className="boards-save-status__name">{board?.name ?? "…"}</span>
      {LABELS[status] && (
        <span className="boards-save-status__state">{LABELS[status]}</span>
      )}
    </button>
  );
};
