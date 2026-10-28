import { useEffect, useState } from "react";

import { boardsApi } from "../api";
import { getLastWorkspaceId, navigate, setLastWorkspaceId } from "../routes";

import { InlineInput } from "./ui";

import "./dashboard.scss";

/**
 * `/`: opens the last used workspace, or the first one. A user without any
 * workspace (e.g. after leaving their last shared one) can create one here.
 */
export const HomeRedirect = () => {
  const [state, setState] = useState<"loading" | "empty" | "creating">(
    "loading",
  );
  const [error, setError] = useState("");

  useEffect(() => {
    let cancelled = false;
    boardsApi
      .listWorkspaces()
      .then((workspaces) => {
        if (cancelled) {
          return;
        }
        const lastId = getLastWorkspaceId();
        const target =
          workspaces.find((workspace) => workspace.id === lastId) ??
          workspaces[0];
        if (target) {
          navigate(
            { page: "dashboard", workspaceId: target.id, collection: null },
            { replace: true },
          );
        } else {
          setLastWorkspaceId(null);
          setState("empty");
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
  }, []);

  const create = async (name: string) => {
    try {
      const workspace = await boardsApi.createWorkspace(name);
      navigate(
        { page: "dashboard", workspaceId: workspace.id, collection: null },
        { replace: true },
      );
    } catch (error: any) {
      setError(error.message);
      setState("empty");
    }
  };

  if (state === "loading" && !error) {
    return null;
  }
  return (
    <div className="boards-dashboard boards-dashboard--centered">
      <div className="boards-dashboard__empty-card">
        <h1>Welcome</h1>
        {error ? (
          <p className="boards-dashboard__error-text">{error}</p>
        ) : (
          <p>You don't have a workspace yet. Create one to start drawing.</p>
        )}
        {state === "creating" ? (
          <InlineInput
            label="Workspace name"
            placeholder="Workspace name"
            maxLength={100}
            onSubmit={create}
            onCancel={() => setState("empty")}
          />
        ) : (
          state === "empty" && (
            <button
              type="button"
              className="boards-dashboard__primary"
              onClick={() => setState("creating")}
            >
              Create workspace
            </button>
          )
        )}
      </div>
    </div>
  );
};
