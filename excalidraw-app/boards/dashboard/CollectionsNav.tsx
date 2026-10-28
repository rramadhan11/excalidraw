import {
  DotsHorizontalIcon,
  gridIcon,
  LoadIcon,
  pencilIcon,
  PlusIcon,
  TrashIcon,
} from "@excalidraw/excalidraw/components/icons";
import clsx from "clsx";
import { useState } from "react";

import { boardsApi, UNSORTED } from "../api";
import { navigate } from "../routes";

import { InlineInput, usePopover } from "./ui";

import type { Collection, CollectionFilter } from "../api";

const goTo = (workspaceId: string, collection: CollectionFilter) =>
  navigate({ page: "dashboard", workspaceId, collection });

const CollectionItem = ({
  collection,
  active,
  onChanged,
  onError,
}: {
  collection: Collection;
  active: boolean;
  onChanged: () => void;
  onError: (message: string) => void;
}) => {
  const { open, setOpen, ref } = usePopover<HTMLLIElement>();
  const [mode, setMode] = useState<"view" | "rename" | "confirmDelete">("view");

  const run = async (action: () => Promise<void>) => {
    setMode("view");
    setOpen(false);
    try {
      await action();
      onChanged();
    } catch (error: any) {
      onError(error.message);
    }
  };

  if (mode === "rename") {
    return (
      <li className="boards-nav__row">
        <InlineInput
          label="Collection name"
          initialValue={collection.name}
          maxLength={100}
          onCancel={() => setMode("view")}
          onSubmit={(name) =>
            run(() =>
              boardsApi.renameCollection(collection.id, name).then(() => {}),
            )
          }
        />
      </li>
    );
  }

  return (
    <li className="boards-nav__row" ref={ref}>
      <button
        type="button"
        className={clsx("boards-nav__item", {
          "boards-nav__item--active": active,
        })}
        aria-current={active ? "page" : undefined}
        onClick={() => goTo(collection.workspaceId, collection.id)}
      >
        {LoadIcon}
        <span className="boards-nav__text">{collection.name}</span>
        <span className="boards-nav__count">{collection.boardCount}</span>
      </button>
      <button
        type="button"
        className="boards-nav__more"
        aria-label={`${collection.name} options`}
        onClick={() => setOpen(!open)}
      >
        {DotsHorizontalIcon}
      </button>
      {open && (
        <div className="boards-popover boards-nav__menu" role="menu">
          {mode === "confirmDelete" ? (
            <div className="boards-popover__confirm">
              <p>
                Delete “{collection.name}”? Its {collection.boardCount}{" "}
                {collection.boardCount === 1 ? "board moves" : "boards move"} to
                Unsorted.
              </p>
              <div className="boards-popover__actions">
                <button
                  type="button"
                  className="boards-dashboard__danger"
                  onClick={() =>
                    run(async () => {
                      await boardsApi.deleteCollection(collection.id);
                      if (active) {
                        await goTo(collection.workspaceId, null);
                      }
                    })
                  }
                >
                  Delete
                </button>
                <button type="button" onClick={() => setOpen(false)}>
                  Cancel
                </button>
              </div>
            </div>
          ) : (
            <>
              <button
                type="button"
                role="menuitem"
                className="boards-popover__item"
                onClick={() => {
                  setOpen(false);
                  setMode("rename");
                }}
              >
                {pencilIcon} Rename
              </button>
              <button
                type="button"
                role="menuitem"
                className="boards-popover__item boards-popover__item--danger"
                onClick={() => setMode("confirmDelete")}
              >
                {TrashIcon} Delete
              </button>
            </>
          )}
        </div>
      )}
    </li>
  );
};

export const CollectionsNav = ({
  workspaceId,
  collections,
  boardCount,
  active,
  onChanged,
  onError,
}: {
  workspaceId: string;
  collections: Collection[] | null;
  boardCount: number | undefined;
  active: CollectionFilter;
  onChanged: () => void;
  onError: (message: string) => void;
}) => {
  const [creating, setCreating] = useState(false);

  const create = async (name: string) => {
    setCreating(false);
    try {
      const collection = await boardsApi.createCollection(workspaceId, name);
      onChanged();
      await goTo(workspaceId, collection.id);
    } catch (error: any) {
      onError(error.message);
    }
  };

  return (
    <nav className="boards-nav" aria-label="Boards">
      <ul className="boards-nav__list">
        <li className="boards-nav__row">
          <button
            type="button"
            className={clsx("boards-nav__item", {
              "boards-nav__item--active": active === null,
            })}
            aria-current={active === null ? "page" : undefined}
            onClick={() => goTo(workspaceId, null)}
          >
            {gridIcon}
            <span className="boards-nav__text">All boards</span>
            {boardCount !== undefined && (
              <span className="boards-nav__count">{boardCount}</span>
            )}
          </button>
        </li>
        <li className="boards-nav__row">
          <button
            type="button"
            className={clsx("boards-nav__item", {
              "boards-nav__item--active": active === UNSORTED,
            })}
            aria-current={active === UNSORTED ? "page" : undefined}
            onClick={() => goTo(workspaceId, UNSORTED)}
          >
            {LoadIcon}
            <span className="boards-nav__text">Unsorted</span>
          </button>
        </li>
      </ul>

      <div className="boards-nav__header">
        <h2>Collections</h2>
        <button
          type="button"
          className="boards-nav__add"
          aria-label="New collection"
          title="New collection"
          onClick={() => setCreating(true)}
        >
          {PlusIcon}
        </button>
      </div>
      <ul className="boards-nav__list">
        {creating && (
          <li className="boards-nav__row">
            <InlineInput
              label="New collection name"
              placeholder="Collection name"
              maxLength={100}
              onSubmit={create}
              onCancel={() => setCreating(false)}
            />
          </li>
        )}
        {collections?.map((collection) => (
          <CollectionItem
            key={collection.id}
            collection={collection}
            active={active === collection.id}
            onChanged={onChanged}
            onError={onError}
          />
        ))}
        {collections?.length === 0 && !creating && (
          <li className="boards-nav__hint">
            Group boards into collections with +
          </li>
        )}
      </ul>
    </nav>
  );
};
