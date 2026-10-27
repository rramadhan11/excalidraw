# excalidraw-server

Board storage API for self-hosted `excalidraw-app`: user accounts (session
cookies), boards (scene JSON) and image files, stored in Postgres. See
[`deploy/BOARDS_PLAN.md`](../deploy/BOARDS_PLAN.md) for the design.

## Commands

```sh
excalidraw-server serve                                   # run the API (applies migrations first)
excalidraw-server user create --email a@b.c --name Name   # prompts for the password
excalidraw-server user passwd --email a@b.c               # also signs out all sessions
excalidraw-server user list
excalidraw-server healthcheck                             # used by the Docker HEALTHCHECK
```

There is no public sign-up; accounts are created with `user create`. When
stdin is not a terminal the password is read from its first line
(`echo "$PW" | excalidraw-server user create ...`).

| Env | Default | |
|---|---|---|
| `DATABASE_URL` | (required) | Postgres connection string |
| `LISTEN_ADDR` | `:8080` | |
| `COOKIE_SECURE` | `true` | `false` only for local http development |
| `TRUST_PROXY_HEADERS` | `false` | `true` behind nginx so rate limiting uses `X-Real-IP` |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |

## API

Every request that changes state must send `X-Requested-With: excalidraw`
(CSRF guard). Errors are `{"error": {"code", "message"}}`. Boards owned by
another user return 404.

| Route | |
|---|---|
| `POST /api/auth/login` `{email, password}` | sets the session cookie |
| `POST /api/auth/logout` | |
| `GET /api/auth/me` | |
| `GET /api/boards` | `{boards: [{id, name, createdAt, updatedAt}]}`, newest first |
| `POST /api/boards` `{name?}` | creates an empty board |
| `GET /api/boards/{id}` | `{id, name, version, scene, ...}` |
| `PUT /api/boards/{id}/scene` `{version, scene}` | 409 `{version}` if the board changed since `version` |
| `PATCH /api/boards/{id}` `{name}` | rename |
| `DELETE /api/boards/{id}` | deletes the board and its files |
| `PUT /api/boards/{id}/files/{fileId}` `{mimeType, dataURL, created, version?}` | only a newer `version` replaces a stored file |
| `GET /api/boards/{id}/files?ids=a,b` | `{files: [BinaryFileData], erroredIds}` |

## Development

```sh
docker run -d --rm --name excalidraw-dev-pg -e POSTGRES_PASSWORD=dev -p 55432:5432 postgres:17-alpine
export DATABASE_URL='postgres://postgres:dev@localhost:55432/postgres?sslmode=disable' COOKIE_SECURE=false
go run ./cmd/excalidraw-server user create --email me@example.com --name Me
go run ./cmd/excalidraw-server serve

go test ./...                                   # unit tests
TEST_DATABASE_URL="$DATABASE_URL" go test ./... # + integration tests (WIPES the database)
```
