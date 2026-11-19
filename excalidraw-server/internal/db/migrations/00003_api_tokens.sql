-- +goose Up
-- Personal access tokens for non-browser clients (the MCP endpoint). Unlike
-- sessions they don't slide; they live until revoked or until expires_at.
CREATE TABLE api_tokens (
  id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id      uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
  name         text NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
  -- sha256 of the token; the raw token is shown once when created
  token_hash   bytea NOT NULL UNIQUE,
  created_at   timestamptz NOT NULL DEFAULT now(),
  last_used_at timestamptz,
  expires_at   timestamptz,
  revoked_at   timestamptz
);
CREATE INDEX api_tokens_user_id_idx ON api_tokens (user_id);

-- +goose Down
DROP TABLE api_tokens;
