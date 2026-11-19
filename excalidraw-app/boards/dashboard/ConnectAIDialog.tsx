/**
 * Self-service API tokens for MCP clients (Claude Code, Codex, …): create one
 * and get ready-to-paste client setup, list them, revoke them.
 */
import { copyTextToSystemClipboard } from "@excalidraw/excalidraw/clipboard";
import { CloseIcon } from "@excalidraw/excalidraw/components/icons";
import { useCallback, useEffect, useState } from "react";

import { tokensApi } from "../api";

import { formatRelative } from "./ui";

import type { ApiToken, TokenLifetimeDays } from "../api";

const LIFETIMES: { label: string; days: TokenLifetimeDays }[] = [
  { label: "30 days", days: 30 },
  { label: "90 days", days: 90 },
  { label: "1 year", days: 365 },
  { label: "Never expires", days: null },
];
const DEFAULT_LIFETIME = 1;

const tokenStatus = (token: ApiToken, now = Date.now()) => {
  if (token.revokedAt) {
    return "revoked";
  }
  if (token.expiresAt && new Date(token.expiresAt).getTime() <= now) {
    return "expired";
  }
  return "active";
};

/** client setup snippets with the token filled in */
export const clientSetup = (mcpUrl: string, secret: string) => [
  {
    label: "Claude Code",
    text: `claude mcp add --transport http excalidraw ${mcpUrl} --header "Authorization: Bearer ${secret}"`,
  },
  {
    label: "Codex: add to your shell profile",
    text: `export EXCALIDRAW_TOKEN="${secret}"`,
  },
  {
    label: "Codex: ~/.codex/config.toml",
    text: `[mcp_servers.excalidraw]\nurl = "${mcpUrl}"\nbearer_token_env_var = "EXCALIDRAW_TOKEN"`,
  },
  { label: "Token only (other MCP clients)", text: secret },
];

const CopyBlock = ({ label, text }: { label: string; text: string }) => {
  const [copied, setCopied] = useState(false);
  return (
    <div className="connect-ai__snippet">
      <div className="connect-ai__snippet-header">
        <span>{label}</span>
        <button
          type="button"
          className="boards-dashboard__secondary"
          aria-label={`Copy ${label}`}
          onClick={async () => {
            await copyTextToSystemClipboard(text);
            setCopied(true);
          }}
        >
          {copied ? "Copied" : "Copy"}
        </button>
      </div>
      <pre>{text}</pre>
    </div>
  );
};

export const ConnectAIDialog = ({ onClose }: { onClose: () => void }) => {
  const [tokens, setTokens] = useState<ApiToken[] | null>(null);
  const [error, setError] = useState("");
  const [name, setName] = useState("Claude Code");
  const [lifetime, setLifetime] = useState(DEFAULT_LIFETIME);
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  // the raw token lives only here, until the dialog closes
  const [created, setCreated] = useState<{
    name: string;
    secret: string;
  } | null>(null);
  const [confirmRevoke, setConfirmRevoke] = useState<string | null>(null);

  const reload = useCallback(async () => {
    try {
      setTokens(await tokensApi.list());
    } catch (err: any) {
      setError(err.message);
    }
  }, []);

  useEffect(() => {
    reload();
  }, [reload]);

  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape") {
        onClose();
      }
    };
    document.addEventListener("keydown", onKeyDown);
    return () => document.removeEventListener("keydown", onKeyDown);
  }, [onClose]);

  const create = async (event: React.FormEvent) => {
    event.preventDefault();
    setBusy(true);
    setError("");
    try {
      const res = await tokensApi.create({
        name,
        expiresInDays: LIFETIMES[lifetime].days,
        password,
      });
      setCreated({ name: res.token.name, secret: res.secret });
      setPassword("");
      await reload();
    } catch (err: any) {
      setError(
        err.code === "invalid_password" ? "Wrong password." : err.message,
      );
    } finally {
      setBusy(false);
    }
  };

  const revoke = async (tokenId: string) => {
    setConfirmRevoke(null);
    setError("");
    try {
      await tokensApi.revoke(tokenId);
      await reload();
    } catch (err: any) {
      setError(err.message);
    }
  };

  const active = (tokens ?? []).filter((t) => tokenStatus(t) === "active");
  const inactive = (tokens ?? []).filter((t) => tokenStatus(t) !== "active");

  return (
    <div
      className="connect-ai__backdrop"
      onMouseDown={(event) => {
        if (event.target === event.currentTarget) {
          onClose();
        }
      }}
    >
      <div
        className="connect-ai"
        role="dialog"
        aria-modal="true"
        aria-labelledby="connect-ai-title"
      >
        <header className="connect-ai__header">
          <h2 id="connect-ai-title">Connect AI tools</h2>
          <button
            type="button"
            className="boards-dashboard__icon-button"
            onClick={onClose}
            aria-label="Close"
          >
            {CloseIcon}
          </button>
        </header>

        <p className="boards-dashboard__muted">
          Let Claude Code, Codex or another MCP client read, organise and draw
          on your boards. A token acts as you, in all your workspaces: keep it
          secret, like a password.
        </p>

        {error && (
          <div
            className="boards-dashboard__banner boards-dashboard__banner--error"
            role="alert"
          >
            {error}
          </div>
        )}

        {created ? (
          <section className="connect-ai__created" aria-label="New token">
            <p>
              <strong>Token “{created.name}” created.</strong> Copy it now: it
              won't be shown again.
            </p>
            {clientSetup(tokensApi.mcpUrl(), created.secret).map((snippet) => (
              <CopyBlock key={snippet.label} {...snippet} />
            ))}
            <button
              type="button"
              className="boards-dashboard__primary"
              onClick={() => setCreated(null)}
            >
              Done
            </button>
          </section>
        ) : (
          <form className="connect-ai__form" onSubmit={create}>
            <h3>New token</h3>
            <label>
              Name
              <input
                className="boards-dashboard__input"
                required
                maxLength={100}
                value={name}
                onChange={(event) => setName(event.target.value)}
              />
            </label>
            <label>
              Expires
              <select
                className="boards-dashboard__input"
                value={lifetime}
                onChange={(event) => setLifetime(Number(event.target.value))}
              >
                {LIFETIMES.map((option, index) => (
                  <option key={option.label} value={index}>
                    {option.label}
                  </option>
                ))}
              </select>
            </label>
            <label>
              Your password
              <input
                className="boards-dashboard__input"
                type="password"
                autoComplete="current-password"
                required
                value={password}
                onChange={(event) => setPassword(event.target.value)}
              />
            </label>
            <button
              type="submit"
              className="boards-dashboard__primary"
              disabled={busy}
            >
              Create token
            </button>
          </form>
        )}

        <section aria-label="Your tokens">
          <h3>Your tokens</h3>
          {tokens === null ? (
            <p className="boards-dashboard__muted">Loading…</p>
          ) : tokens.length === 0 ? (
            <p className="boards-dashboard__muted">No tokens yet.</p>
          ) : (
            <ul className="connect-ai__tokens">
              {[...active, ...inactive].map((token) => {
                const status = tokenStatus(token);
                return (
                  <li
                    key={token.id}
                    className={
                      status === "active" ? undefined : "connect-ai__inactive"
                    }
                  >
                    <div className="connect-ai__token-info">
                      <strong>{token.name}</strong>
                      <span className="boards-dashboard__muted">
                        {status === "active"
                          ? `created ${formatRelative(token.createdAt)} · ${
                              token.lastUsedAt
                                ? `used ${formatRelative(token.lastUsedAt)}`
                                : "never used"
                            } · ${
                              token.expiresAt
                                ? `expires ${formatRelative(token.expiresAt)}`
                                : "never expires"
                            }`
                          : status}
                      </span>
                    </div>
                    {status === "active" &&
                      (confirmRevoke === token.id ? (
                        <span className="connect-ai__confirm">
                          <button
                            type="button"
                            className="boards-dashboard__danger"
                            onClick={() => revoke(token.id)}
                          >
                            Revoke
                          </button>
                          <button
                            type="button"
                            className="boards-dashboard__secondary"
                            onClick={() => setConfirmRevoke(null)}
                          >
                            Cancel
                          </button>
                        </span>
                      ) : (
                        <button
                          type="button"
                          className="boards-dashboard__secondary"
                          aria-label={`Revoke ${token.name}`}
                          onClick={() => setConfirmRevoke(token.id)}
                        >
                          Revoke
                        </button>
                      ))}
                  </li>
                );
              })}
            </ul>
          )}
        </section>
      </div>
    </div>
  );
};
