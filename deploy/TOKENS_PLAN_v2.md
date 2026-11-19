# Plan v2: token MCP self-service dari web

> **Versi:** v2 · **Tanggal:** 2026-11-19 · **Status:** diimplementasi (lihat "Catatan implementasi" di akhir) · **Branch:** `feat/self-service-tokens` (dari `selfhost`)
>
> Riwayat plan: `BOARDS_PLAN.md` (simpan banyak board), `WORKSPACE_PLAN.md` (workspace, dashboard, collections), `MCP_PLAN_v2.md` (endpoint MCP), dokumen ini (token self-service).

## TL;DR

**Tier**: T2. Ada endpoint baru di jalur auth yang sudah ada (cookie session + CSRF), satu dialog baru di dashboard, dan tidak ada migrasi.

**Verdict**: Setiap user bisa **membuat, melihat, dan me-revoke token MCP miliknya sendiri**.

- **Di web:** tombol "Connect AI" di dashboard. Setelah token dibuat, langsung muncul command `claude mcp add ...` dan config Codex yang siap di-copy.
- **Dari terminal:** curl ke `/api/tokens`, setelah login.
- **CLI admin di VPS** tetap ada.

**Risk**: Low–Medium. Risiko utamanya: session browser yang dicuri bisa dipakai untuk membuat token yang berumur panjang. Mitigasinya: setiap kali membuat token harus memasukkan **password lagi**, dengan rate limit yang sama seperti login.

**Rollback path**: `git revert` lalu deploy ulang. Tidak ada perubahan skema, dan token yang sudah dibuat tetap berlaku sampai di-revoke lewat CLI.

## Context

Saat ini token MCP hanya bisa dibuat dengan `docker compose exec server excalidraw-server token create ...` di VPS, jadi harus punya akses SSH. User ingin membagikan fitur MCP ke teman-teman, dan mereka tidak punya (dan tidak boleh punya) SSH.

Alur yang diinginkan:

1. Admin membuat akun teman (sekali saja, lewat CLI).
2. Teman login di web, klik "Connect AI", lalu klik "Buat token".
3. Teman copy command yang muncul, paste di terminal, dan Claude Code/Codex langsung terhubung.

Alternatif untuk yang suka terminal: dua command curl (login, lalu buat token).

## Current State (verified)

**Store token** (`excalidraw-server/internal/auth/tokens.go`):

- `Create(ctx, email, name, expiresAt)` (`:50`): validasi nama 1–100 karakter dan expiry harus di masa depan, lalu `INSERT ... SELECT id FROM users WHERE email = $1`. Token mentah hanya dikembalikan sekali; yang disimpan hanya hash sha256.
- `List(ctx, email)` (`:114`): semua token user, termasuk yang sudah di-revoke.
- `Revoke(ctx, id)` (`:133`): **tidak dibatasi per user**. Ini memang sesuai untuk admin CLI, tapi tidak boleh dipakai di endpoint user.
- Semua method dikunci ke **email**. Pemanggilnya hanya CLI (`cmd/excalidraw-server/main.go:331-374`) dan test (`internal/server/mcp_test.go:35,177-189`).

**Auth HTTP** (`internal/auth/handlers.go`):

- `Login` (`:89`) memakai rate limiter `ipLimit` dan `accountLimit`, lalu memanggil `Users.Authenticate`.
- `RequireUser` (`:152`) memvalidasi cookie session dan menaruh `User` di context (`UserFrom`).

**Routing** (`internal/server/server.go:55-71`):

- Endpoint `/api/auth/*` terdaftar di `mux`.
- Seluruh `mux` dibungkus `httpx.RequireCSRFHeader`, yang mewajibkan header `X-Requested-With` di request POST, PATCH, dan DELETE.
- `/api/mcp` berada di luar pembungkus itu.

**Frontend:**

- `excalidraw-app/boards/api.ts:85` punya `request()` yang sudah mengirim header CSRF dan memetakan 401/404/409.
- Di dashboard, `Dashboard.tsx:198-210` punya area user di sidebar (nama + tombol logout), dan di situlah tombol baru ditaruh.
- Dashboard tidak memakai komponen `Dialog` dari Excalidraw (dashboard dirender di luar `<Excalidraw>`).
- `dashboard.test.tsx:65` memakai fake server `fetch` untuk test.

## Proposed Approach

### 1. Store: per user (`auth/tokens.go`)

Kunci method diubah dari email ke **userID**. Pemanggil di jalur HTTP sudah punya `User` dari session, dan CLI cukup mencari user dari email sekali.

- `Create(ctx, userID, name, expiresAt)`:
  - validasinya tetap sama
  - **tambahan:** maksimal 20 token aktif per user, dicek dalam transaksi yang sama dengan insert (`SELECT ... FOR UPDATE` pada baris user). Kalau sudah penuh, kembalikan `ErrTooManyTokens`.
- `List(ctx, userID)`: query yang sama dengan kunci `user_id`.
- `Revoke(ctx, userID, id)`:
  - `WHERE id::text = $1 AND user_id = $2`
  - kalau id-nya milik user lain hasilnya `ErrTokenNotFound`, jadi keberadaan token user lain tidak bocor
- **Jalur admin:** CLI `token revoke --id` tetap bisa me-revoke token siapa saja lewat `RevokeAny(ctx, id)`.
  - Revoke yang dibatasi per user dan revoke admin adalah dua aturan akses yang berbeda, jadi dibuat dua method yang jelas. Bukan satu method dengan parameter opsional.
- **CLI:** `token create` dan `token list` memanggil `Users.GetByEmail(email)` (method baru, satu query) lalu memakai userID-nya. Perilaku dan output CLI tidak berubah.

### 2. Endpoint HTTP (`auth/token_handlers.go`, didaftarkan di `server.go`)

Semua endpoint ini lewat `RequireUser` + `RequireCSRFHeader` dan hanya bekerja untuk user yang sedang login.

| Method | Path | Body | Respons |
| --- | --- | --- | --- |
| `GET` | `/api/tokens` | – | `200 {tokens: APIToken[]}`: metadata saja, tanpa hash dan tanpa token mentah |
| `POST` | `/api/tokens` | `{name, expiresInDays: 30\|90\|365\|null, password}` | `201 {token: APIToken, secret: "exa_..."}`: `secret` hanya muncul di respons ini |
| `DELETE` | `/api/tokens/{id}` | – | `204`, atau `404` kalau bukan miliknya |

**Password konfirmasi saat create:**

- Dicek dengan `Users.Authenticate(user.Email, password)`, memakai `ipLimit`/`accountLimit` yang **sama** dengan login, jadi tidak bisa dijadikan jalur brute-force baru.
- Password salah dibalas `403 invalid_password`, bukan 401, supaya frontend tidak menganggap session-nya habis lalu melempar user ke halaman login.

**Validasi:**

- `expiresInDays` hanya boleh salah satu dari daftar di atas. `null` berarti tidak expired. Kalau tidak diisi, default-nya 90.
- Body maksimal 4 KiB.

**Error lain:** `ErrTooManyTokens` → `409 too_many_tokens`, dan kesalahan validasi → `400 invalid_input`.

**Logging:**

- Dicatat: `token created`/`token revoked` dengan `user_id`, `token_id`, dan nama token.
- Tidak pernah dicatat: token mentah dan password.

**Kenapa di package `auth`:** limiter login, `Users`, dan `Tokens` sudah ada di package itu. `Handler` cukup diberi tambahan dependency `*Tokens`.

### 3. Dashboard: dialog "Connect AI"

- **Di sidebar:** tombol "Connect AI" di area user, di atas tombol logout. Tombol ini membuka `ConnectAIDialog` (`boards/dashboard/ConnectAIDialog.tsx`), yang memakai elemen `<dialog>` native: fokus terkunci di dalam dialog dan bisa ditutup dengan Esc, tanpa perlu library baru. Style-nya ditaruh di `dashboard.scss`.
- **Isi dialog:**

  1. **Daftar token:** nama, dibuat, terakhir dipakai (`formatRelative`), dan expired. Token yang sudah di-revoke atau expired ditampilkan abu-abu di bawah. Tombol "Revoke" minta konfirmasi dulu sebelum dijalankan.
  2. **Form "Buat token":** nama (default `"Claude Code"`), pilihan masa berlaku (30 hari / 90 hari / 1 tahun / tidak pernah, default 90 hari), dan password.
  3. **Setelah dibuat:** panel "Simpan sekarang, token tidak akan ditampilkan lagi", dengan tiga blok yang masing-masing punya tombol Copy:

     - token saja
     - Claude Code: `claude mcp add --transport http excalidraw <origin>/api/mcp --header "Authorization: Bearer exa_..."`
     - Codex: `export EXCALIDRAW_TOKEN=...` + blok `[mcp_servers.excalidraw]`

     `<origin>` diambil dari `window.location.origin`, jadi otomatis benar di domain mana pun.

     Token mentah hanya disimpan di state komponen, dan hilang begitu dialog ditutup. Tidak pernah ditulis ke localStorage.

- **`api.ts`:** `tokensApi.list/create/revoke` memakai `request()` yang sudah ada.

### 4. Dokumentasi

- **`deploy/README.md` bagian 6:** jalur utama untuk user jadi "login → Connect AI". CLI tetap ditulis sebagai jalur admin. Tambah resep curl:
  ```bash
  curl -c /tmp/exc.jar -H 'X-Requested-With: curl' -H 'Content-Type: application/json' \
    -d '{"email":"kamu@example.com","password":"..."}' https://draw.gemps.my.id/api/auth/login
  curl -b /tmp/exc.jar -H 'X-Requested-With: curl' -H 'Content-Type: application/json' \
    -d '{"name":"laptop","expiresInDays":90,"password":"..."}' https://draw.gemps.my.id/api/tokens
  rm /tmp/exc.jar
  ```
  Ada juga catatan bahwa password jangan diketik langsung di command line, supaya tidak masuk shell history. Gunakan `read -s` di contoh.
- **`excalidraw-server/README.md`:** tabel endpoint `/api/tokens`.

## First-Principles Audit

| Approach | Pros | Cons | Verdict |
| --- | --- | --- | --- |
| **A. Endpoint `/api/tokens` dengan session + dialog di dashboard, ditambah password konfirmasi** | Teman tidak butuh SSH. Command siap di-copy. Aturan aksesnya memakai session yang sudah ada. Curl jadi bisa untuk yang mau | Ada endpoint dan UI baru yang perlu di-maintain | **Chosen** |
| B. Script shell (`excalidraw-token.sh`) yang login, membuat token, lalu menulis config Claude Code | Tidak perlu UI | Tetap butuh endpoint dari opsi A. Teman harus mengunduh script, dan dukungan Windows repot | Rejected sebagai jalur utama. Resep curl di README sudah mencakup kebutuhan ini |
| C. Login OAuth di MCP (client membuka browser, user login, token otomatis) | UX terbaik, tanpa copy-paste token, dan bisa dipakai sebagai connector claude.ai | Butuh authorization server, consent page, dynamic client registration, dan PKCE. Pekerjaannya beberapa kali lipat A, dan permukaan keamanannya jauh lebih besar | Ditunda. Endpoint dan store dari A tetap terpakai kalau OAuth ditambahkan nanti |
| A tanpa password konfirmasi | Lebih cepat satu langkah | Session yang dicuri atau laptop yang ditinggal dalam keadaan login bisa membuat token yang tidak expired, dan token itu tetap hidup walaupun session di-logout | Rejected. Membuat credential baru yang berumur panjang termasuk aksi sensitif |

## Files to Modify

| File | Tier | Why | Action |
| --- | --- | --- | --- |
| `excalidraw-server/internal/auth/tokens.go` | Production | kunci userID, `Revoke` dibatasi per user, `RevokeAny`, batas 20 token aktif | Modify |
| `excalidraw-server/internal/auth/users.go` | Production | `GetByEmail` untuk CLI | Modify |
| `excalidraw-server/internal/auth/token_handlers.go` | Production | `GET/POST/DELETE /api/tokens` | New |
| `excalidraw-server/internal/auth/handlers.go` | Production | `Handler` mendapat `*Tokens` | Modify |
| `excalidraw-server/internal/server/server.go` | Production | daftarkan route, satu instance `Tokens` dipakai bersama dengan MCP | Modify |
| `excalidraw-server/cmd/excalidraw-server/main.go` | Production | CLI memakai `GetByEmail` dan `RevokeAny` | Modify |
| `excalidraw-server/internal/server/tokens_test.go` | Test | integration test endpoint | New |
| `excalidraw-server/internal/server/mcp_test.go` | Test | ikut perubahan signature | Modify |
| `excalidraw-app/boards/api.ts` | Production | `tokensApi` + tipe `ApiToken` | Modify |
| `excalidraw-app/boards/dashboard/ConnectAIDialog.tsx` | Production | dialog | New |
| `excalidraw-app/boards/dashboard/Dashboard.tsx`, `dashboard.scss` | Production | tombol + style | Modify |
| `excalidraw-app/tests/connectAI.test.tsx` | Test | alur dialog dengan fake server | New |
| `deploy/README.md`, `excalidraw-server/README.md` | Doc | jalur web + curl | Modify |

Tidak ada migrasi dan tidak ada perubahan di `packages/*`.

## Reused Functions / Patterns

| Function / Module | Location | Why |
| --- | --- | --- |
| `Users.Authenticate` + `ipLimit`/`accountLimit` | `internal/auth/handlers.go:89-110` | password konfirmasi dengan rate limit yang sama dengan login |
| `RequireUser`, `UserFrom` | `internal/auth/handlers.go:152` | identitas user |
| `RequireCSRFHeader` | `internal/httpx/httpx.go` | perlindungan CSRF (otomatis, karena route ada di `mux`) |
| `httpx.DecodeJSON`, `WriteJSON`, `WriteError` | `internal/httpx` | format respons dan error yang konsisten |
| `request()` | `excalidraw-app/boards/api.ts:85` | header CSRF + pemetaan error |
| `formatRelative` | `boards/dashboard/ui.tsx:101` | "terakhir dipakai 3 hari lalu" |
| Fake server `fetch` | `excalidraw-app/tests/dashboard.test.tsx:65` | test dialog |

## Test Strategy

**Integration (Go, `tokens_test.go`, Postgres sementara di port 55499):**

- Tanpa session → 401. Tanpa header CSRF → 403.
- Password salah → 403. Setelah berkali-kali gagal → 429, dan limiter-nya sama dengan login.
- Create:
  - menghasilkan `secret` dengan prefix `exa_`
  - `GET` tidak pernah memuat secret atau hash
  - secret itu **langsung bisa dipakai di `/api/mcp`** (`tools/list` → 200)
- Revoke:
  - token langsung ditolak di `/api/mcp` (401)
  - revoke kedua kalinya tetap 204
- **Isolasi:** Bob tidak bisa melihat (list) maupun me-revoke token Alice; revoke dibalas 404, dan token Alice tetap berlaku.
- **Batas:** token aktif ke-21 → 409. Token yang sudah di-revoke atau expired tidak dihitung.
- `expiresInDays` di luar daftar → 400.
- CLI tetap jalan: test store untuk `GetByEmail` + `Create`, dan `RevokeAny`.

**Vitest (`connectAI.test.tsx`):**

- Dialog terbuka dan daftar token tampil.
- Buat token: muncul panel dengan command Claude Code yang memuat origin dan token.
- Tutup dialog lalu buka lagi: token mentah sudah tidak ada.
- Revoke dengan konfirmasi.
- Password salah menampilkan pesan error dan **tidak** mengarahkan ke halaman login.

**Manual:** di lokal, login sebagai user kedua, buat token dari dialog, copy command, lalu jalankan `claude mcp add`. Cek `list_workspaces` hanya menampilkan workspace user itu.

## Design Principles Alignment

- **Accurate / Complete:**
  - Token mentah hanya muncul sekali.
  - Daftar token tetap memuat yang sudah di-revoke, untuk jejak audit.
  - Batas token aktif dicek dalam transaksi yang sama dengan insert, jadi dua request bersamaan tidak bisa melewati batas.
- **Secure:**
  - Setiap user hanya bisa mengakses tokennya sendiri, dan token user lain dibalas 404.
  - Ada perlindungan CSRF.
  - Membuat token wajib password, dengan rate limit login.
  - Secret tidak pernah di-log dan tidak disimpan di storage browser.
  - Revoke admin dipisah secara eksplisit dari revoke milik user.
- **Simple:**
  - Tanpa migrasi dan tanpa library UI baru.
  - Satu endpoint dipakai untuk web maupun curl.

**Tradeoff yang sengaja diambil:**

1. **Masih copy-paste token.** OAuth ditunda (lihat audit).
2. **Pilihan "tidak pernah expired" tetap ada.** Ini praktis untuk dipakai sendiri, tapi default-nya tetap 90 hari.
3. **Admin belum bisa melihat token semua user dari web.** Untuk itu tetap pakai CLI.

## Rollback Plan

Pakai `git revert`, lalu build dan deploy ulang. Tidak ada perubahan skema. Token yang dibuat lewat web tetap tersimpan di `api_tokens` dan tetap berlaku sampai di-revoke lewat CLI (`token list` / `token revoke`).

## Verification

1. `cd excalidraw-server && go vet ./... && TEST_DATABASE_URL=postgres://...:55499/... go test -race ./...`
2. `yarn test:typecheck`, ESLint, `yarn test:other`, `npx vitest run`
3. Lokal: `serve` + `yarn start`, lalu login, buka Connect AI, buat token, jalankan `claude mcp add` ke `localhost`, dan panggil `list_workspaces`.
4. Deploy ke VPS seperti biasa:
   - `pg_dump`, `git pull`, build di Mac, `docker load`, `up -d --no-build`
   - cek `draw.gemps.my.id` dan `medic-quickcomm` tetap 200
   - coba alurnya dari web

## Risks

| Risk | Mitigasi |
| --- | --- |
| Session dicuri lalu dipakai untuk membuat token berumur panjang | Password konfirmasi + rate limit. Cookie session sudah `HttpOnly` + `SameSite=Lax` + `Secure` |
| User menempel token di tempat publik (chat, repo) | Peringatan di dialog, default 90 hari, revoke satu klik. Catatan di README bahwa `~/.claude.json` berisi token |
| Teman membuat banyak sekali token | Maksimal 20 token aktif per user |
| Perubahan signature store mematahkan CLI | Test CLI/store + output CLI tidak berubah |
| Token baru memberi akses ke semua workspace user itu | Memang sesuai desain: token = user. Scope per workspace bisa ditambah kalau nanti dibutuhkan |

## Catatan implementasi

Bagian yang berbeda dari plan di atas:

- **Header CSRF di curl.** Contoh curl di atas (`X-Requested-With: curl`) salah. Server mewajibkan nilai persis `excalidraw` (`httpx.CSRFHeaderValue`), dan hal ini ketahuan saat contoh itu dicoba ke binary asli. Resep yang benar ada di `deploy/README.md` bagian 6. Resep itu membaca password dengan `read -s` dan menyusun JSON dengan `jq`.
- **Dialog.** Dialog dibuat dengan `div role="dialog" aria-modal`, bukan `<dialog>` native, karena jsdom (lingkungan test) tidak mendukung `showModal`. Dialog bisa ditutup dengan Esc atau klik di luar.
- **Tombol.** Tombol dialog di sidebar memakai ikon `MagicIcon` dengan label "Connect AI tools", dan letaknya di sebelah tombol logout.
- **Store.** `Revoke(ctx, userID, id)` hanya bisa me-revoke token milik user itu sendiri. `RevokeAny(ctx, id)` dipakai CLI admin.
- **Mutation test.** Pembatasan per user di `Revoke` sengaja dihapus sebentar, dan test isolasi langsung gagal. Jadi test itu benar-benar menjaga aturan tersebut.
- **Belum dicek visual di browser.** Claude in Chrome tidak tersedia di sesi ini. Perilaku dialog sudah dicakup vitest (`connectAI.test.tsx`).
