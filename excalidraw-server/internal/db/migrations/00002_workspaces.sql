-- +goose Up
-- Boards move from being owned by a user to being owned by a workspace that
-- users are members of. Every existing user gets a personal workspace holding
-- their boards. goose runs this file in a single transaction.

CREATE TABLE workspaces (
  id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  name       text NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE workspace_members (
  workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
  user_id      uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
  role         text NOT NULL CHECK (role IN ('owner', 'member')),
  created_at   timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (workspace_id, user_id)
);
CREATE INDEX workspace_members_user_id_idx ON workspace_members (user_id);

CREATE TABLE collections (
  id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
  name         text NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
  created_by   uuid REFERENCES users (id) ON DELETE SET NULL,
  created_at   timestamptz NOT NULL DEFAULT now(),
  updated_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX collections_workspace_idx ON collections (workspace_id, lower(name));

-- The creator no longer owns the board: deleting a user must not delete
-- boards that live in a (possibly shared) workspace.
ALTER TABLE boards RENAME COLUMN owner_id TO created_by;
ALTER TABLE boards DROP CONSTRAINT boards_owner_id_fkey;
ALTER TABLE boards ALTER COLUMN created_by DROP NOT NULL;
ALTER TABLE boards ADD CONSTRAINT boards_created_by_fkey
  FOREIGN KEY (created_by) REFERENCES users (id) ON DELETE SET NULL;

-- RESTRICT: a workspace can only be deleted once it has no boards.
ALTER TABLE boards ADD COLUMN workspace_id uuid REFERENCES workspaces (id) ON DELETE RESTRICT;
-- SET NULL: deleting a collection moves its boards to "unsorted".
ALTER TABLE boards ADD COLUMN collection_id uuid REFERENCES collections (id) ON DELETE SET NULL;

-- +goose StatementBegin
DO $$
DECLARE
  u  record;
  ws uuid;
BEGIN
  FOR u IN SELECT id, name FROM users LOOP
    INSERT INTO workspaces (name)
      VALUES (left(u.name || '''s workspace', 100))
      RETURNING id INTO ws;
    INSERT INTO workspace_members (workspace_id, user_id, role)
      VALUES (ws, u.id, 'owner');
    UPDATE boards SET workspace_id = ws WHERE created_by = u.id;
  END LOOP;
END $$;
-- +goose StatementEnd

ALTER TABLE boards ALTER COLUMN workspace_id SET NOT NULL;

DROP INDEX boards_owner_updated_idx;
-- dashboard listing, newest first, keyset-paginated on (updated_at, id)
CREATE INDEX boards_workspace_updated_idx ON boards (workspace_id, updated_at DESC, id DESC);
CREATE INDEX boards_workspace_collection_updated_idx
  ON boards (workspace_id, collection_id, updated_at DESC, id DESC);
-- per-collection counts and ON DELETE SET NULL from collections / users
CREATE INDEX boards_collection_id_idx ON boards (collection_id);
CREATE INDEX boards_created_by_idx ON boards (created_by);

CREATE TABLE board_thumbnails (
  board_id   uuid PRIMARY KEY REFERENCES boards (id) ON DELETE CASCADE,
  mime_type  text NOT NULL CHECK (mime_type IN ('image/jpeg', 'image/png')),
  data       bytea NOT NULL,
  updated_at timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
-- Collections, thumbnails and workspaces are dropped; board content is kept.
-- Boards whose creator was deleted go to their workspace's oldest owner. If a
-- workspace has no owner left this fails (NOT NULL) instead of losing boards.
DROP TABLE board_thumbnails;

UPDATE boards b SET created_by = (
  SELECT m.user_id FROM workspace_members m
   WHERE m.workspace_id = b.workspace_id AND m.role = 'owner'
   ORDER BY m.created_at LIMIT 1
) WHERE created_by IS NULL;

ALTER TABLE boards DROP COLUMN collection_id;
ALTER TABLE boards DROP COLUMN workspace_id;
DROP INDEX boards_created_by_idx;
ALTER TABLE boards DROP CONSTRAINT boards_created_by_fkey;
ALTER TABLE boards ALTER COLUMN created_by SET NOT NULL;
ALTER TABLE boards RENAME COLUMN created_by TO owner_id;
ALTER TABLE boards ADD CONSTRAINT boards_owner_id_fkey
  FOREIGN KEY (owner_id) REFERENCES users (id) ON DELETE CASCADE;
CREATE INDEX boards_owner_updated_idx ON boards (owner_id, updated_at DESC);

DROP TABLE collections;
DROP TABLE workspace_members;
DROP TABLE workspaces;
