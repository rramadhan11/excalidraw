# Plan v2 — MCP server untuk excalidraw self-hosted

> **Versi:** v2 · **Tanggal:** 2026-11-19 · **Status:** diimplementasi (lihat "Catatan implementasi" di akhir) · **Branch:** `feat/mcp`
>
> Riwayat plan: `BOARDS_PLAN.md` (simpan banyak board), `WORKSPACE_PLAN.md` (workspace, dashboard, collections), dokumen ini (MCP).

## TL;DR

**Tier**: T3. Fitur ini menambah jalur auth baru (token API untuk non-browser), endpoint publik baru, dan menulis scene Excalidraw dari server (sebelumnya hanya browser yang menulis).

**Verdict**: Endpoint **remote MCP (Streamable HTTP) di dalam server Go yang sudah ada**, di `https://draw.gemps.my.id/api/mcp`.

- Auth: **personal access token per user** (`Authorization: Bearer`), jadi bisa dipakai dari Claude Code, Codex, dan client MCP lain.
- Tools memanggil store dan package `access` yang sama dengan UI, jadi aturan aksesnya identik.
- Diagram dibuat dari **skeleton elemen**, subset dari `ExcalidrawElementSkeleton` resmi. Server Go mengubahnya menjadi elemen Excalidraw lengkap.

**Risk**: Medium. Ada dua risiko utama:

- Elemen buatan server tidak sesuai skema Excalidraw. Mitigasinya: tes golden yang memuat output Go lewat `restoreElements` asli di vitest.
- AI mengedit board yang sedang terbuka di browser, sehingga autosave browser kena 409 "Changed elsewhere — reload". Ini perilaku yang sudah ada dan tidak ada data yang tertimpa.

**Rollback path**: hapus route `/api/mcp` lewat `git revert`. Migrasi `00003` (tabel `api_tokens`) bersifat additive dan punya blok Down.

## Context

User ingin AI tool (Claude Code, Codex, dan tool lain) bisa:

1. **membaca dan mencari** board
2. **mengelola** board dan collection
3. **membuat dan mengedit diagram** langsung di board excalidraw self-hosted (`draw.gemps.my.id`)

Saat ini satu-satunya auth adalah cookie session dari login browser, dan hanya frontend yang menulis scene.

## Current State (verified)

- **Routing:** `internal/server/server.go` membuat satu `ServeMux`, yang dibungkus `httpx.Logger(httpx.RequireCSRFHeader(mux))`. `RequireCSRFHeader` (`internal/httpx/httpx.go:218`) menolak POST tanpa `X-Requested-With`, jadi POST MCP akan ditolak kalau lewat jalur ini.
- **Auth:** `auth.Handler.RequireUser` hanya membaca cookie `excalidraw_session`. Token session di-hash dengan `hashToken` (`internal/auth/sessions.go:32`, sha256).
- **Store:** semua method store sudah menerima `userID` dan mengecek akses lewat `access.Checker`. Contohnya:

  - `boards.Store.Get/SaveScene/Update/Create/List` (`internal/boards/store.go:131-313`)
  - `collections.Store.*`, `workspaces.Store.List`

  Jadi tools MCP cukup memanggil store ini dengan user milik token.

- **Simpan scene:**
  - `SaveScene` memakai optimistic version (`WHERE version = $2`) dan mengembalikan `*ConflictError` kalau versinya sudah berubah.
  - `normalizeScene` (`validate.go:80`) hanya menyimpan key `elements` dan `appState`.
- **Load di frontend:** `restoreElements(elements, null, {repairBindings: true, deleteInvisibleElements: true})` (`excalidraw-app/boards/useBoardsMode.ts:110`).
  - `restoreElementWithProperties` (`packages/excalidraw/data/restore.ts:450`) mengisi default untuk field yang tidak ada.
  - `syncInvalidIndices` menetapkan `index` yang null.
  - Binding arrow memakai skema v2 `{elementId, fixedPoint, mode}` (`packages/element/src/types.ts:318-332`).
- **Ukuran teks:** saat load, ukuran teks **tidak** diukur ulang (`refreshDimensions` tidak dipakai di app), dan `Fonts.onLoaded` hanya me-render ulang.
  - Untuk teks di dalam container, `x` = tengah container − width/2, lalu teks digambar rata tengah. Jadi perkiraan lebar dari server tetap tampil di tengah.
  - Begitu teks diedit, Excalidraw mengukur ulang ukurannya.
- **Konflik di browser:** `BoardStorage` menangkap 409 dan menghentikan autosave ("Changed elsewhere — reload", `SaveStatus.tsx:18-27`). Tidak ada yang tertimpa.
- **SDK MCP Go:** `github.com/modelcontextprotocol/go-sdk` v1.8.0 menyediakan:

  - `mcp.NewServer`, `mcp.AddTool[In,Out]` (schema input dibuat dari struct)
  - `mcp.NewStreamableHTTPHandler(getServer, &StreamableHTTPOptions{Stateless, JSONResponse})`
  - `auth.RequireBearerToken(verifier, opts)`. `TokenInfo{UserID}` bisa dibaca di handler lewat `req.Extra.TokenInfo`.

  SDK ini butuh **Go ≥ 1.25**, sedangkan server sekarang Go 1.23 (sudah EOL; versi stabil terbaru 1.27.4 / 1.26.11).

## Proposed Approach

### 1. Token API (`auth`)

- **Migrasi `00003_api_tokens.sql`:**
  ```
  api_tokens (id uuid pk, user_id fk users ON DELETE CASCADE, name text check 1..100,
              token_hash bytea unique not null, created_at, last_used_at null,
              expires_at null, revoked_at null)   -- index (user_id)
  ```
- **`internal/auth/tokens.go`:**
  - `Tokens.Create(ctx, userID, name, expiresAt)`:
    - membuat token `exa_` + 32 byte acak (base64url)
    - token mentah hanya dikembalikan sekali; yang disimpan hanya hash `hashToken`
  - `Verify(ctx, token) (User, error)`:
    - token harus belum di-revoke dan belum expired
    - `last_used_at` di-update paling sering sekali per menit
  - `List(userID)`, `Revoke(ctx, id)`
- **CLI** (`cmd/excalidraw-server/main.go`, pola sama dengan `user ...`):
  - `token create --email E --name N [--expires-days D]` mencetak token sekali
  - `token list --email E`
  - `token revoke --id ID`
- **Tradeoff:** di v1 token dibuat lewat CLI saja, karena saat ini hanya kamu (admin via SSH) yang memakai MCP. Halaman "API tokens" di UI bisa menyusul.

### 2. Endpoint `/api/mcp` (`internal/mcpserver`)

- **Wiring di `server.go`:**
  - Mux luar mengirim `/api/mcp` ke `auth.RequireBearerToken(tokens.Verifier, nil)(mcp.NewStreamableHTTPHandler(..., {Stateless: true, JSONResponse: true}))`.
  - Endpoint ini **tidak** melewati `RequireCSRFHeader` dan **tidak** menerima cookie. Bearer token tidak dikirim browser secara otomatis, jadi tidak rentan CSRF.
  - `httpx.Logger` tetap membungkus semuanya.
  - Body request dibatasi 4 MiB dengan `http.MaxBytesReader`.
- **Kenapa `Stateless + JSONResponse`:**
  - tools tidak butuh notifikasi dari server
  - tanpa SSE, jadi nginx dan Caddy tidak perlu dikonfigurasi khusus
  - aman di-restart dan aman kalau ada banyak replika
- **Di handler:** `userID := req.Extra.TokenInfo.UserID`. Error store diterjemahkan jadi tool error dengan pesan yang jelas: `ErrNotFound` → "not found or no access", 409 → conflict.
- **Logging:** nama tool, user id, board id, dan durasi. Token tidak pernah di-log.

### 3. Tools

Setiap tool punya deskripsi yang jelas dan anotasi `ReadOnlyHint` / `DestructiveHint` / `IdempotentHint`.

| Tool | Isi | Memanggil |
| --- | --- | --- |
| `list_workspaces` | `{id, name, role, boardCount}` | `workspaces.Store.List` |
| `list_collections` (workspace_id) |  | `collections.Store.List` |
| `list_boards` (workspace_id, collection?, cursor?, limit≤100) | summary + `url` | `boards.Store.List` |
| `search_boards` (query, workspace_id?) | cocok di nama **atau** teks di dalam board, maks 20 | **baru:** `boards.Store.Search`, `ILIKE` pada `name` dan `scene::text`, dibatasi workspace yang user jadi anggotanya |
| `get_board` (board_id, detail: `summary`\|`full`) | `summary`: daftar elemen ringkas `{id, type, x, y, w, h, text/label, from/to, colors}` + bounding box seluruh konten + `version` + `url`. `full`: JSON elemen mentah. Maksimal 1000 elemen; kalau lebih, ada keterangan `truncated` | `boards.Store.Get` |
| `create_board` (workspace_id, name?, collection_id?) |  | `boards.Store.Create` |
| `rename_board`, `move_board` (collection_id\|null) |  | `boards.Store.Update` |
| `create_collection`, `rename_collection` |  | `collections.Store` |
| `add_elements` (board_id, elements: skeleton[]) | menambahkan elemen ke scene. Hasilnya `{createdIds, version, url}` | `scene` builder + `SaveScene` |
| `update_elements` (board_id, updates: [{id, x?, y?, width?, height?, text?/label?, strokeColor?, backgroundColor?}]) |  | sama |
| `delete_elements` (board_id, ids) | `isDeleted: true` per elemen. Teks label dan binding arrow ikut dibersihkan | sama |

- **Tidak ada tool untuk menghapus board, collection, atau workspace di v1.** Belum ada Trash, sehingga penghapusan oleh AI tidak bisa dibatalkan. Penghapusan tetap lewat UI. Tool ini bisa ditambah setelah fitur Trash ada.
- **Read-modify-write untuk tool menggambar:**

  1. `Get` board (dapat versi v)
  2. terapkan operasi
  3. `SaveScene(v)`
  4. kalau konflik, ulangi dari langkah 1, maksimal 3 kali

  Ini aman diulang karena operasinya deklaratif.

- **Versi elemen:** setiap elemen yang diubah mendapat `version + 1` dan `versionNonce` baru, sama seperti editor. Dengan begitu, rekonsiliasi per elemen di masa depan tetap benar.

### 4. Skeleton → elemen Excalidraw (`internal/scene`, Go murni dan bisa dites unit)

- **Skeleton input** (subset `ExcalidrawElementSkeleton`, `packages/element/src/transform.ts`):
  - `rectangle` / `ellipse` / `diamond`: `{id?, x, y, width?, height?, label?: {text, fontSize?}, strokeColor?, backgroundColor?, fillStyle?, strokeStyle?, roundness?}`
  - `text`: `{x, y, text, fontSize?, textAlign?}`
  - `arrow` / `line`: `{x?, y?, points?, start?: {id}, end?: {id}, label?, startArrowhead?, endArrowhead?, strokeStyle?}`
  - `frame`: `{x, y, width, height, name}`. Child dihubungkan lewat `frameId`, dengan id skeleton atau id elemen yang sudah ada.
- **Output elemen** mengikuti field yang diisi `restoreElementWithProperties`:
  - field umum: `id` (acak 20 char kalau tidak diberi), `seed`, `versionNonce`, `version: 1`, `index: null` (diisi `syncInvalidIndices` saat load), `updated` (ms), `boundElements`, `groupIds: []`, `frameId`, `roundness` (`{type: 3}` untuk rectangle, sesuai default editor)
  - nilai default diambil dari `DEFAULT_ELEMENT_PROPS` dan `DEFAULT_FONT_FAMILY` / `DEFAULT_FONT_SIZE`. Konstanta disalin ke Go, dengan komentar yang menunjuk file sumbernya.
- **Label di dalam shape:**
  - dibuat sebagai elemen `text` dengan `containerId`, `textAlign: center`, `verticalAlign: middle`
  - container mendapat `boundElements: [{type: "text", id}]`
  - teks di-wrap per kata sesuai lebar container dikurangi padding (`BOUND_TEXT_PADDING`)
  - tinggi container ditambah kalau teksnya tidak muat
- **Ukuran teks** diperkirakan dari rata-rata lebar karakter per font × fontSize, dan `lineHeight` mengikuti `getLineHeight`. Hasilnya tidak presisi, tapi teks dalam container tetap tampil di tengah, dan Excalidraw mengukur ulang saat diedit (lihat Current State).
- **Arrow dengan `start` / `end`:**
  - titik ujung ada di tepi shape, di garis antar-pusat
  - binding `{elementId, fixedPoint: [fx, fy], mode: "orbit"}`, dengan `fixedPoint` = posisi relatif titik itu terhadap bounding box
  - id arrow ditambahkan ke `boundElements` kedua shape
  - `points` relatif terhadap `x, y`
- **Validasi** di batas input:
  - type harus dari whitelist
  - angka harus finite dan dalam ±1e6
  - teks maksimal 10k karakter
  - maksimal 500 elemen per panggilan
  - warna harus hex atau `transparent`
  - referensi `start` / `end` / `frameId` harus ada
  - id tidak boleh duplikat
  - `link` dan `image` / `embeddable` ditolak di v1
- **`summarize`:** dipakai oleh `get_board`. Elemen dengan `isDeleted` dilewati, dan label digabung ke container-nya.

### 5. Go, deploy, dan dokumen

- **Go:** `go.mod` → `go 1.26.0` / `toolchain go1.26.11`, ditambah dependency go-sdk v1.8.0. `Dockerfile` → `golang:1.26.11-alpine`.
- **nginx:** tidak perlu diubah. `/api/` sudah di-proxy, dan dengan `JSONResponse` tidak ada SSE.
- **`excalidraw-server/README.md` dan `deploy/README.md`** ditambah bagian "MCP":
  - membuat token
  - Claude Code: `claude mcp add --transport http excalidraw https://draw.gemps.my.id/api/mcp --header "Authorization: Bearer $TOKEN"`
  - Codex: `[mcp_servers.excalidraw]` dengan `url` dan `bearer_token_env_var`
  - revoke token
- **Plan disalin** ke `deploy/MCP_PLAN_v2.md`.
- **Deploy** sama seperti update biasa: build di Mac, `docker load`, lalu `up -d --no-build`. Hanya container excalidraw yang di-recreate (downtime beberapa detik).

## First-Principles Audit

| Approach | Pros | Cons | Verdict |
| --- | --- | --- | --- |
| **A. MCP di server Go + token API + builder skeleton di Go** | Satu deployable. Akses lewat `access` yang sama dengan UI. Tanpa runtime baru. Data langsung berupa elemen final, jadi bisa dibaca dan dicari tanpa membuka browser | Builder Go harus mengikuti skema Excalidraw (diatasi dengan tes golden terhadap `restoreElements` asli). Ukuran teks hanya perkiraan | **Chosen** |
| B. Sidecar Node/TS yang memakai `convertToExcalidrawElements` asli | Konversinya identik dengan upstream | Ada service kedua dengan auth dan akses DB sendiri. Paket editor butuh DOM/canvas untuk mengukur teks di Node. Image dan deploy jadi lebih besar | Rejected: menggandakan batas keamanan demi akurasi ukuran teks yang sebenarnya tidak terlihat |
| C. Server menyimpan skeleton "pending", browser mengonversinya saat board dibuka | Memakai converter asli tanpa runtime baru | Ada dua bentuk data. Board yang belum pernah dibuka tidak punya elemen nyata (tidak bisa dibaca atau dicari, tidak ada thumbnail). Hasil `add_elements` belum bisa diverifikasi | Rejected: bertentangan dengan prinsip Accurate / Complete |
| Auth: OAuth 2.1 (remote connector claude.ai) vs PAT | OAuth: bisa dipakai di connector claude.ai / desktop | OAuth butuh authorization server, consent page, dan dynamic client registration | **PAT sekarang.** SDK sudah punya `ProtectedResourceMetadataHandler`, jadi OAuth bisa ditambah nanti tanpa mengubah tools |

## Files to Modify

| File | Tier | Why | Action |
| --- | --- | --- | --- |
| `excalidraw-server/internal/db/migrations/00003_api_tokens.sql` | Migration | tabel token | New |
| `excalidraw-server/internal/auth/tokens.go` (+ test) | Production | create / verify / revoke token | New |
| `excalidraw-server/cmd/excalidraw-server/main.go` | Production | CLI `token ...` | Modify |
| `excalidraw-server/internal/scene/{skeleton,build,text,summarize}.go` (+ tests) | Production | validasi, builder, ringkasan | New |
| `excalidraw-server/internal/mcpserver/{server,tools_read,tools_manage,tools_draw}.go` (+ test) | Production | registrasi tools dan read-modify-write | New |
| `excalidraw-server/internal/boards/store.go` | Production | `Search` | Modify |
| `excalidraw-server/internal/server/server.go` (+ `server_test.go`) | Production | route `/api/mcp` di luar CSRF | Modify |
| `excalidraw-server/go.mod`, `go.sum`, `Dockerfile` | Build | Go 1.26, go-sdk | Modify |
| `excalidraw-app/tests/mcpScene.test.ts` + `excalidraw-app/tests/fixtures/mcp-scene.json` | Test | golden: output Go → `restoreElements` asli | New |
| `excalidraw-server/README.md`, `deploy/README.md`, `deploy/MCP_PLAN_v2.md` | Doc | setup client dan token | Modify / New |

Tidak ada perubahan di `packages/*` maupun kode frontend app.

## Reused Functions / Patterns

| Function / Module | Location | Why |
| --- | --- | --- |
| `hashToken`, `newToken` | `internal/auth/sessions.go:24-32` | hashing dan generate token API |
| `access.Checker` + semua `Store` | `internal/access`, `internal/boards`, `internal/collections`, `internal/workspaces` | satu titik keputusan akses |
| `boards.Store.SaveScene` + `ConflictError` | `internal/boards/store.go:235` | optimistic concurrency |
| `normalizeScene` | `internal/boards/validate.go:80` | tetap menjadi gerbang terakhir untuk bentuk scene |
| Pola CLI `user ...` dan `readPassword` | `cmd/excalidraw-server/main.go` | `token ...` |
| `auth.RequireBearerToken`, `mcp.AddTool`, `NewStreamableHTTPHandler` | go-sdk v1.8.0 | protokol MCP dan bearer auth |
| Bentuk `ExcalidrawElementSkeleton` | `packages/element/src/transform.ts:579` | format input yang sudah dikenal model AI |
| `restoreElements` | `packages/excalidraw/data/restore.ts:956` | oracle untuk tes golden |

## Test Strategy

- **Unit (Go):**
  - `scene`: label dibungkus jadi teks ber-`containerId`, arrow dengan binding v2 dan `boundElements` dua arah, wrap teks, validasi (referensi hilang, angka tidak finite, type tidak dikenal, id duplikat), `delete_elements` ikut menghapus label dan binding, `update_elements` menaikkan `version`
  - `summarize`
  - token: format, hash, expired, revoked
- **Integration (Go, Postgres sementara di port 55499 seperti sebelumnya):**
  - `/api/mcp` tanpa token → 401
  - token revoked atau expired → 401
  - cookie session saja → 401
  - `tools/list` lengkap
  - setiap tool berjalan lewat HTTP MCP sungguhan memakai client dari go-sdk
  - **isolasi:** token user B tidak bisa membaca atau menulis board dan collection milik A (selalu "not found")
  - `search_boards` hanya mencari di workspace milik user
  - `add_elements` saat versi berubah: retry berhasil dan elemen dari kedua penulis tetap ada
  - POST `/api/mcp` tidak butuh header CSRF, tapi route lain tetap butuh
- **Golden (vitest):**
  - `go test ./internal/scene -update` menulis fixture dari skeleton yang mencakup semua type
  - `mcpScene.test.ts` menjalankan `restoreElements(fixture, null, {repairBindings: true})` dan memastikan:
    - jumlah elemen sama, tidak ada yang dibuang atau dihapus
    - binding arrow dan `containerId` tetap ada
    - field yang wajib ada di elemen hasil `convertToExcalidrawElements` juga ada di output Go
- **Manual / E2E:** dari Claude Code, `claude mcp add` ke server lokal, minta "buat diagram arsitektur 3 service", lalu buka board di browser. Shape, label, dan arrow harus tampil dan bisa diedit, dan menggeser shape ikut menggeser arrow.

## Design Principles Alignment

- **Accurate:**
  - output builder divalidasi terhadap `restore` asli
  - optimistic version mencegah perubahan saling menimpa
  - versi elemen dinaikkan seperti di editor
- **Complete:** tidak ada data terhapus permanen lewat MCP. Penghapusan elemen bersifat soft (`isDeleted`).
- **Secure:**
  - token disimpan sebagai hash, bisa di-revoke, dan opsional expired
  - endpoint hanya menerima bearer token, sehingga tidak ada risiko CSRF
  - semua akses lewat `access` (404 untuk yang tidak berhak)
  - input skeleton divalidasi ketat, `link` dan embed ditolak
  - token tidak pernah di-log
- **Simple:** satu binary, tanpa service baru, tanpa perubahan frontend.

**Tradeoff yang sengaja diambil:**

1. Ukuran teks hanya perkiraan sampai teks diedit.
2. Belum ada tool hapus board atau collection.
3. Token hanya dibuat lewat CLI.
4. Kalau board sedang terbuka di browser saat AI menulis, autosave browser berhenti dengan "Changed elsewhere — reload". Perubahan lokal setelah save terakhir perlu di-export sebelum reload.
   - Perbaikan lanjutan: browser mengambil scene terbaru dan menggabungkannya otomatis dengan `reconcileElements`.

## Rollback Plan

- **Kode:** `git revert`, lalu deploy ulang. `/api/mcp` hilang, fitur lain tidak terdampak.
- **Migrasi `00003`:** Down = `DROP TABLE api_tokens`. Tidak ada data board yang tersentuh.
- **Token yang bocor:** `token revoke --id`, berlaku seketika karena setiap request diverifikasi ke DB.
- **Sebelum deploy:** `pg_dump` seperti di runbook bagian Update.

## Verification

1. `cd excalidraw-server && go vet ./... && TEST_DATABASE_URL=... go test ./...` (Postgres sementara di port 55499, dihapus setelahnya).
2. `yarn test:typecheck`, ESLint, Prettier, `npx vitest run` (termasuk tes golden).
3. Lokal:
   - `serve` + `token create`
   - `claude mcp add --transport http excalidraw-local http://localhost:8080/api/mcp --header "Authorization: Bearer …"`
   - panggil `list_workspaces`, `search_boards`, `add_elements`, lalu cek hasilnya di browser
4. Deploy ke VPS (build di Mac, `docker load`, `up -d --no-build`):
   - `token create` di VPS
   - `claude mcp add` ke `https://draw.gemps.my.id/api/mcp`
   - cek bahwa `medic-quickcomm` tetap 200

## Risks

| Risk | Mitigasi |
| --- | --- |
| Skema elemen Excalidraw berubah di upstream (sync 26 commit tertunda) | Tes golden gagal saat upstream mengubah `restore`. Konstanta Go diberi komentar sumbernya |
| Token bocor (misalnya ter-commit di config client) | Hash di DB, revoke instan, opsi expired, dokumentasi menyarankan env var (`bearer_token_env_var`) |
| AI menulis di board yang sedang dibuka | Konflik terdeteksi (tidak ada yang tertimpa). Didokumentasikan, perbaikan rekonsiliasi menyusul |
| Respons `get_board` terlalu besar untuk konteks model | Default `summary`, maksimal 1000 elemen, keterangan `truncated` |
| Pencarian `scene::text ILIKE` lambat kalau board sangat banyak | Skala personal (puluhan sampai ratusan board) aman. Kalau perlu, tambah kolom teks hasil ekstraksi + index trigram |
| Upgrade Go 1.23 → 1.26 | Seluruh tes Go dijalankan ulang. Go menjaga kompatibilitas mundur |

## Catatan implementasi

Hal-hal yang berbeda dari plan di atas, ditemukan saat implementasi dan pengujian:

- **Pencarian isi board** tidak memakai `scene::text ILIKE`. Cara itu ikut mencocokkan nama key JSON: mencari "type" atau "rectangle" akan cocok dengan semua board. Pencarian sekarang hanya melihat `text` dan nama frame dari elemen yang belum dihapus (`json_array_elements`).
- **Thumbnail dihapus** setelah setiap tool menggambar, karena gambarnya sudah tidak sesuai. Dashboard menampilkan placeholder, dan editor membuat thumbnail baru saat board dibuka.
- **Verifikasi token** ada di `mcpserver`, bukan di `auth`. SDK mengirim teks error verifier ke client, jadi error internal di-log lalu diganti pesan generik. Token tanpa expiry dipakai dengan `AllowMissingExpiration`.
- **Tes golden menemukan dua ketidaksesuaian, dan keduanya sudah diperbaiki:**
  - arrowhead `dot` adalah nama lama (sekarang `circle`, ditambah `cardinality_*` untuk diagram ER)
  - elemen yang dihapus tidak boleh lagi mereferensikan label yang ikut terhapus (`boundElements`)
- **`index` dibiarkan null.** Saat board dibuka, `syncInvalidIndices` mengisinya. Tes golden memastikan index hasilnya valid dan urut, dan semua field lain tidak diubah oleh `restore`.
- **`boards.ParseCollectionFilter` / `DecodeCursor` / `Cursor`** di-export supaya `list_boards` memakai parser yang sama dengan API browser.
- **Di luar MCP:** tes dashboard yang flaky ternyata menunjukkan collection baru belum tampil sampai fetch berikutnya. Sekarang collection baru langsung ditampilkan (commit terpisah).
