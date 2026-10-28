import {
  checkIcon,
  chevronDownIcon,
  pencilIcon,
  PlusIcon,
  TrashIcon,
} from "@excalidraw/excalidraw/components/icons";
import { useState } from "react";

import { boardsApi } from "../api";
import { navigate, setLastWorkspaceId } from "../routes";

import { InlineInput, usePopover } from "./ui";

import type { Workspace } from "../api";

type Mode = "list" | "create" | "rename" | "confirmDelete";

export const WorkspaceSwitcher = ({
  workspaces,
  current,
  onChanged,
  onError,
}: {
  workspaces: Workspace[];
  current: Workspace | undefined;
  onChanged: () => void;
  onError: (message: string) => void;
}) => {
  const { open, setOpen, ref } = usePopover();
  const [mode, setMode] = useState<Mode>("list");

  const close = () => {
    setOpen(false);
    setMode("list");
  };

  const run = async (action: () => Promise<void>) => {
    close();
    try {
      await action();
    } catch (error: any) {
      onError(error.message);
    }
  };

  const isOwner = current?.role === "owner";

  return (
    <div className="boards-ws" ref={ref}>
      <button
        type="button"
        className="boards-ws__current"
        aria-haspopup="menu"
        aria-expanded={open}
        onClick={() => (open ? close() : setOpen(true))}
      >
        <span className="boards-ws__avatar" aria-hidden="true">
          {(current?.name ?? "?").slice(0, 1).toUpperCase()}
        </span>
        <span className="boards-ws__name">{current?.name ?? "…"}</span>
        <span className="boards-ws__chevron">{chevronDownIcon}</span>
      </button>

      {open && (
        <div className="boards-popover boards-ws__menu" role="menu">
          {mode === "list" && (
            <>
              <div className="boards-popover__label">Workspaces</div>
              {workspaces.map((workspace) => (
                <button
                  key={workspace.id}
                  type="button"
                  role="menuitem"
                  className="boards-popover__item"
                  onClick={() =>
                    run(async () => {
                      await navigate({
                        page: "dashboard",
                        workspaceId: workspace.id,
                        collection: null,
                      });
                    })
                  }
                >
                  <span className="boards-popover__text">{workspace.name}</span>
                  {workspace.id === current?.id && checkIcon}
                </button>
              ))}
              <div className="boards-popover__separator" />
              <button
                type="button"
                role="menuitem"
                className="boards-popover__item"
                onClick={() => setMode("create")}
              >
                {PlusIcon} Create workspace
              </button>
              {isOwner && (
                <>
                  <button
                    type="button"
                    role="menuitem"
                    className="boards-popover__item"
                    onClick={() => setMode("rename")}
                  >
                    {pencilIcon} Rename workspace
                  </button>
                  <button
                    type="button"
                    role="menuitem"
                    className="boards-popover__item boards-popover__item--danger"
                    onClick={() => setMode("confirmDelete")}
                  >
                    {TrashIcon} Delete workspace
                  </button>
                </>
              )}
            </>
          )}

          {mode === "create" && (
            <InlineInput
              label="New workspace name"
              placeholder="Workspace name"
              maxLength={100}
              onCancel={close}
              onSubmit={(name) =>
                run(async () => {
                  const workspace = await boardsApi.createWorkspace(name);
                  onChanged();
                  await navigate({
                    page: "dashboard",
                    workspaceId: workspace.id,
                    collection: null,
                  });
                })
              }
            />
          )}

          {mode === "rename" && current && (
            <InlineInput
              label="Workspace name"
              initialValue={current.name}
              maxLength={100}
              onCancel={close}
              onSubmit={(name) =>
                run(async () => {
                  await boardsApi.renameWorkspace(current.id, name);
                  onChanged();
                })
              }
            />
          )}

          {mode === "confirmDelete" && current && (
            <div className="boards-popover__confirm">
              <p>
                Delete “{current.name}”? Only a workspace without boards can be
                deleted.
              </p>
              <div className="boards-popover__actions">
                <button
                  type="button"
                  className="boards-dashboard__danger"
                  onClick={() =>
                    run(async () => {
                      await boardsApi.deleteWorkspace(current.id);
                      setLastWorkspaceId(null);
                      await navigate({ page: "home" }, { replace: true });
                    })
                  }
                >
                  Delete
                </button>
                <button type="button" onClick={close}>
                  Cancel
                </button>
              </div>
            </div>
          )}
        </div>
      )}
    </div>
  );
};
