import { EVENT, preventUnload } from "@excalidraw/common";
import { useEffect } from "react";

import { appJotaiStore, useAtomValue } from "../app-jotai";

import { BoardStorage } from "./BoardStorage";
import { Dashboard } from "./dashboard/Dashboard";
import { HomeRedirect } from "./dashboard/HomeRedirect";
import { handlePopState, parseRoute } from "./routes";
import { routeAtom } from "./state";

/**
 * Boards mode: renders the page for the current URL (dashboard or editor) and
 * guards unsaved board changes for the whole app lifetime.
 */
export const BoardsRouter = ({ editor }: { editor: React.ReactNode }) => {
  const route = useAtomValue(routeAtom);

  useEffect(() => {
    appJotaiStore.set(routeAtom, parseRoute(window.location));
    const onPopState = () => {
      handlePopState();
    };
    window.addEventListener("popstate", onPopState);
    return () => window.removeEventListener("popstate", onPopState);
  }, []);

  useEffect(() => {
    const flush = () => {
      BoardStorage.flushSave();
      BoardStorage.flushThumbnail();
    };
    const onVisibilityChange = () => {
      if (document.hidden) {
        flush();
      }
    };
    const onBeforeUnload = (event: BeforeUnloadEvent) => {
      BoardStorage.flushSave();
      if (BoardStorage.hasUnsavedChanges()) {
        preventUnload(event);
      }
    };
    const onOnline = () => BoardStorage.retryNow();

    window.addEventListener(EVENT.BLUR, flush);
    document.addEventListener(EVENT.VISIBILITY_CHANGE, onVisibilityChange);
    window.addEventListener(EVENT.BEFORE_UNLOAD, onBeforeUnload);
    window.addEventListener("online", onOnline);
    return () => {
      window.removeEventListener(EVENT.BLUR, flush);
      document.removeEventListener(EVENT.VISIBILITY_CHANGE, onVisibilityChange);
      window.removeEventListener(EVENT.BEFORE_UNLOAD, onBeforeUnload);
      window.removeEventListener("online", onOnline);
    };
  }, []);

  switch (route?.page) {
    case undefined:
      return null;
    case "home":
      return <HomeRedirect />;
    case "dashboard":
      return (
        <Dashboard
          workspaceId={route.workspaceId}
          collection={route.collection}
        />
      );
    case "board":
      return <>{editor}</>;
  }
};
