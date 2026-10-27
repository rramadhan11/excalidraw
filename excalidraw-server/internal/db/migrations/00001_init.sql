-- +goose Up
CREATE TABLE users (
  id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  -- stored lowercased so the UNIQUE constraint is case-insensitive
  email         text NOT NULL UNIQUE CHECK (email = lower(email) AND length(email) BETWEEN 3 AND 254),
  name          text NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
  password_hash text NOT NULL,
  created_at    timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE sessions (
  -- sha256 of the session token; the raw token only lives in the cookie
  token_hash bytea PRIMARY KEY,
  user_id    uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
  created_at timestamptz NOT NULL DEFAULT now(),
  expires_at timestamptz NOT NULL
);
CREATE INDEX sessions_user_id_idx ON sessions (user_id);
CREATE INDEX sessions_expires_at_idx ON sessions (expires_at);

CREATE TABLE boards (
  id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  owner_id   uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
  name       text NOT NULL CHECK (length(name) BETWEEN 1 AND 200),
  -- `json` (not `jsonb`) keeps the client payload byte-for-byte and accepts
  -- \u0000 escapes, which jsonb rejects. The scene is never queried into.
  scene      json NOT NULL,
  -- optimistic concurrency token, bumped on every scene save
  version    bigint NOT NULL DEFAULT 1,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX boards_owner_updated_idx ON boards (owner_id, updated_at DESC);

CREATE TABLE board_files (
  board_id     uuid NOT NULL REFERENCES boards (id) ON DELETE CASCADE,
  file_id      text NOT NULL,
  mime_type    text NOT NULL,
  -- stored verbatim so the client gets back exactly what it uploaded
  data_url     text NOT NULL,
  -- BinaryFileData.created (epoch ms, client clock)
  file_created bigint NOT NULL,
  -- BinaryFileData.version; a file is only overwritten by a newer version
  version      integer NOT NULL DEFAULT 1,
  updated_at   timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (board_id, file_id)
);

-- +goose Down
-- Destroys all data. Only for rolling back a fresh install.
DROP TABLE board_files;
DROP TABLE boards;
DROP TABLE sessions;
DROP TABLE users;
