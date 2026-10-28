import { DEFAULT_SIDEBAR } from "@excalidraw/common";
import { useExcalidrawAPI } from "@excalidraw/excalidraw";
import { LoadIcon, loginIcon } from "@excalidraw/excalidraw/components/icons";
import { MainMenu } from "@excalidraw/excalidraw/index";

import { useAtomValue } from "../app-jotai";

import { boardsApi } from "./api";
import { BoardStorage } from "./BoardStorage";
import { BOARDS_SIDEBAR_TAB } from "./SaveStatus";
import { authStateAtom } from "./state";

/** Main menu entries replacing the Excalidraw+ links in boards mode. */
export const BoardsMenuItems = () => {
  const excalidrawAPI = useExcalidrawAPI();
  const auth = useAtomValue(authStateAtom);
  const user = auth.status === "authenticated" ? auth.user : null;

  return (
    <>
      <MainMenu.Item
        icon={LoadIcon}
        onSelect={() =>
          excalidrawAPI?.toggleSidebar({
            name: DEFAULT_SIDEBAR.name,
            tab: BOARDS_SIDEBAR_TAB,
            force: true,
          })
        }
      >
        Boards
      </MainMenu.Item>
      <MainMenu.Item
        icon={loginIcon}
        title={user ? `Logged in as ${user.email}` : undefined}
        onSelect={async () => {
          await BoardStorage.flushAndWait();
          if (
            BoardStorage.hasUnsavedChanges() &&
            !window.confirm(
              "Changes on this board could not be saved and will be lost. Log out anyway?",
            )
          ) {
            return;
          }
          await boardsApi.logout();
          window.location.assign("/");
        }}
      >
        Log out{user ? ` (${user.name})` : ""}
      </MainMenu.Item>
    </>
  );
};
