import {
  checkIcon,
  DotsHorizontalIcon,
  LoadIcon,
  pencilIcon,
  TrashIcon,
} from "@excalidraw/excalidraw/components/icons";
import { useState } from "react";

import { boardsApi, thumbnailsApi } from "../api";
import { navigate } from "../routes";

import { formatRelative, InlineInput, usePopover } from "./ui";

import type { BoardSummary, Collection } from "../api";

type MenuMode = "main" | "move" | "confirmDelete";

export const BoardCard = ({
  board,
  collections,
  onChanged,
  onError,
}: {
  board: BoardSummary;
  collections: Collection[];
  onChanged: () => void;
  onError: (message: string) => void;
}) => {
  const { open, setOpen, ref } = usePopover<HTMLElement>();
  const [menu, setMenu] = useState<MenuMode>("main");
  const [renaming, setRenaming] = useState(false);
  const thumbnailUrl = thumbnailsApi.url(board);

  const openBoard = () => navigate({ page: "board", boardId: board.id });

  const run = async (action: () => Promise<unknown>) => {
    setOpen(false);
    setMenu("main");
    try {
      await action();
      onChanged();
    } catch (error: any) {
      onError(error.message);
    }
  };

  return (
    <article className="board-card" ref={ref}>
      <button
        type="button"
        className="board-card__preview"
        aria-label={`Open ${board.name}`}
        onClick={openBoard}
      >
        {thumbnailUrl ? (
          <img src={thumbnailUrl} alt="" loading="lazy" />
        ) : (
          <span className="board-card__placeholder">No preview yet</span>
        )}
        <span className="board-card__badge">
          {formatRelative(board.updatedAt)}
        </span>
      </button>

      <div className="board-card__info">
        <div className="board-card__text">
          {renaming ? (
            <InlineInput
              label="Board name"
              initialValue={board.name}
              maxLength={200}
              onCancel={() => setRenaming(false)}
              onSubmit={(name) => {
                setRenaming(false);
                run(() => boardsApi.updateBoard(board.id, { name }));
              }}
            />
          ) : (
            <button
              type="button"
              className="board-card__name"
              title={board.name}
              onClick={openBoard}
            >
              {board.name}
            </button>
          )}
          {board.createdBy && (
            <span className="board-card__meta">by {board.createdBy.name}</span>
          )}
        </div>
        <button
          type="button"
          className="board-card__more"
          aria-label={`${board.name} options`}
          aria-haspopup="menu"
          aria-expanded={open}
          onClick={() => {
            setMenu("main");
            setOpen(!open);
          }}
        >
          {DotsHorizontalIcon}
        </button>
      </div>

      {open && (
        <div className="boards-popover board-card__menu" role="menu">
          {menu === "main" && (
            <>
              <button
                type="button"
                role="menuitem"
                className="boards-popover__item"
                onClick={() => {
                  setOpen(false);
                  setRenaming(true);
                }}
              >
                {pencilIcon} Rename
              </button>
              <button
                type="button"
                role="menuitem"
                className="boards-popover__item"
                onClick={() => setMenu("move")}
              >
                {LoadIcon} Move to…
              </button>
              <button
                type="button"
                role="menuitem"
                className="boards-popover__item boards-popover__item--danger"
                onClick={() => setMenu("confirmDelete")}
              >
                {TrashIcon} Delete
              </button>
            </>
          )}

          {menu === "move" && (
            <>
              <div className="boards-popover__label">Move to</div>
              {[
                { id: null, name: "Unsorted" },
                ...collections.map((c) => ({ id: c.id, name: c.name })),
              ].map((target) => (
                <button
                  key={target.id ?? "unsorted"}
                  type="button"
                  role="menuitem"
                  className="boards-popover__item"
                  disabled={target.id === board.collectionId}
                  onClick={() =>
                    run(() =>
                      boardsApi.updateBoard(board.id, {
                        collectionId: target.id,
                      }),
                    )
                  }
                >
                  <span className="boards-popover__text">{target.name}</span>
                  {target.id === board.collectionId && checkIcon}
                </button>
              ))}
            </>
          )}

          {menu === "confirmDelete" && (
            <div className="boards-popover__confirm">
              <p>Delete “{board.name}” and its images? This can't be undone.</p>
              <div className="boards-popover__actions">
                <button
                  type="button"
                  className="boards-dashboard__danger"
                  onClick={() => run(() => boardsApi.deleteBoard(board.id))}
                >
                  Delete
                </button>
                <button type="button" onClick={() => setOpen(false)}>
                  Cancel
                </button>
              </div>
            </div>
          )}
        </div>
      )}
    </article>
  );
};
