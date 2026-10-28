# Workspace + Dashboard + Collections + Thumbnail untuk board mode

## TL;DR

**Tier**: T3. Model kepemilikan data berubah (board pindah dari "milik user" ke "milik workspace"), ada migrasi backfill, security boundary baru (role workspace), halaman baru, dan routing baru. **Verdict**: **Workspace** menjadi batas kepemilikan dan akses. User adalah anggota workspace dengan role tertentu. Board dan collection milik workspace. Semua keputusan akses lewat satu package `access`. Di atas fondasi ini dibangun:

- Dashboard ala Excalidraw+
- workspace switcher (create, rename, switch, delete kalau kosong)
- Collections (folder)
- thumbnail

Invite member dan share board ke user lain menyusul sebagai tambahan **additive**: tabel baru dan satu cabang baru di `access`, tanpa migrasi ulang data. **Risk**: Medium. Ada dua risiko utama:

- **Migrasi backfill** board ke workspace personal. Mitigasinya: satu transaksi, test migrasi di atas data lama, dan `pg_dump` sebelum deploy.
- **Unsaved changes saat keluar editor.** Mitigasinya: satu jalur `navigate()` dengan flush + konfirmasi.

**Rollback path**: `git revert`. Migrasi `00002` punya blok Down yang mengembalikan `owner_id` dari `created_by`. Data board tidak dihapus.

## Context

Sejauh ini user bisa menyimpan banyak board, tapi pengelolaannya hanya lewat tab sidebar di editor. User ingin workspace seperti Excalidraw+ (referensi: `custom_req/Screenshot 2026-10-28 at 17.31.53.png`). Arsitekturnya **harus bisa tumbuh** ke:

- menambah, mengedit, dan berpindah workspace
- membagikan board ke orang lain (bukan live collab)

Scope tahap ini (keputusan user):

- **Masuk**: fondasi workspace + CRUD/switch workspace, Dashboard + thumbnail, Collections.
- **Tahap berikutnya**: invite member dan share board. Fondasinya disiapkan sekarang.
- **Ditunda**: Trash, Quick search, Recent activity.

Bug sidebar sebelumnya sudah diperbaiki di `5039bbfe2`.

## Current State (verified)

**Backend**

- `boards.owner_id` adalah FK ke users dengan `ON DELETE CASCADE`. Index `(owner_id, updated_at desc)` (`excalidraw-server/internal/db/migrations/00001_init.sql`).
- Akses board diputuskan oleh `Store.authorize`, yang membandingkan `owner_id = userID` (`internal/boards/store.go`). Board orang lain dibalas 404.
- `GET /api/boards` mengembalikan semua board milik user (`List`).
- User dibuat lewat CLI `user create` (`internal/auth/users.go`, `Users.Create`).

**Frontend**

- Di board mode, `ExcalidrawApp` → `BoardsAuthGate` → `ExcalidrawWrapper` (`excalidraw-app/App.tsx`, bagian akhir).
- Route `/` di-resolve ke board terbaru (`boards/useBoardsMode.ts`, `getDefaultBoardId`).
- `boardRouteAtom` (`boards/state.ts`) dan `navigateToBoard` (`boards/routes.ts`) menangani flush + konfirmasi saat pindah board.
- `BoardStorage.persist` (`boards/BoardStorage.ts`) mengupload file dulu, lalu scene, dengan konflik ditangani lewat 409. Mekanisme ini sudah aman untuk beberapa editor non-live, yang dibutuhkan untuk sharing nanti.

**API thumbnail dan tema yang tersedia**

- Thumbnail: `exportScene({type: "jpeg", data, config: {maxWidthOrHeight, quality, theme, padding}})` (`packages/excalidraw/scene/export.ts:1347`, diekspor di `packages/excalidraw/index.tsx:426`) dan `cleanAppStateForExport` (`packages/excalidraw/appState.ts`).
- Tema: filter dark mode `invert(93%) hue-rotate(180deg)` (`packages/excalidraw/css/theme.scss:185`) dan `useHandleAppTheme` (`excalidraw-app/useHandleAppTheme.ts`).

## Proposed Approach

### 1. Model data (migrasi `00002_workspaces.sql`, satu transaksi)

```
workspaces        (id uuid pk, name text check 1..100, created_at, updated_at)
workspace_members (workspace_id fk cascade, user_id fk users cascade,
                   role text check (role in ('owner','member')), created_at,
                   primary key (workspace_id, user_id))            -- index (user_id)
collections       (id uuid pk, workspace_id fk cascade, name text check 1..100,
                   created_by fk users on delete set null, created_at, updated_at)
                                                                    -- index (workspace_id, lower(name))
boards            owner_id → RENAME created_by (nullable, FK users ON DELETE SET NULL)
                  + workspace_id uuid NOT NULL fk workspaces ON DELETE RESTRICT
                  + collection_id uuid NULL fk collections ON DELETE SET NULL
                  index (workspace_id, updated_at desc, id), (workspace_id, collection_id, updated_at desc, id)
board_thumbnails  (board_id pk fk boards cascade, mime_type text, data bytea, updated_at)
```

**Backfill** (blok `DO` di migrasi yang sama):

- Setiap user yang sudah ada dibuatkan satu workspace "`<name>`'s workspace", dengan user tersebut sebagai `owner`.
- `boards.workspace_id` diisi workspace personal milik `created_by`, lalu kolomnya di-set `NOT NULL`.

**Kenapa begini:**

- **`created_by` ON DELETE SET NULL**: di workspace bersama, menghapus satu user tidak boleh ikut menghapus board yang dia buat. Saat ini `CASCADE` akan menghapusnya.
- **`workspaces` → `boards` ON DELETE RESTRICT**: workspace hanya bisa dihapus kalau sudah kosong. Ini dijamin oleh database, bukan cuma oleh kode.
- **Invariant `collection.workspace_id = board.workspace_id`** dicek di store dalam transaksi yang sama dengan update. FK komposit butuh `ON DELETE SET NULL (kolom)` dari PG15+, sedangkan versi Postgres di VPS belum diketahui. Invariant ini juga dicakup integration test.
- **User baru** (`user create`) otomatis dibuatkan workspace personal dalam transaksi yang sama.

**Untuk tahap berikutnya (tidak dibuat sekarang, semuanya additive):**

- `workspace_invites` untuk mengundang member.
- `board_shares (board_id, user_id, role 'viewer'|'editor')` untuk share board ke user di luar workspace.
- Viewer membuka editor dengan `viewModeEnabled`, dan `BoardStorage` tidak menyimpan.
- Dashboard mendapat bagian "Shared with me".
- Tidak ada perubahan pada tabel yang dibuat sekarang.

### 2. Akses terpusat: package `internal/access`

Ini satu-satunya tempat yang memutuskan "boleh atau tidak". Store memanggilnya sebelum operasi apa pun.

- `Workspace(ctx, userID, workspaceID, perm)` → role, atau `ErrNotFound` kalau bukan anggota.
  - `PermRead` dan `PermWrite`: owner atau member.
  - `PermManage` (rename/delete workspace): hanya owner.
- `Board(ctx, userID, boardID, perm)` → `{workspaceID}`. Untuk sekarang aksesnya lewat membership workspace board. **Titik ekstensi sharing**: nanti ditambah `OR board_shares`.
- `Collection(ctx, userID, collectionID, perm)` → `{workspaceID}`, lewat workspace.
- Semua cek memakai satu query join (tanpa N+1). Yang tidak berhak selalu dibalas **404**, supaya keberadaan resource tidak bocor.

### 3. API (stdlib mux, pola handler yang sama dengan sekarang)

| Route | Akses | Keterangan |
| --- | --- | --- |
| `GET /api/workspaces` | anggota | `{workspaces: [{id, name, role, boardCount}]}` (satu query) |
| `POST /api/workspaces` `{name}` | user login | pembuat jadi `owner` (dalam satu transaksi) |
| `PATCH /api/workspaces/{id}` `{name}` | owner | rename |
| `DELETE /api/workspaces/{id}` | owner | 409 `workspace_not_empty` kalau masih ada board, 409 `last_workspace` kalau ini satu-satunya workspace user |
| `GET /api/workspaces/{id}/collections` | anggota | `[{id, name, boardCount}]` urut nama |
| `POST /api/workspaces/{id}/collections` `{name}` | anggota |  |
| `PATCH` / `DELETE /api/collections/{id}` | anggota | delete memindahkan board ke Unsorted (`SET NULL`), board tidak dihapus |
| `GET /api/workspaces/{id}/boards?collection=<uuid>\|unsorted&limit=&cursor=` | anggota | summary: `{id, name, workspaceId, collectionId, createdBy: {id, name}\|null, createdAt, updatedAt, thumbnailUpdatedAt}`. **Keyset pagination** di `(updated_at, id)`: `limit` default 60, maksimal 200, `nextCursor` opaque |
| `POST /api/workspaces/{id}/boards` `{name?, collectionId?}` | anggota |  |
| `GET /api/boards/{id}` | akses board | ditambah `workspaceId`, `collectionId`, `thumbnailUpdatedAt` |
| `PATCH /api/boards/{id}` `{name?, collectionId?: uuid\|null}` | akses board | `collectionId` absent dibedakan dari `null` lewat `json.RawMessage`. **Pindah collection tidak menaikkan `updated_at`**, karena kolom itu dipakai untuk "Recently modified" |
| `PUT` / `DELETE` / `GET /api/boards/{id}/thumbnail` | akses board | lihat bagian thumbnail di bawah |
| `PUT /api/boards/{id}/scene`, files | akses board | tidak berubah, hanya authorize-nya lewat `access` |

- `GET /api/boards` (list global) **dihapus**. Endpoint ini belum pernah dirilis, dan penggantinya adalah list per workspace.
- Memindahkan board antar workspace tidak masuk scope ini.

**Thumbnail**

- `PUT`:
  - body berisi bytes mentah `image/jpeg` atau `image/png`, maksimal 512 KiB
  - **magic bytes harus cocok** dengan Content-Type
  - balasan 204
- `GET`:
  - header `nosniff`, `Content-Security-Policy: default-src 'none'`, `Cache-Control: private, max-age=31536000, immutable`
  - URL selalu memakai `?v=<thumbnailUpdatedAt>` sebagai cache-busting
  - `<img>` dari origin yang sama otomatis membawa cookie session

### 4. Frontend

**Routing** (`routeAtom` menggantikan `boardRouteAtom`, `navigate()` menggantikan `navigateToBoard`): | URL | Halaman | |---|---| | `/` | redirect ke workspace terakhir (disimpan di localStorage, dicek ulang ke daftar workspace), atau workspace pertama | | `/w/:workspaceId` (+ `?collection=<uuid>\|unsorted`) | Dashboard | | `/board/:id` | Editor. `workspaceId` dari response board dipakai untuk link "Dashboard" dan tab sidebar |

- `navigate()` saat meninggalkan board: `BoardStorage.flushAndWait()` + flush thumbnail. Kalau masih ada perubahan yang gagal disimpan, user diminta konfirmasi.

**Layout**: `BoardsAuthGate` → `BoardsRouter`.

- Dashboard → `<Dashboard/>`.
- Board → `<ExcalidrawWrapper/>` (perubahan di `App.tsx` ±3 baris).
- Editor di-mount ulang setiap kali masuk dari dashboard.

**Dashboard** (`excalidraw-app/boards/dashboard/`), dibuat mirip screenshot.

- **Sidebar kiri:**
  - **Workspace switcher** (nama + ⇅): daftar workspace untuk berpindah, _Create workspace_, _Rename_ (owner), _Delete_ (owner; error 409 ditampilkan apa adanya, misalnya "Move or delete its boards first")
  - "All boards", "Unsorted"
  - **Collections** dengan tombol `+`, item berisi nama + jumlah board + menu (Rename, Delete dengan konfirmasi "Boards move to Unsorted")
  - user + Log out di bagian bawah
- **Area utama:**
  - judul dan tombol **Start drawing** (membuat board di collection aktif, lalu langsung membukanya)
  - tampilan "All boards": **Recently modified** (4 teratas), lalu grid semua board dengan tombol "Load more" (pakai cursor)
- **BoardCard:**
  - thumbnail (`loading="lazy"`) atau placeholder
  - nama
  - "by `<createdBy>`"
  - "Edited 7 days ago" (`Intl.RelativeTimeFormat`)
  - menu ⋯ berisi Open, Rename, Move to → (daftar collection + Unsorted), Delete
- **Tema:**
  - token CSS sendiri di `.boards-dashboard[data-theme]`, mengikuti `useHandleAppTheme`
  - di dark mode, thumbnail memakai filter yang sama dengan editor, jadi satu thumbnail terang cukup untuk kedua tema
- Responsif: di layar kecil, sidebar menjadi bagian atas.

**Thumbnail** (`boards/thumbnail.ts`, dijadwalkan oleh `BoardStorage`)

- `exportScene` JPEG 640px, quality 0.8, `theme: "light"`.
- **Kapan dibuat:**
  - setelah scene berhasil disimpan, paling sering sekali per 30 detik
  - flush langsung saat keluar board, saat tab disembunyikan, dan saat `open()` board lain
  - board lama yang `thumbnailUpdatedAt == null` dibuatkan setelah dibuka
- Kalau board kosong, panggil `DELETE`.
- Kalau gagal, cukup `console.warn` tanpa mengubah `saveStatus`. Tradeoff yang disengaja: thumbnail tidak kritikal.

**Di dalam editor**

- Main menu mendapat item **Dashboard** (`/w/<workspaceId>`).
- Tab sidebar "Boards" menampilkan board di workspace yang sama, lewat `navigate()`.

### 5. Dokumen

- Plan ini disalin ke `deploy/WORKSPACE_PLAN.md`, termasuk bagian "Tahap berikutnya: invite & share".
- README server diupdate.

## First-Principles Audit

| Approach | Pros | Cons | Verdict |
| --- | --- | --- | --- |
| **A. Workspace sebagai pemilik board dan collection, akses lewat membership + package `access`** | Workspace switching, invite, dan share hanya menambah tabel dan cabang akses. Board bersama tidak hilang saat pembuatnya dihapus. Satu titik audit keamanan | Migrasi backfill sekarang, dan setiap query akses butuh join ke membership | **Chosen** |
| B. Tetap `owner_id`, workspace sebagai label/grup saja | Migrasi kecil | Workspace bersama butuh migrasi ulang ke model A nanti, dengan data yang sudah lebih banyak. Logika akses jadi bercabang di banyak tempat | Rejected: bertentangan dengan permintaan "bisa scale ke workspace & share" |
| C. Akses berbasis ACL per board (tanpa workspace) | Fleksibel per board | Mengelola akses ratusan board satu per satu. Tidak cocok dengan konsep workspace di referensi | Rejected. Share per board tetap disediakan sebagai tambahan (`board_shares`) di atas A |
| Thumbnail: render di browser (`exportScene`) vs di server (headless) | Hasilnya identik dengan editor, tanpa infrastruktur tambahan | Hanya ter-update dari client yang sedang mengedit | **Browser dipilih**. Server rendering butuh Chrome/Node di VPS |

## Files to Modify

| File | Tier | Why | Action |
| --- | --- | --- | --- |
| `excalidraw-server/internal/db/migrations/00002_workspaces.sql` | Migration | skema + backfill | New |
| `excalidraw-server/internal/access/access.go` (+ test) | Production | keputusan akses terpusat | New |
| `excalidraw-server/internal/workspaces/{store,handlers}.go` | Production | CRUD workspace | New |
| `excalidraw-server/internal/collections/{store,handlers}.go` | Production | CRUD collection | New |
| `excalidraw-server/internal/boards/{store,handlers,validate}.go`, `thumbnail.go` | Production | pakai `access`, list per workspace + cursor, move, thumbnail | Modify/New |
| `excalidraw-server/internal/auth/users.go` | Production | `user create` juga membuat workspace personal | Modify |
| `excalidraw-server/internal/server/{server,server_test}.go`, `internal/db/migrate_test.go` | Prod/Test | wiring, integration, test migrasi | Modify/New |
| `excalidraw-server/README.md` | Doc | endpoint baru | Modify |
| `excalidraw-app/boards/{api,state,routes,BoardStorage,useBoardsMode}.ts`, `thumbnail.ts` | Production | API, route, thumbnail | Modify/New |
| `excalidraw-app/boards/BoardsRouter.tsx`, `dashboard/*` (+ `dashboard.scss`) | Production | Dashboard, switcher, collections, cards | New |
| `excalidraw-app/boards/{BoardsSidebarTab,BoardsMenuItems}.tsx` | Production | per workspace, item Dashboard | Modify |
| `excalidraw-app/App.tsx` | Production | gate → `BoardsRouter` (±3 baris) | Modify |
| `excalidraw-app/tests/{boards,dashboard}.test.tsx` | Test |  | Modify/New |
| `deploy/WORKSPACE_PLAN.md` | Doc | salinan plan | New |

## Reused Functions / Patterns

| Function / Module | Location | Why |
| --- | --- | --- |
| Pola `Store.authorize` (404 untuk yang tidak berhak) | `excalidraw-server/internal/boards/store.go` | dipindah dan digeneralisasi ke `access` |
| `httpx.DecodeJSON` / `WriteError` / `AddLogAttrs` | `excalidraw-server/internal/httpx/httpx.go` | handler baru konsisten dengan yang ada |
| `pgx.BeginFunc` (transaksi) | dipakai di `auth/users.go` `SetPassword` | create workspace + member, create user + workspace |
| `exportScene`, `cleanAppStateForExport` | `packages/excalidraw/scene/export.ts:1347`, `appState.ts` | thumbnail |
| `--theme-filter` | `packages/excalidraw/css/theme.scss:185` | thumbnail di dark mode |
| `useHandleAppTheme` | `excalidraw-app/useHandleAppTheme.ts` | tema dashboard |
| `BoardStorage.flushAndWait` / `hasUnsavedChanges` | `excalidraw-app/boards/BoardStorage.ts` | guard navigasi |
| Fake server di test | `excalidraw-app/tests/boards.test.tsx` | diperluas untuk workspace, collection, dan thumbnail |

## Test Strategy

- **Test migrasi** (`internal/db/migrate_test.go`, integration):
  1. Jalankan migrasi sampai `00001`.
  2. Isi 2 user dengan board, file, dan session.
  3. Jalankan `00002`.
  4. Pastikan: tiap user punya 1 workspace sebagai owner, semua board ter-assign ke workspace pembuatnya, scene/file/version tidak berubah, dan Down → Up kembali konsisten.
- **Go unit**: magic bytes, encode/decode cursor, PATCH dengan `collectionId` absent vs `null` vs uuid, matriks permission di `access` (owner/member/non-member × read/write/manage).
- **Go integration**:
  - CRUD workspace: create membuat owner, rename hanya oleh owner, delete ditolak kalau tidak kosong atau workspace terakhir
  - CRUD collection: delete memindahkan board ke Unsorted
  - list dengan filter dan cursor (tanpa duplikat, tanpa yang terlewat saat paginasi)
  - pindah collection tidak mengubah `updated_at`
  - board tidak bisa dimasukkan ke collection dari workspace lain
  - **isolasi**: user lain tidak bisa melihat atau mengubah workspace, collection, board, maupun thumbnail milik orang lain (404)
  - menghapus user tidak menghapus board di workspace (`created_by` → NULL)
  - thumbnail: put/get/delete, header, 413, magic bytes salah
- **Frontend (vitest)**, `thumbnail.ts` di-mock:
  - `/` redirect ke workspace
  - switch, create, dan rename workspace
  - filter collection, Start drawing, Move to, delete collection
  - Load more
  - editor → Dashboard: flush sebelum unmount, konfirmasi muncul saat save gagal
  - setelah save, thumbnail di-upload; board kosong memicu DELETE
  - test board mode yang sudah ada diperbarui sesuai route baru
- Menjalankan `yarn test:typecheck`, ESLint, Prettier, seluruh `vitest run`, dan `go vet ./... && go test ./...`.

## Design Principles Alignment

- **Accurate/Complete**:
  - backfill dalam satu transaksi dan ada test-nya
  - menghapus collection atau user tidak pernah menghapus board
  - workspace yang masih berisi board tidak bisa dihapus (dijamin FK RESTRICT)
  - keluar editor selalu melewati flush
- **Secure**:
  - satu titik keputusan akses (`access`)
  - role owner untuk mengelola workspace
  - resource yang tidak berhak selalu dibalas 404
  - thumbnail dicek magic bytes dan disajikan dengan nosniff + CSP
- **Scalable**:
  - index untuk setiap pola query baru
  - keyset pagination untuk list board
  - `boardCount` dihitung dengan satu query
  - invite dan share hanya menambah tabel dan cabang akses
- **Simple**: tanpa infrastruktur baru, thumbnail memakai renderer Excalidraw sendiri.

**Tradeoff yang sengaja diambil:**

1. Thumbnail bisa tertinggal maksimal ±30 detik, dan board lama baru punya thumbnail setelah dibuka.
2. `/` sekarang menampilkan dashboard, tidak langsung membuka board terakhir.
3. Satu board hanya bisa berada di satu collection.
4. Memindahkan board antar workspace belum didukung.

## Rollback Plan

- Kode: `git revert` commit-commit fase ini.
- Migrasi `00002` Down:

  1. kembalikan `boards.owner_id` dari `created_by` (board yang `created_by`-nya NULL di-assign ke owner workspace-nya)
  2. drop kolom `workspace_id` dan `collection_id`
  3. drop `board_thumbnails`, `collections`, `workspace_members`, `workspaces`
  4. kembalikan FK `ON DELETE CASCADE`

  Isi board tidak hilang. Pengelompokan collection dan thumbnail hilang.

- **Sebelum deploy ke VPS:** `pg_dump`.

## Verification

1. `cd excalidraw-server && go vet ./... && TEST_DATABASE_URL=... go test ./...`, dengan Postgres sementara di Docker yang dihapus setelahnya. Test migrasi dijalankan di atas data versi `00001`.
2. `yarn test:typecheck`, ESLint, Prettier, `npx vitest run`: semua lolos, termasuk test upstream.
3. Manual (backend `serve` + `VITE_APP_BOARDS_API=/api yarn start`):
   - Board lama muncul di "`<nama>`'s workspace".
   - Buat workspace "Kerja", pindah ke sana, buat collection, Start drawing, gambar, kembali ke Dashboard: thumbnail muncul.
   - Rename workspace. Coba hapus workspace yang masih berisi board: muncul pesan error.
   - Mode dark: dashboard dan thumbnail ikut gelap.
   - User lain tidak melihat apa pun milik user pertama.

## Risks

| Risk | Mitigasi |
| --- | --- |
| Backfill gagal atau salah assign di DB produksi | Satu transaksi (gagal berarti tidak ada yang berubah), test migrasi di atas data lama, `pg_dump` sebelum deploy |
| Unsaved changes hilang saat editor di-unmount | Satu jalur `navigate()` dengan flush + konfirmasi, `beforeunload` tetap aktif, ada test-nya |
| Kebocoran akses lewat endpoint baru | Semua lewat `access`, ditambah integration test isolasi untuk setiap route |
| Render thumbnail berat | Throttle 30 detik, maksimal 640px, dijalankan setelah save (bukan di onChange) |
| Merge upstream | Perubahan `App.tsx` ±3 baris, sisanya di `excalidraw-app/boards/` |
