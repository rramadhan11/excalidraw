import { useEffect, useState } from "react";

import { useAtom } from "../app-jotai";

import { boardsApi, UnauthorizedError } from "./api";
import { BoardStorage } from "./BoardStorage";
import { authStateAtom } from "./state";

import "./boards.scss";

import type { BoardUser } from "./api";

const LoginForm = ({
  overlay,
  onLogin,
}: {
  overlay: boolean;
  onLogin: (user: BoardUser) => void;
}) => {
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState("");
  const [submitting, setSubmitting] = useState(false);

  const onSubmit = async (event: React.FormEvent) => {
    event.preventDefault();
    setSubmitting(true);
    setError("");
    try {
      onLogin(await boardsApi.login(email, password));
    } catch (error: any) {
      setError(error.message);
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <div
      className={
        overlay ? "boards-login boards-login--overlay" : "boards-login"
      }
    >
      <form className="boards-login__card" onSubmit={onSubmit}>
        <h1 className="boards-login__title">
          {overlay ? "Session expired" : "Excalidraw"}
        </h1>
        <p className="boards-login__subtitle">
          {overlay
            ? "Log in again to keep saving. Your unsaved changes are kept."
            : "Log in to open your boards."}
        </p>
        <label className="boards-login__field">
          Email
          <input
            type="email"
            autoComplete="username"
            required
            autoFocus
            value={email}
            onChange={(event) => setEmail(event.target.value)}
          />
        </label>
        <label className="boards-login__field">
          Password
          <input
            type="password"
            autoComplete="current-password"
            required
            value={password}
            onChange={(event) => setPassword(event.target.value)}
          />
        </label>
        {error && (
          <div className="boards-login__error" role="alert">
            {error}
          </div>
        )}
        <button
          type="submit"
          className="boards-login__submit"
          disabled={submitting}
        >
          {submitting ? "Logging in…" : "Log in"}
        </button>
      </form>
    </div>
  );
};

/**
 * Boards mode: renders the editor only for a logged-in user. If the session
 * ends while editing, the editor stays mounted (keeping unsaved changes) under
 * a login overlay, and autosave resumes after logging in.
 */
export const BoardsAuthGate = ({ children }: { children: React.ReactNode }) => {
  const [auth, setAuth] = useAtom(authStateAtom);

  useEffect(() => {
    boardsApi
      .me()
      .then((user) => setAuth({ status: "authenticated", user }))
      .catch((error) => {
        if (error instanceof UnauthorizedError) {
          setAuth({ status: "anonymous" });
        } else {
          console.error(error);
          setAuth({ status: "unreachable", message: error.message });
        }
      });
  }, [setAuth]);

  switch (auth.status) {
    case "loading":
      return null;
    case "unreachable":
      return (
        <div className="boards-login">
          <div className="boards-login__card">
            <h1 className="boards-login__title">Can't reach the server</h1>
            <p className="boards-login__subtitle">{auth.message}</p>
            <button
              type="button"
              className="boards-login__submit"
              onClick={() => window.location.reload()}
            >
              Retry
            </button>
          </div>
        </div>
      );
    case "anonymous":
      return (
        <LoginForm
          overlay={false}
          onLogin={(user) => setAuth({ status: "authenticated", user })}
        />
      );
    case "expired":
      return (
        <>
          {children}
          <LoginForm
            overlay
            onLogin={(user) => {
              setAuth({ status: "authenticated", user });
              BoardStorage.resumeAfterLogin();
            }}
          />
        </>
      );
    case "authenticated":
      return <>{children}</>;
  }
};
