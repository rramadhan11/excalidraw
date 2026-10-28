# excalidraw-server

Board storage API for self-hosted `excalidraw-app`: user accounts (session cookies), boards (scene JSON) and image files, stored in Postgres. See [`deploy/BOARDS_PLAN.md`](../deploy/BOARDS_PLAN.md) for the design.

## Commands

```sh
excalidraw-server serve                                   # run the API (applies migrations first)
excalidraw-server user create --email a@b.c --name Name   # prompts for the password
excalidraw-server user passwd --email a@b.c               # also signs out all sessions
excalidraw-server user list
excalidraw-server healthcheck                             # used by the Docker HEALTHCHECK
```

There is no public sign-up; accounts are created with `user create`. When stdin is not a terminal the password is read from its first line (`echo "$PW" | excalidraw-server user create ...`).

| Env | Default |  |
| --- | --- | --- |
| `DATABASE_URL` | (required) | Postgres connection string |
| `LISTEN_ADDR` | `:8080` |  |
| `COOKIE_SECURE` | `true` | `false` only for local http development |
| `TRUST_PROXY_HEADERS` | `false` | `true` behind nginx so rate limiting uses `X-Real-IP` |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |

## Data model

Users are members (`owner` or `member`) of **workspaces**. A workspace owns its **boards** and **collections** (folders; a board is in at most one, otherwise "unsorted"). Every user gets a personal workspace when created. All access decisions go through `internal/access`; sharing a board with users outside its workspace will extend `access.Board` only.

## API

Every request that changes state must send `X-Requested-With: excalidraw` (CSRF guard). Errors are `{"error": {"code", "message"}}`. Anything in a workspace you're not a member of returns 404; a member lacking the role gets 403.

| Route |  |
| --- | --- |
| `POST /api/auth/login` `{email, password}` | sets the session cookie |
| `POST /api/auth/logout` |  |
| `GET /api/auth/me` |  |
| `GET /api/workspaces` | `{workspaces: [{id, name, role, boardCount}]}` |
| `POST /api/workspaces` `{name}` | you become its owner |
| `PATCH /api/workspaces/{id}` `{name}` | owner only |
| `DELETE /api/workspaces/{id}` | owner only; 409 `workspace_not_empty` / `last_workspace` |
| `GET /api/workspaces/{id}/collections` | `{collections: [{id, name, boardCount}]}` |
| `POST /api/workspaces/{id}/collections` `{name}` |  |
| `PATCH /api/collections/{id}` `{name}` |  |
| `DELETE /api/collections/{id}` | its boards become unsorted, never deleted |
| `GET /api/workspaces/{id}/boards?collection=<id>\|unsorted&limit=&cursor=` | `{boards: [summary], nextCursor}`, newest first |
| `POST /api/workspaces/{id}/boards` `{name?, collectionId?}` | creates an empty board |
| `GET /api/boards/{id}` | summary + `{version, scene}` |
| `PATCH /api/boards/{id}` `{name?, collectionId?}` | `collectionId: null` moves to unsorted; moving doesn't change `updatedAt` |
| `DELETE /api/boards/{id}` | deletes the board, its files and thumbnail |
| `PUT /api/boards/{id}/scene` `{version, scene}` | 409 `{version}` if the board changed since `version` |
| `PUT /api/boards/{id}/files/{fileId}` `{mimeType, dataURL, created, version?}` | only a newer `version` replaces a stored file |
| `GET /api/boards/{id}/files?ids=a,b` | `{files: [BinaryFileData], erroredIds}` |
| `PUT /api/boards/{id}/thumbnail` | raw `image/jpeg` or `image/png` body, ≤ 512 KiB |
| `GET` / `DELETE /api/boards/{id}/thumbnail` | served immutable; request it with `?v=<thumbnailUpdatedAt>` |

A board summary is `{id, name, workspaceId, collectionId, createdBy: {id, name} | null, createdAt, updatedAt, thumbnailUpdatedAt}`.

## Development

```sh
docker run -d --rm --name excalidraw-dev-pg -e POSTGRES_PASSWORD=dev -p 55432:5432 postgres:17-alpine
export DATABASE_URL='postgres://postgres:dev@localhost:55432/postgres?sslmode=disable' COOKIE_SECURE=false
go run ./cmd/excalidraw-server user create --email me@example.com --name Me
go run ./cmd/excalidraw-server serve

go test ./...                                   # unit tests
TEST_DATABASE_URL="$DATABASE_URL" go test ./... # + integration tests (WIPES the database; use a throwaway one)
```
