import {
  loginIcon,
  PenModeIcon,
} from "@excalidraw/excalidraw/components/icons";
import { useCallback, useEffect, useState } from "react";

import { useAtom, useAtomValue } from "../../app-jotai";
import { useHandleAppTheme } from "../../useHandleAppTheme";
import { boardsApi, NotFoundError, UNSORTED } from "../api";
import { navigate, setLastWorkspaceId } from "../routes";
import { authStateAtom, dashboardNoticeAtom } from "../state";

import { BoardCard } from "./BoardCard";
import { CollectionsNav } from "./CollectionsNav";
import { WorkspaceSwitcher } from "./WorkspaceSwitcher";

import "./dashboard.scss";

import type {
  BoardSummary,
  Collection,
  CollectionFilter,
  Workspace,
} from "../api";

const RECENT_COUNT = 4;

export const Dashboard = ({
  workspaceId,
  collection,
}: {
  workspaceId: string;
  collection: CollectionFilter;
}) => {
  const auth = useAtomValue(authStateAtom);
  const user = auth.status === "authenticated" ? auth.user : null;
  const { editorTheme } = useHandleAppTheme();
  const [notice, setNotice] = useAtom(dashboardNoticeAtom);
  const [error, setError] = useState("");

  const [workspaces, setWorkspaces] = useState<Workspace[]>([]);
  const [collections, setCollections] = useState<Collection[] | null>(null);
  const [boards, setBoards] = useState<BoardSummary[] | null>(null);
  const [nextCursor, setNextCursor] = useState("");
  const [loadingMore, setLoadingMore] = useState(false);
  // bumped after any change, so every list reloads
  const [reloadKey, setReloadKey] = useState(0);
  const reload = useCallback(() => setReloadKey((key) => key + 1), []);

  useEffect(() => {
    let cancelled = false;
    boardsApi
      .listWorkspaces()
      .then((list) => {
        if (cancelled) {
          return;
        }
        setWorkspaces(list);
        if (list.some((workspace) => workspace.id === workspaceId)) {
          setLastWorkspaceId(workspaceId);
        } else {
          setLastWorkspaceId(null);
          setNotice(
            "That workspace doesn't exist or you're not a member of it.",
          );
          navigate({ page: "home" }, { replace: true });
        }
      })
      .catch((error) => !cancelled && setError(error.message));
    return () => {
      cancelled = true;
    };
  }, [workspaceId, reloadKey, setNotice]);

  useEffect(() => {
    let cancelled = false;
    boardsApi
      .listCollections(workspaceId)
      .then((list) => !cancelled && setCollections(list))
      .catch((error) => {
        // an unknown workspace is handled by the workspaces effect
        if (!cancelled && !(error instanceof NotFoundError)) {
          setError(error.message);
        }
      });
    return () => {
      cancelled = true;
    };
  }, [workspaceId, reloadKey]);

  // a different list starts from its own first page
  useEffect(() => {
    setBoards(null);
  }, [workspaceId, collection]);

  useEffect(() => {
    let cancelled = false;
    boardsApi
      .listBoards(workspaceId, { collection })
      .then((page) => {
        if (!cancelled) {
          setBoards(page.boards);
          setNextCursor(page.nextCursor);
        }
      })
      .catch((error) => {
        if (!cancelled && !(error instanceof NotFoundError)) {
          setError(error.message);
        }
      });
    return () => {
      cancelled = true;
    };
  }, [workspaceId, collection, reloadKey]);

  const loadMore = async () => {
    setLoadingMore(true);
    try {
      const page = await boardsApi.listBoards(workspaceId, {
        collection,
        cursor: nextCursor,
      });
      setBoards((current) => [...(current ?? []), ...page.boards]);
      setNextCursor(page.nextCursor);
    } catch (error: any) {
      setError(error.message);
    } finally {
      setLoadingMore(false);
    }
  };

  const startDrawing = async () => {
    try {
      const board = await boardsApi.createBoard(workspaceId, {
        collectionId: collection && collection !== UNSORTED ? collection : null,
      });
      await navigate({ page: "board", boardId: board.id });
    } catch (error: any) {
      setError(error.message);
    }
  };

  const logout = async () => {
    await boardsApi.logout();
    window.location.assign("/");
  };

  const currentWorkspace = workspaces.find(
    (workspace) => workspace.id === workspaceId,
  );
  const title =
    collection === null
      ? "All boards"
      : collection === UNSORTED
      ? "Unsorted"
      : collections?.find((c) => c.id === collection)?.name ?? "Collection";

  const renderGrid = (list: BoardSummary[]) => (
    <div className="boards-grid">
      {list.map((board) => (
        <BoardCard
          key={`${board.id}:${board.updatedAt}:${board.collectionId}`}
          board={board}
          collections={collections ?? []}
          onChanged={reload}
          onError={setError}
        />
      ))}
    </div>
  );

  return (
    <div className="boards-dashboard" data-theme={editorTheme}>
      <aside className="boards-dashboard__sidebar">
        <WorkspaceSwitcher
          workspaces={workspaces}
          current={currentWorkspace}
          onChanged={reload}
          onError={setError}
        />
        <CollectionsNav
          workspaceId={workspaceId}
          collections={collections}
          boardCount={currentWorkspace?.boardCount}
          active={collection}
          onChanged={reload}
          onError={setError}
        />
        <div className="boards-dashboard__user">
          <span className="boards-dashboard__user-name" title={user?.email}>
            {user?.name}
          </span>
          <button
            type="button"
            className="boards-dashboard__icon-button"
            onClick={logout}
            title="Log out"
            aria-label="Log out"
          >
            {loginIcon}
          </button>
        </div>
      </aside>

      <main className="boards-dashboard__main">
        <header className="boards-dashboard__header">
          <h1>{title}</h1>
          <button
            type="button"
            className="boards-dashboard__primary"
            onClick={startDrawing}
          >
            {PenModeIcon} Start drawing
          </button>
        </header>

        {notice && (
          <div className="boards-dashboard__banner" role="status">
            {notice}
            <button type="button" onClick={() => setNotice(null)}>
              Dismiss
            </button>
          </div>
        )}
        {error && (
          <div
            className="boards-dashboard__banner boards-dashboard__banner--error"
            role="alert"
          >
            {error}
            <button type="button" onClick={() => setError("")}>
              Dismiss
            </button>
          </div>
        )}

        {boards === null ? (
          <p className="boards-dashboard__muted">Loading…</p>
        ) : boards.length === 0 ? (
          <div className="boards-dashboard__empty">
            <p>No boards here yet.</p>
            <button
              type="button"
              className="boards-dashboard__primary"
              onClick={startDrawing}
            >
              Start drawing
            </button>
          </div>
        ) : (
          <>
            {collection === null && boards.length > RECENT_COUNT && (
              <section className="boards-dashboard__section">
                <h2>Recently modified</h2>
                {renderGrid(boards.slice(0, RECENT_COUNT))}
              </section>
            )}
            <section className="boards-dashboard__section">
              {collection === null && boards.length > RECENT_COUNT && (
                <h2>All boards</h2>
              )}
              {renderGrid(boards)}
            </section>
            {nextCursor && (
              <button
                type="button"
                className="boards-dashboard__secondary boards-dashboard__more"
                disabled={loadingMore}
                onClick={loadMore}
              >
                {loadingMore ? "Loading…" : "Load more"}
              </button>
            )}
          </>
        )}
      </main>
    </div>
  );
};
