# Multi-board + akun user untuk Excalidraw self-hosted (Go + Postgres + Docker)

## TL;DR

**Tier**: T3. Ada security boundary baru (auth), service baru, dan perubahan alur persistence app. **Verdict**: Service Go kecil (`excalidraw-server/`) menyediakan auth berbasis session cookie dan CRUD board/file di Postgres. `excalidraw-app` mendapat "board mode" yang aktif hanya jika env `VITE_APP_BOARDS_API` di-set. Dengan begitu perilaku upstream tidak berubah dan `packages/*` tidak disentuh. **Risk**: Medium. Risiko utamanya data hilang saat autosave (konflik dua tab, atau tab ditutup sebelum flush). Mitigasinya optimistic versioning dan flush saat tab disembunyikan. **Rollback path**: Build ulang frontend tanpa `VITE_APP_BOARDS_API` (app kembali ke mode localStorage). Data di Postgres tetap ada.

## Git Workflow (fork) & Langkah 0

Repo saat ini hanya punya remote `origin` (fork `rramadhan11/excalidraw`), belum ada remote `upstream`. Aturannya:

- **`master` tetap bersih**, menjadi mirror dari upstream `excalidraw/excalidraw`. Fitur tidak pernah di-commit langsung ke `master`.
- **`selfhost`**: branch jangka panjang yang berisi semua kustomisasi. Branch inilah yang di-deploy ke VPS.
- **Feature branch** dibuat dari `selfhost` (mis. `feat/boards`), lalu di-merge kembali ke `selfhost` lewat PR di fork setelah test lolos.
- **Sync upstream** secara berkala:
  ```
  git fetch upstream
  git checkout master && git merge --ff-only upstream/master && git push
  git checkout selfhost && git merge master
  ```
  Konflik merge biasanya hanya muncul di `App.tsx` dan beberapa komponen yang ditandai.

Langkah 0 setelah plan disetujui:

1. `git remote add upstream https://github.com/excalidraw/excalidraw.git`
2. `git checkout -b selfhost`, lalu `git checkout -b feat/boards`
3. Salin plan ini ke `deploy/BOARDS_PLAN.md` di dalam project dan commit sebagai commit pertama.

## Context

Excalidraw self-hosted hanya menyimpan **1 scene** di localStorage browser, sementara share link dan collab memakai server excalidraw.com. User mau menjalankannya di VPS untuk dirinya, keluarga, dan teman dengan ketentuan:

- tiap orang punya akun sendiri
- board tersimpan di server: list, buat, rename, hapus, autosave, buka via `/board/:id`
- gambar ikut tersimpan
- live collab belum perlu
- backend Go, Postgres yang sudah jalan di Docker VPS, deploy dengan Docker Compose

Ke depan mungkin perlu share board ke user lain. Untuk sekarang cukup didesain supaya fitur itu bisa ditambahkan tanpa migrasi yang merusak data.

## Current State (verified)

**Load** (`excalidraw-app/App.tsx:217-370`, `initializeScene`)

1. Baca scene dari localStorage lewat `importFromLocalStorage()`.
2. Kalau ada `#json=id,key`, ambil scene dari `importFromBackend` (json.excalidraw.com).
3. Kalau ada room link, mulai collab (Firebase + socket).

**Gambar** (`App.tsx:479-558`, `loadImages`)

- Diambil dari IndexedDB (`LocalData.fileStorage.getFiles`) atau dari Firebase untuk scene eksternal.

**Save** (`App.tsx:715-760`, `onChange`)

1. `onChange` memanggil `LocalData.save` (`data/LocalData.ts:131-160`). Fungsi ini di-debounce (`SAVE_TO_LOCAL_STORAGE_TIMEOUT`).
2. Elements dan appState ditulis ke localStorage.
3. File gambar disimpan lewat `FileManager.saveFiles` ke IndexedDB.
4. `flushSave` dipanggil saat unload, blur, dan visibilitychange (`App.tsx:655-672, 690-712`).

**Hal lain yang relevan**

- `FileManager` (`data/FileManager.ts:21`) generik terhadap backend: constructor menerima `getFiles` dan `saveFiles`. Ini sudah dipakai ulang oleh Firebase dan IndexedDB.
- Helper serialisasi yang tersedia:
  - `clearAppStateForDatabase` (`packages/excalidraw/appState.ts:331`)
  - `restoreElements` dan `restoreAppState` (`packages/excalidraw/data/restore.ts:956,1264`)
  - `hashElementsVersion` (`packages/element/src/index.ts:21`)
- Tidak ada router. App adalah SPA tunggal (`excalidraw-app/index.tsx`).
- `Dockerfile` di root membangun `yarn build:app:docker` lalu menyajikannya dengan nginx default. Belum ada SPA fallback dan belum ada proxy `/api`.
- Ketergantungan ke excalidraw.com: `.env.production` (`VITE_APP_BACKEND_V2_*`, `VITE_APP_FIREBASE_CONFIG`, `VITE_APP_WS_SERVER_URL`, `VITE_APP_AI_BACKEND`, `VITE_APP_PLUS_*`). Firebase sudah lazy-loaded (commit 1d4dc2c0), jadi tidak ikut dimuat kalau fiturnya tidak dipakai.

## Proposed Approach

### 1. Backend Go: `excalidraw-server/` (direktori baru di root repo)

**Stack**

- `net/http` stdlib (Go 1.22+, routing berbasis method dan path)
- `pgx/v5` (pgxpool)
- `golang.org/x/crypto/bcrypt`
- migrasi SQL yang di-embed (`embed` + `goose`)
- tanpa framework

**Skema** (`excalidraw-server/internal/db/migrations/00001_init.sql`)

```sql
users       (id uuid pk, email text unique check(lowercase), name text, password_hash text, created_at)
sessions    (token_hash bytea pk, user_id fk on delete cascade, expires_at, created_at)  -- index (user_id), (expires_at)
boards      (id uuid pk, owner_id fk on delete cascade, name text not null,
             scene json not null, version bigint not null default 1,
             created_at, updated_at)                                            -- index (owner_id, updated_at desc)
board_files (board_id fk on delete cascade, file_id text, mime_type text,
             data_url text, file_created bigint, version int, updated_at,
             primary key (board_id, file_id))
```

- `scene` berisi `{elements, appState}`. appState sudah dibersihkan dengan `clearAppStateForDatabase` di sisi client, sama seperti cara share link menyimpan data.
- **Penyesuaian saat implementasi:**
  - `scene` memakai tipe `json`, bukan `jsonb`. `jsonb` menolak escape `\u0000` (save board bisa gagal permanen) dan mengurutkan ulang key. Isi scene tidak pernah di-query, jadi `jsonb` tidak memberi manfaat apa pun.
  - Email disimpan dalam lowercase dengan CHECK, bukan `citext`, supaya tidak perlu extension Postgres.
- Gambar disimpan di Postgres, bukan di disk, sehingga backup cukup satu `pg_dump`. Skala keluarga/teman masih aman di sini. Batas ukuran per file 4 MiB, sesuai `FILE_UPLOAD_MAX_BYTES` di `excalidraw-app/app_constants.ts`.
  - **Penyesuaian:** gambar disimpan sebagai `data_url text` apa adanya, bukan di-decode ke `bytea`. Client mendapat kembali data yang persis sama, termasuk data URL non-base64 seperti SVG. Biayanya sekitar 33% overhead base64.
  - **Penyesuaian:** `BinaryFileData` punya field `version` (`packages/excalidraw/types.ts:145`), jadi file tidak sepenuhnya immutable. Upload memakai upsert: file yang tersimpan hanya diganti kalau `version` yang dikirim lebih baru. Re-upload versi yang sama tidak mengubah apa pun, jadi tetap aman di-retry.
- Persiapan fitur sharing: semua akses board lewat satu fungsi `authorizeBoard(ctx, userID, boardID, perm)`. Untuk sekarang isinya cek `owner_id`. Nanti cukup tambah tabel `board_members`, tanpa mengubah handler.

**Auth**

- Tidak ada signup publik. Admin membuat user lewat CLI: `docker compose exec server excalidraw-server user create --email ... --name ...` Password dibaca dari prompt atau stdin, tidak lewat argumen. Ada juga perintah `user passwd` dan `user list`.
- `POST /api/auth/login`: verifikasi bcrypt, lalu buat token acak 32 byte. Yang disimpan di DB adalah sha256 dari token. Token dikirim ke browser sebagai cookie `HttpOnly; Secure; SameSite=Lax; Path=/`, masa berlaku 30 hari (sliding).
- `POST /api/auth/logout` dan `GET /api/auth/me`.
- Rate limit login sederhana per IP+email di memori. Pesan error dibuat generik supaya tidak membocorkan apakah email terdaftar.
- CSRF: frontend dan API berada di origin yang sama (nginx proxy `/api`), cookie memakai SameSite=Lax, dan semua request yang mengubah data wajib membawa header `X-Requested-With: excalidraw`. Kalau tidak, server membalas 403.

**API board** (semua butuh session dan lolos `authorizeBoard`) | Method & path | Fungsi | |---|---| | `GET /api/boards` | `[{id,name,updatedAt}]` urut `updated_at desc`, tanpa scene | | `POST /api/boards` | `{name}` → board kosong | | `GET /api/boards/{id}` | `{id,name,version,scene}` | | `PUT /api/boards/{id}/scene` | body `{version, scene}` | | `PATCH /api/boards/{id}` | `{name}` (rename) | | `DELETE /api/boards/{id}` | hapus board, file ikut terhapus lewat cascade | | `PUT /api/boards/{id}/files/{fileId}` | `{mimeType, dataURL, created, version?}` → disimpan apa adanya. Hanya `version` yang lebih baru yang menimpa file lama, jadi aman di-retry | | `GET /api/boards/{id}/files?ids=a,b,c` | kembalikan `{files: [...BinaryFileData]}` dan `erroredIds` |

Detail `PUT /scene`:

- Query: `UPDATE ... SET scene=$1, version=version+1 WHERE id=$2 AND version=$3`.
- Kalau 0 baris terupdate, balas **409** dengan versi terbaru (optimistic concurrency).
- Batas body 50 MB.

Detail umum:

- Board milik orang lain dibalas **404**, bukan 403, supaya keberadaan id tidak bocor.
- Validasi input: uuid, panjang nama 1–200, whitelist mime (`image/png|jpeg|gif|webp|svg+xml|avif|bmp`), ukuran file. Scene harus JSON object yang berisi `elements` berupa array.
- Logging dengan `slog` JSON: request id, user id, board id, status, durasi. Tanpa password atau token.
- Konfigurasi lewat env: `DATABASE_URL`, `LISTEN_ADDR`, `COOKIE_SECURE`.
- Migrasi dijalankan otomatis saat start.
- Ada `/healthz`.

### 2. Frontend: board mode di `excalidraw-app/` (opt-in lewat env)

Board mode aktif bila `import.meta.env.VITE_APP_BOARDS_API` di-set (contoh `/api`). Tanpa env ini perilaku app sama persis dengan upstream, jadi test upstream tetap lolos dan merge dari upstream tetap gampang.

**File baru**

- `excalidraw-app/boards/api.ts`
  - wrapper `fetch` bertipe dengan `credentials: "same-origin"` dan header CSRF
  - 401 → `UnauthorizedError`, 409 → `ConflictError`
- `excalidraw-app/boards/BoardStorage.ts` meniru API `LocalData`: `save`, `flushSave`, `fileStorage`, `isSavePaused`
  - **Reuse `FileManager`**. `getFiles` dan `saveFiles` diarahkan ke endpoint files. Pola ini sama dengan `LocalData.fileStorage`, jadi status gambar "saved"/"error" dan `updateStaleImageStatuses` langsung berfungsi.
  - Save di-debounce sekitar 1 detik dan hanya jalan kalau `hashElementsVersion(elements)` atau appState yang di-persist berubah.
  - Save untuk satu board diserialkan (satu request in-flight; perubahan berikutnya di-coalesce).
  - Setiap save mengirim `version` terakhir. Saat 409, autosave dipause dan muncul dialog "Board diubah di tab/perangkat lain, muat ulang". Data lokal **tidak** di-overwrite diam-diam.
  - Status save disimpan di jotai atom: `saved | saving | dirty | error | conflict`.
- `excalidraw-app/boards/LoginScreen.tsx`: form email dan password, dirender sebelum `<Excalidraw>` jika `/auth/me` membalas 401.
- `excalidraw-app/boards/BoardsSidebarTab.tsx`: tab baru di `AppSidebar` untuk list board, buat baru, rename inline, dan hapus (dengan `openConfirmModal`). Item yang diklik membuka board tersebut.
- `excalidraw-app/boards/SaveStatus.tsx`: indikator kecil "Tersimpan / Menyimpan… / Gagal simpan" di `renderTopRightUI`.
- `excalidraw-app/boards/useBoardRoute.ts`
  - parse `/board/:id` dari `location.pathname`
  - navigasi dengan `history.pushState` + `popstate`
  - `/` tanpa id diarahkan ke board terbaru, atau ke board baru "Untitled" kalau user belum punya board

**Perubahan di `App.tsx`** (dibuat sedikit dan terkumpul, ditandai komentar `// boards mode`)

- `initializeScene`: kalau board mode aktif, load scene dari `GET /boards/:id`, lalu jalankan `restoreElements` dan `restoreAppState` dengan pola yang sama seperti cabang `jsonBackendMatch`. Cabang localStorage, `#json`, dan collab dilewati.
- `loadImages`: memakai `BoardStorage.fileStorage.getFiles` untuk board mode.
- `onChange`, `flushSave`, dan handler unload/visibility: memanggil `BoardStorage` sebagai pengganti `LocalData` di board mode.
- Saat `beforeunload`, `preventUnload` dipanggil kalau status masih `dirty/saving`. `fetch keepalive` tidak dipakai karena batasnya 64 KB, terlalu kecil untuk scene.
- Saat pindah board: flush save board lama, tunggu selesai, lalu `updateScene` dengan board baru dan `excalidrawAPI.history.clear()` supaya undo tidak menyeberang antar board.
- `syncData` antar tab via localStorage dimatikan di board mode, karena server yang menjadi source of truth dan konflik ditangani lewat 409.

**Menonaktifkan dependensi ke excalidraw.com di board mode**

- `isCollabDisabled = true`: tombol Live collaboration, `Collab`, dan room link tidak dirender.
- Share link (`onExportToBackend`), `ExportToExcalidrawPlus`, promo Excalidraw+ (`ExcalidrawPlusPromoBanner`, promo di `AppSidebar`), dan item Excalidraw+/Sign-in di `AppMainMenu` tidak dirender.
- AI (`AIComponents`, TTD) tidak dirender, karena backend-nya `oss-ai.excalidraw.com`.
- Tracking: `VITE_APP_ENABLE_TRACKING=false`. Sentry sudah dimatikan oleh `build:app:docker`.
- Library browser (`libraries.excalidraw.com`) dibiarkan aktif. Itu hanya situs publik untuk menelusuri library, bukan penyimpanan data user.
- Mengimpor board lama dari localStorage tidak perlu fitur baru: cukup export `.excalidraw` dari instance lama, lalu Open di board baru dan board akan autosave.

### 3. Deploy (Docker Compose)

- **`.env.selfhost`** (baru): override env untuk build. `vite build --mode selfhost` memuat file ini di atas `.env.production`. Isinya `VITE_APP_BOARDS_API=/api`, tracking off, dan URL excalidraw.com yang tidak dipakai dikosongkan.
- **`Dockerfile`** (root, modify):
  - tambah `ARG BUILD_MODE` (default kosong, perilaku upstream tetap)
  - `COPY deploy/nginx.conf`
- **`excalidraw-app/package.json`**: script baru `build:app:selfhost`.
- **`deploy/nginx.conf`** (baru):
  - SPA fallback `try_files $uri /index.html`
  - `location /api/ { proxy_pass http://server:8080; client_max_body_size 50m; }`
  - security headers
- **`excalidraw-server/Dockerfile`** (baru): multi-stage `golang:1.23` ke `gcr.io/distroless/static`, user non-root.
- **`deploy/docker-compose.selfhost.yml`** (baru)
  - service `web` (nginx + build frontend) dan `server` (Go)
  - join ke `external: true` network milik Postgres yang sudah ada
  - `DATABASE_URL` dari `.env` (tidak di-commit)
  - hanya `web` yang expose port
  - TLS lewat reverse proxy VPS yang sudah ada (Caddy/Traefik/nginx), atau Caddy ditambahkan kalau belum ada
- **`deploy/README.md`**: langkah setup
  1. buat database dan role di Postgres yang ada
  2. isi `.env`
  3. `docker compose up -d --build`
  4. `user create`
  5. backup dengan `pg_dump`

## First-Principles Audit

| Approach | Pros | Cons | Verdict |
| --- | --- | --- | --- |
| **A. Go API + Postgres (scene json, file data URL di Postgres), board mode opt-in di excalidraw-app** | Sesuai stack user. Satu sumber data dan satu backup. Reuse `FileManager`/restore. Core library tidak disentuh | Perlu menulis auth sendiri dan merawat fork `App.tsx` | **Chosen** |
| B. Implement ulang protokol share-link `#json` (`VITE_APP_BACKEND_V2_*`) di Go, frontend tidak diubah | Hampir tanpa perubahan frontend | Tidak ada konsep user, list board, rename, atau autosave. Tiap perubahan menghasilkan link baru. Tidak memenuhi requirement | Rejected |
| C. Simpan gambar di disk (volume) atau MinIO, bukan bytea | DB lebih kecil, streaming lebih efisien | Dua sistem yang harus di-backup dan disinkronkan. Rawan orphan file atau konsistensi hilang saat restore. Belum ada kebutuhan skala ke arah sana | Rejected untuk sekarang. Bisa dimigrasi nanti di balik interface `FileStore` di Go |
| D. Pakai proyek OSS pihak ketiga (mis. excalidraw-storage-backend) | Lebih cepat | Bukan Go. Model data per-room tanpa akun. Kode tidak dikuasai | Rejected |

Kenapa A: requirement intinya adalah _kepemilikan per user_ dan _banyak board_. Dua hal itu tidak ada di protokol upstream mana pun (B, D), jadi butuh model data sendiri. `FileManager` dan `restore*` sudah menjadi seam yang dirancang upstream untuk backend lain (Firebase memakai pola yang sama), sehingga integrasi cukup lewat seam itu tanpa menyentuh `packages/*`. Board mode yang opt-in lewat env menjaga perilaku dan test upstream tetap utuh.

## Files to Modify

| File | Tier | Why | Action |
| --- | --- | --- | --- |
| `excalidraw-server/{cmd,internal/{auth,boards,files,db,httpx},migrations}/…` | Production | Service Go | New |
| `excalidraw-server/Dockerfile`, `go.mod` | Production | Build | New |
| `excalidraw-app/boards/{api,BoardStorage,useBoardRoute}.ts` | Production | Client API, persistence, routing | New |
| `excalidraw-app/boards/{LoginScreen,BoardsSidebarTab,SaveStatus}.tsx` + `.scss` | Production | UI | New |
| `excalidraw-app/App.tsx` | Production | Cabang board mode di init/load/save, menyembunyikan fitur excalidraw.com | Modify |
| `excalidraw-app/components/AppSidebar.tsx`, `AppMainMenu.tsx` | Production | Tab Boards, item "My boards"/Logout, sembunyikan promo | Modify |
| `excalidraw-app/app_constants.ts` | Production | `isBoardsMode` dari env | Modify |
| `excalidraw-app/vite-env.d.ts` | Production | Tipe `VITE_APP_BOARDS_API` | Modify |
| `excalidraw-app/vite.config.mts` | Production | Dev proxy `/api` → `localhost:8080` | Modify |
| `excalidraw-app/package.json` | Production | Script `build:app:selfhost` | Modify |
| `.env.selfhost` | Config | Env build self-host | New |
| `Dockerfile` | Production | `BUILD_MODE` arg + nginx.conf | Modify |
| `deploy/{nginx.conf,docker-compose.selfhost.yml,.env.example,README.md}` | Deploy/Doc | Deploy VPS | New |
| `excalidraw-app/tests/boards.test.tsx` | Test | Frontend board mode | New |
| `excalidraw-server/internal/**/*_test.go` | Test | Backend | New |

## Reused Functions / Patterns

| Function / Module | Location | Why |
| --- | --- | --- |
| `FileManager` | `excalidraw-app/data/FileManager.ts:21` | Tracking status simpan/ambil gambar, cukup diisi `getFiles`/`saveFiles` |
| `updateStaleImageStatuses`, `FileStatusStore` | `data/FileManager.ts`, `data/fileStatusStore.ts` | Status gambar error/loaded |
| `restoreElements`, `restoreAppState` | `packages/excalidraw/data/restore.ts:956,1264` | Normalisasi scene dari server, pola seperti cabang `#json` di `App.tsx:262-280` |
| `clearAppStateForDatabase` | `packages/excalidraw/appState.ts:331` | Hanya appState yang relevan yang disimpan |
| `hashElementsVersion` | `packages/element/src/index.ts:21` | Skip save kalau tidak ada perubahan |
| `debounce` (+`.flush`) | `@excalidraw/common` | Sama seperti `LocalData._save` |
| `openConfirmModal` | dipakai di `App.tsx` | Konfirmasi hapus board dan dialog konflik |
| `DefaultSidebar` / `Sidebar.Tab` | `AppSidebar.tsx` | Tab Boards konsisten dengan tab yang sudah ada |

## Test Strategy

- **Go unit**: hashing token dan validasi cookie, validasi input (uuid, mime, ukuran, bentuk scene), decode dataURL, rate limiter.
- **Go integration** (`TEST_DATABASE_URL`, Postgres dari docker; di-skip kalau env tidak di-set) memakai `httptest` untuk:
  - login sukses/gagal, akses tanpa cookie → 401, request tanpa header CSRF → 403
  - user B mengakses board user A → 404 di semua endpoint (GET, PUT, PATCH, DELETE, files)
  - `PUT /scene` dengan versi lama → 409, versi benar → `version+1`
  - upload ulang versi yang sama tidak mengubah data, versi lebih baru menimpa, file >4 MiB → 413, delete board ikut menghapus file (cascade)
- **Frontend (vitest)** `excalidraw-app/tests/boards.test.tsx` dengan `fetch` di-mock:
  - board di-load dari API dan dirender
  - perubahan elemen memicu satu `PUT` setelah debounce, tanpa `PUT` kalau tidak ada perubahan
  - 409 menghentikan autosave dan menampilkan dialog
  - 401 menampilkan LoginScreen
  - tanpa `VITE_APP_BOARDS_API`, test upstream yang ada tetap lolos tanpa perubahan
- Menjalankan `yarn test:typecheck`, `yarn test:update`, `yarn fix`, dan `go test ./...`.

## Design Principles Alignment

- **Accurate & Complete**:
  - server adalah source of truth
  - optimistic versioning mencegah overwrite diam-diam antar tab/perangkat
  - gambar ikut tersimpan per board
  - flush saat tab disembunyikan, ditambah `preventUnload` kalau masih ada perubahan yang belum tersimpan
- **Secure**:
  - tidak ada signup publik
  - bcrypt, token session di-hash di DB, cookie HttpOnly/Secure/SameSite
  - header CSRF, SQL ter-parameterisasi (pgx), validasi di boundary
  - 404 untuk board yang bukan miliknya, rate limit login
- **Simple**: stdlib + pgx, satu DB, tanpa object storage, tanpa framework.

Tradeoff yang sengaja diambil:

- (1) Gambar di bytea. Cukup untuk skala keluarga dan mudah di-backup.
- (2) Fork `App.tsx` menambah potensi konflik saat merge upstream. Diminimalkan dengan logika di `excalidraw-app/boards/` dan insertion point yang sedikit serta ditandai.
- (3) Konflik dua tab ditangani dengan "reload", bukan merge otomatis. Merge otomatis butuh `reconcileElements` dan baru masuk akal bersama live collab nanti.

## Rollback Plan

- Frontend: build ulang tanpa `--mode selfhost` (atau tanpa `VITE_APP_BOARDS_API`), app kembali ke perilaku upstream.
- Backend: `docker compose stop server`. Data tetap di Postgres.
- Migrasi `0001` punya blok `-- +goose Down` (`DROP TABLE board_files, boards, sessions, users`). Hanya dipakai di database baru yang belum berisi data, karena perintah ini menghapus semua data. Sebelum migrasi berikutnya dijalankan di produksi, ambil `pg_dump` dulu.

## Verification

1. Dev lokal:
   - `docker run postgres`
   - `cd excalidraw-server && DATABASE_URL=... go run ./cmd/excalidraw-server serve`
   - `go run ./cmd/excalidraw-server user create --email a@x --name A` (lalu user B)
   - `VITE_APP_BOARDS_API=/api yarn start` (vite proxy `/api` → 8080)
2. Smoke test manual:
   - login A → otomatis masuk board "Untitled" → gambar shape dan tempel image → status "Tersimpan"
   - refresh: shape dan image masih ada
   - buka board yang sama di browser lain: data sama
   - buat board kedua, rename, pindah-pindah board: isi tidak tercampur, undo tidak menyeberang board
   - edit di dua tab lalu simpan: tab kedua mendapat dialog konflik, tidak ada data yang tertimpa diam-diam
   - login B, buka URL board A → "tidak ditemukan"
   - hapus board → `SELECT count(*) FROM board_files WHERE board_id=...` = 0
   - Network tab: tidak ada request ke `*.excalidraw.com` (kecuali saat membuka library browser)
3. `yarn test:typecheck && yarn test:update && go test ./...`
4. Deploy: `docker compose -f deploy/docker-compose.selfhost.yml up -d --build` di VPS, lalu `curl https://<domain>/api/healthz`, lalu smoke test langkah 2 lewat HTTPS.

## Risks

| Risk | Mitigasi |
| --- | --- |
| Tab ditutup tepat setelah edit (dalam ±1 detik debounce) | Flush saat `visibilitychange`/`blur`, ditambah `preventUnload` kalau status `dirty`/`saving` |
| Scene besar dengan banyak gambar membuat save berat | Gambar diunggah terpisah dan hanya sekali per file (immutable), jadi scene JSON tetap kecil. Save hanya jalan kalau hash berubah |
| Konflik merge dengan upstream di `App.tsx` | Logika dikumpulkan di `excalidraw-app/boards/`. Perubahan `App.tsx` berupa cabang kecil bertanda `// boards mode` |
| Cookie `Secure` tidak terkirim kalau diakses via HTTP | Wajib HTTPS di VPS. Ada flag `COOKIE_SECURE=false` khusus dev lokal |
| Postgres sudah dipakai app lain | Database dan role terpisah dengan hak minimal (owner hanya untuk DB `excalidraw`) |

## Open Questions

- Nama network Docker dan host Postgres yang sudah ada di VPS. Ini cukup diisi di `.env`/compose saat deploy, tidak memblokir implementasi.
