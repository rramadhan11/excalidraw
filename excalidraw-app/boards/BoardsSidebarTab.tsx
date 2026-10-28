import clsx from "clsx";
import { useEffect, useState } from "react";

import { useAtomValue } from "../app-jotai";

import { boardsApi } from "./api";
import { BoardStorage } from "./BoardStorage";
import { navigateToBoard } from "./routes";
import { boardsListVersionAtom, currentBoardAtom } from "./state";

import type { BoardSummary } from "./api";

const formatUpdatedAt = (iso: string) =>
  new Date(iso).toLocaleString(undefined, {
    dateStyle: "medium",
    timeStyle: "short",
  });

const BoardRow = ({
  board,
  isCurrent,
  onChanged,
  onError,
}: {
  board: BoardSummary;
  isCurrent: boolean;
  onChanged: () => void;
  onError: (message: string) => void;
}) => {
  const [mode, setMode] = useState<"view" | "rename" | "confirmDelete">("view");
  const [name, setName] = useState(board.name);

  const rename = async () => {
    const nextName = name.trim();
    setMode("view");
    if (!nextName || nextName === board.name) {
      setName(board.name);
      return;
    }
    try {
      await boardsApi.renameBoard(board.id, nextName);
      if (isCurrent) {
        BoardStorage.setBoardName(nextName);
      }
      onChanged();
    } catch (error: any) {
      setName(board.name);
      onError(error.message);
    }
  };

  const remove = async () => {
    setMode("view");
    try {
      await boardsApi.deleteBoard(board.id);
      onChanged();
      if (isCurrent) {
        // the board is gone, so its unsaved changes can't be saved anywhere
        BoardStorage.close();
        await navigateToBoard(null);
      }
    } catch (error: any) {
      onError(error.message);
    }
  };

  if (mode === "rename") {
    return (
      <li className="boards-list__item boards-list__item--editing">
        <input
          className="boards-list__rename"
          autoFocus
          maxLength={200}
          value={name}
          onChange={(event) => setName(event.target.value)}
          onBlur={rename}
          onKeyDown={(event) => {
            if (event.key === "Enter") {
              rename();
            } else if (event.key === "Escape") {
              setName(board.name);
              setMode("view");
            }
          }}
        />
      </li>
    );
  }

  if (mode === "confirmDelete") {
    return (
      <li className="boards-list__item boards-list__item--danger">
        <span className="boards-list__confirm">
          Delete “{board.name}” and its images?
        </span>
        <button type="button" onClick={remove}>
          Delete
        </button>
        <button type="button" onClick={() => setMode("view")}>
          Cancel
        </button>
      </li>
    );
  }

  return (
    <li
      className={clsx("boards-list__item", {
        "boards-list__item--current": isCurrent,
      })}
    >
      <button
        type="button"
        className="boards-list__open"
        onClick={() => navigateToBoard(board.id)}
      >
        <span className="boards-list__name">{board.name}</span>
        <span className="boards-list__meta">
          {formatUpdatedAt(board.updatedAt)}
        </span>
      </button>
      <button
        type="button"
        className="boards-list__action"
        title="Rename"
        onClick={() => setMode("rename")}
      >
        Rename
      </button>
      <button
        type="button"
        className="boards-list__action"
        title="Delete"
        onClick={() => setMode("confirmDelete")}
      >
        Delete
      </button>
    </li>
  );
};

export const BoardsSidebarTab = () => {
  const currentBoard = useAtomValue(currentBoardAtom);
  const listVersion = useAtomValue(boardsListVersionAtom);
  const [boards, setBoards] = useState<BoardSummary[] | null>(null);
  const [error, setError] = useState("");
  const [refreshKey, setRefreshKey] = useState(0);

  useEffect(() => {
    let cancelled = false;
    boardsApi
      .listBoards()
      .then((boards) => {
        if (!cancelled) {
          setBoards(boards);
        }
      })
      .catch((error) => {
        if (!cancelled) {
          setError(error.message);
        }
      });
    return () => {
      cancelled = true;
    };
  }, [listVersion, refreshKey]);

  const refresh = () => setRefreshKey((key) => key + 1);

  const createBoard = async () => {
    try {
      const board = await boardsApi.createBoard();
      refresh();
      await navigateToBoard(board.id);
    } catch (error: any) {
      setError(error.message);
    }
  };

  return (
    <div className="boards-sidebar">
      <div className="boards-sidebar__header">
        <h2 className="boards-sidebar__title">Boards</h2>
        <button
          type="button"
          className="boards-sidebar__new"
          onClick={createBoard}
        >
          + New board
        </button>
      </div>
      {error && (
        <div className="boards-sidebar__error" role="alert">
          {error}
          <button type="button" onClick={() => setError("")}>
            Dismiss
          </button>
        </div>
      )}
      {boards === null ? (
        <div className="boards-sidebar__empty">Loading…</div>
      ) : (
        <ul className="boards-list">
          {boards.map((board) => (
            <BoardRow
              key={`${board.id}:${board.name}`}
              board={board}
              isCurrent={board.id === currentBoard?.id}
              onChanged={refresh}
              onError={setError}
            />
          ))}
        </ul>
      )}
    </div>
  );
};
