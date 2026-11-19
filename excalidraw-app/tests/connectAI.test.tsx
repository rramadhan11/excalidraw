import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { vi } from "vitest";

import ExcalidrawApp from "../App";
import { appJotaiStore } from "../app-jotai";
import { BoardStorage } from "../boards/BoardStorage";
import { authStateAtom, dashboardNoticeAtom, routeAtom } from "../boards/state";

import { FakeBoardsServer, WORKSPACE_ID } from "./helpers/fakeBoardsServer";

const clipboard = vi.hoisted(() => ({ copied: [] as string[] }));
vi.mock("@excalidraw/excalidraw/clipboard", async (importOriginal) => ({
  ...(await importOriginal<object>()),
  copyTextToSystemClipboard: vi.fn(async (text: string) => {
    clipboard.copied.push(text);
  }),
}));

let server: FakeBoardsServer;

const openDialog = async () => {
  fireEvent.click(
    await screen.findByRole("button", { name: "Connect AI tools" }),
  );
  return screen.findByRole("dialog", { name: "Connect AI tools" });
};

const fill = (label: string, value: string) =>
  fireEvent.change(screen.getByLabelText(label), { target: { value } });

const createToken = (opts: { name?: string; password?: string } = {}) => {
  if (opts.name) {
    fill("Name", opts.name);
  }
  fill("Your password", opts.password ?? server.password);
  fireEvent.click(screen.getByRole("button", { name: "Create token" }));
};

describe("boards mode: connect AI tools", () => {
  beforeAll(() => {
    vi.stubEnv("VITE_APP_BOARDS_API", "/api");
  });

  afterAll(() => {
    vi.unstubAllEnvs();
  });

  beforeEach(async () => {
    server = new FakeBoardsServer();
    clipboard.copied = [];
    vi.stubGlobal("fetch", server.fetch);
    BoardStorage.close();
    localStorage.clear();
    appJotaiStore.set(authStateAtom, { status: "loading" });
    appJotaiStore.set(routeAtom, undefined);
    appJotaiStore.set(dashboardNoticeAtom, null);
    window.history.replaceState(null, "", `/w/${WORKSPACE_ID}`);
    render(<ExcalidrawApp />);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("creates a token and shows client setup with it, only once", async () => {
    await openDialog();
    expect(await screen.findByText("No tokens yet.")).toBeTruthy();

    createToken({ name: "My laptop" });

    await screen.findByText(/Copy it now/);
    const [token] = server.tokens;
    expect(token.name).toBe("My laptop");
    // 90 days by default
    expect(
      Math.abs(
        new Date(token.expiresAt!).getTime() - (Date.now() + 90 * 86_400_000),
      ),
    ).toBeLessThan(60_000);

    const secret = `exa_secret-${token.id}`;
    const command = `claude mcp add --transport http excalidraw ${window.location.origin}/api/mcp --header "Authorization: Bearer ${secret}"`;
    expect(screen.getByText(command)).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Copy Claude Code" }));
    await waitFor(() => expect(clipboard.copied).toEqual([command]));
    expect(
      screen.getByRole("button", { name: "Copy Claude Code" }).textContent,
    ).toBe("Copied");
    // the password field is cleared
    fireEvent.click(screen.getByRole("button", { name: "Done" }));
    expect(
      (screen.getByLabelText("Your password") as HTMLInputElement).value,
    ).toBe("");

    // the secret is gone after closing, the token stays listed
    fireEvent.click(screen.getByRole("button", { name: "Close" }));
    await openDialog();
    await screen.findByText("My laptop");
    expect(document.body.textContent).not.toContain(secret);
  });

  it("offers tokens that never expire", async () => {
    await openDialog();
    fireEvent.change(screen.getByLabelText("Expires"), {
      target: { value: "3" },
    });
    createToken();
    await screen.findByText(/Copy it now/);
    expect(server.tokens[0].expiresAt).toBeNull();
  });

  it("keeps the session on a wrong password", async () => {
    await openDialog();
    createToken({ password: "nope" });

    expect((await screen.findByRole("alert")).textContent).toBe(
      "Wrong password.",
    );
    expect(server.tokens).toEqual([]);
    expect(server.loggedIn).toBe(true);
    expect(
      screen.getByRole("dialog", { name: "Connect AI tools" }),
    ).toBeTruthy();
  });

  it("revokes a token after confirming", async () => {
    await openDialog();
    createToken({ name: "Old" });
    await screen.findByText(/Copy it now/);
    fireEvent.click(screen.getByRole("button", { name: "Done" }));

    fireEvent.click(await screen.findByRole("button", { name: "Revoke Old" }));
    expect(server.tokens[0].revokedAt).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Revoke" }));

    await screen.findByText("revoked");
    expect(server.tokens[0].revokedAt).not.toBeNull();
    expect(screen.queryByRole("button", { name: "Revoke Old" })).toBeNull();
  });

  it("closes on Escape", async () => {
    await openDialog();
    fireEvent.keyDown(document, { key: "Escape" });
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  });
});
