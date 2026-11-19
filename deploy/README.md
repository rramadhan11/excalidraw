# Deploy ke VPS

Excalidraw + board server, berdampingan dengan app lain yang sudah jalan di VPS.

```
Internet ──HTTPS──► Caddy yang sudah ada (+ 1 blok site baru)
                       │ reverse_proxy excalidraw-web:80  (lewat network Docker milik Caddy)
                       ▼
              web (nginx: frontend + /api proxy) ──► server (Go API) ──► db (Postgres milik excalidraw)
```

Saat ini di VPS, Caddy adalah bagian dari compose project `medic-quickcomm` (app assessment):

- Caddyfile ada di `/home/ubuntu/medic-quickcomm/Caddyfile`.
- Network-nya `medic-quickcomm_default`.

Excalidraw menumpang Caddy itu, tapi **punya Postgres sendiri**, supaya datanya tidak ikut terhapus kalau project lain di-`down -v`.

**Prinsip: app yang sudah jalan tidak boleh terganggu.**

- **Postgres app lain tidak disentuh sama sekali.**
- **Caddy tidak di-restart dan container-nya tidak dibuat ulang.** Caddy hanya ditambah satu blok site, divalidasi, lalu di-_reload_. Reload Caddy itu graceful: koneksi ke site lain tidak terputus, dan kalau config tidak valid Caddy tetap memakai config lama.
- **Container excalidraw tidak membuka port publik.** Hanya ada `127.0.0.1:8090` untuk cek lokal.
- **RAM dan CPU dibatasi** (`mem_limit`, `cpus`).
- **Image di-build di Mac**, karena VPS cuma punya 2 CPU dan build frontend akan memakai CPU penuh beberapa menit.

Perintah ditandai **(di Mac)** atau **(di VPS)**. Di Mac, VPS dapat dijangkau sebagai `ssh sumoagent`.

---

## 0. Cek kondisi VPS (read-only, aman) — (di VPS)

```bash
docker ps --format 'table {{.Names}}\t{{.Image}}\t{{.Ports}}'
ss -ltn | grep ':8090 ' || echo "port 8090 kosong"
free -h; df -h /
dig +short draw.gemps.my.id        # harus IP VPS
```

## 1. Ambil kode — (di VPS)

```bash
git clone --branch selfhost https://github.com/rramadhan11/excalidraw.git ~/excalidraw
```

## 2. Konfigurasi — (di VPS)

```bash
cd ~/excalidraw/deploy
cp .env.example .env && chmod 600 .env
sed -i "s/^POSTGRES_PASSWORD=.*/POSTGRES_PASSWORD=$(openssl rand -hex 24)/" .env
sed -i "s/^CADDY_NETWORK=.*/CADDY_NETWORK=medic-quickcomm_default/" .env
sed -i "s|^PUBLIC_URL=.*|PUBLIC_URL=https://draw.gemps.my.id|" .env
cat .env
```

`POSTGRES_PASSWORD` hanya dipakai saat database pertama kali dibuat. **Jangan diganti** setelah volume `excalidraw_pgdata` ada. Kalau diganti, server tidak bisa login ke database.

## 3. Build image & kirim ke VPS — (di Mac)

Jalankan dari root repo, di branch yang sama dengan yang di-clone di VPS. VPS-nya `x86_64`, jadi `--platform linux/amd64`. Proses build-nya sendiri berjalan native di Mac (frontend dan Go di-cross-compile), jadi tidak lambat.

```bash
docker buildx build --platform linux/amd64 -f deploy/web.Dockerfile -t excalidraw-web:local --load .
docker buildx build --platform linux/amd64 -t excalidraw-server:local --load excalidraw-server
docker save excalidraw-web:local excalidraw-server:local | gzip | ssh sumoagent 'gunzip | docker load'
```

Alternatifnya, build di VPS dengan `docker compose build`. Cara ini memakai CPU VPS penuh selama beberapa menit, jadi hindari selama app lain sedang dipakai.

## 4. Jalankan — (di VPS)

```bash
cd ~/excalidraw/deploy
docker compose -f docker-compose.yml -f docker-compose.caddy-network.yml up -d --no-build
docker compose ps               # db, server, web harus "healthy"
curl -fsS http://127.0.0.1:8090/api/healthz && echo    # {"status":"ok"}
docker compose logs server | head    # harus ada "migration applied" dan "listening"
```

Setelah ini, container `web` juga masuk network `medic-quickcomm_default` dengan nama `excalidraw-web`. Caddy belum mengarah ke sana, jadi belum ada yang berubah untuk app lain.

Lalu buat akun untuk dirimu, keluarga, atau teman. Password diketik di prompt:

```bash
docker compose exec server excalidraw-server user create --email kamu@example.com --name "Nama Kamu"
docker compose exec server excalidraw-server user list
```

Setiap user otomatis mendapat workspace pribadi.

## 5. Subdomain & HTTPS di Caddy — (di VPS)

Caddyfile di-mount ke container sebagai **satu file**. Editor yang menyimpan dengan cara mengganti file (vim, `sed -i`, dll.) akan membuat container tetap melihat isi lama. Karena itu blok baru **ditambahkan di akhir file** dengan `>>`, yang menulis ke file yang sama.

```bash
cd /home/ubuntu/medic-quickcomm
cp Caddyfile Caddyfile.bak-$(date +%F)

cat >> Caddyfile <<'EOF'

draw.gemps.my.id {
	encode zstd gzip
	reverse_proxy excalidraw-web:80
}
EOF

# container harus melihat blok baru
docker exec medic-quickcomm-caddy-1 tail -5 /etc/caddy/Caddyfile

# harus "Valid configuration"; kalau gagal, JANGAN reload (lihat di bawah)
docker exec medic-quickcomm-caddy-1 caddy validate --config /etc/caddy/Caddyfile --adapter caddyfile

docker exec medic-quickcomm-caddy-1 caddy reload --config /etc/caddy/Caddyfile --adapter caddyfile
```

Cek keduanya setelah reload:

```bash
curl -sSI https://medic-quickcomm.gemps.my.id | head -1     # app lama tetap jalan
curl -fsS https://draw.gemps.my.id/api/healthz && echo      # {"status":"ok"}
docker logs --since 2m medic-quickcomm-caddy-1 | grep -i draw.gemps   # status sertifikat
```

Caddy mengambil sertifikat Let's Encrypt untuk subdomain baru secara otomatis, biasanya dalam beberapa detik. Setelah itu buka `https://draw.gemps.my.id`, login, lalu buat board.

**Kalau `validate` gagal:** kembalikan isi file dengan `cat Caddyfile.bak-YYYY-MM-DD > Caddyfile` (tulis ke file yang sama, bukan `cp`/`mv`). Caddy masih memakai config lama, jadi tidak ada yang terganggu.

Catatan:

- Caddyfile ini di-track git di repo `medic-quickcomm`, jadi `git status` di sana akan menunjukkan perubahan. Biarkan saja, jangan di-commit ke repo assessment.
- Kalau project `medic-quickcomm` di-`docker compose down`, network-nya ikut dihapus. Selama container `web` masih menempel, perintah itu akan gagal. Jalankan `docker compose down` di excalidraw dulu.
- Setelah masa review selesai, sebaiknya Caddy dipindah ke compose sendiri (misalnya `~/edge`) sebagai reverse proxy bersama untuk semua subdomain, supaya tidak menumpang project assessment lagi.

## 6. MCP: akses dari AI tools (Claude Code, Codex, dll.)

Endpoint-nya `https://draw.gemps.my.id/api/mcp`, dan aksesnya pakai **token pribadi** per user. Token punya akses penuh seperti user pemiliknya (semua workspace-nya), jadi perlakukan seperti password.

### Buat token: dari web (cara utama, juga untuk teman)

Setiap user membuat token sendiri. Admin cukup membuatkan akunnya (langkah 4, `user create`).

1. Login di `https://draw.gemps.my.id`, lalu klik ikon **Connect AI tools** di bagian bawah sidebar (sebelah tombol logout).
2. Isi nama token (misalnya `Claude Code laptop`), pilih masa berlaku, masukkan password, lalu klik **Create token**.
3. Copy command yang muncul dan paste di terminal. Token hanya ditampilkan sekali.

Di dialog yang sama ada daftar token, kapan terakhir dipakai, dan tombol **Revoke**. Setiap user maksimal punya 20 token aktif.

### Buat token: dari terminal (curl)

Password dibaca dengan `read -s` supaya tidak masuk shell history. Header `X-Requested-With: excalidraw` wajib dikirim (proteksi CSRF).

```bash
B=https://draw.gemps.my.id; EMAIL=kamu@example.com
read -s "PW?Password: "; echo        # bash: read -s -p "Password: " PW; echo
H=(-H 'X-Requested-With: excalidraw' -H 'Content-Type: application/json')
JAR=$(mktemp)
curl -fsS -c "$JAR" "${H[@]}" $B/api/auth/login \
  -d "$(jq -n --arg e "$EMAIL" --arg p "$PW" '{email:$e,password:$p}')" >/dev/null
curl -fsS -b "$JAR" "${H[@]}" $B/api/tokens \
  -d "$(jq -n --arg p "$PW" '{name:"terminal",expiresInDays:90,password:$p}')" | jq -r .secret
rm -f "$JAR"; unset PW
```

`expiresInDays` boleh `30`, `90` (default kalau tidak diisi), `365`, atau `null` (tidak pernah expired). Untuk melihat dan me-revoke token: `GET /api/tokens` dan `DELETE /api/tokens/<id>`, dengan cookie login yang sama.

### Buat token: admin di VPS (CLI)

Untuk akun mana pun, tanpa password user. `token revoke` dari CLI bisa me-revoke token milik siapa saja.

```bash
cd ~/excalidraw/deploy
docker compose exec server excalidraw-server token create --email kamu@example.com --name "claude-code-mac" --expires-days 365
docker compose exec server excalidraw-server token list --email kamu@example.com
docker compose exec server excalidraw-server token revoke --id <TOKEN_ID>    # langsung tidak berlaku
```

Pasang di client **(di Mac)**:

- **Claude Code** menyimpan header-nya, termasuk token, di config-nya sendiri (`~/.claude.json`), jadi file itu ikut menjadi rahasia.
- **Codex** membaca token dari env var setiap kali jalan, sehingga token tidak tersimpan di config.

```bash
export EXCALIDRAW_TOKEN='exa_...'     # dari password manager; untuk Codex taruh di ~/.zshrc
claude mcp add --transport http excalidraw https://draw.gemps.my.id/api/mcp \
  --header "Authorization: Bearer $EXCALIDRAW_TOKEN"
```

```toml
# Codex: ~/.codex/config.toml
[mcp_servers.excalidraw]
url = "https://draw.gemps.my.id/api/mcp"
bearer_token_env_var = "EXCALIDRAW_TOKEN"
```

Cek cepat dari terminal (harus `200`):

```bash
curl -s -o /dev/null -w '%{http_code}\n' -X POST https://draw.gemps.my.id/api/mcp \
  -H "Authorization: Bearer $EXCALIDRAW_TOKEN" -H 'Content-Type: application/json' \
  -H 'Accept: application/json, text/event-stream' \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/list"}'
```

Lewat MCP, AI bisa membaca, mencari, mengatur, dan menggambar di board, tapi **tidak bisa menghapus board, collection, atau workspace**. Kalau board sedang terbuka di browser saat AI mengubahnya, autosave di tab itu berhenti dengan pesan "Changed elsewhere — reload". Reload tab-nya untuk melihat hasilnya.

---

## Backup — (di VPS)

Semua data board ada di database `excalidraw`, termasuk gambar dan thumbnail. Satu file dump sudah cukup:

```bash
mkdir -p ~/backups/excalidraw
cd ~/excalidraw/deploy
docker compose exec -T db pg_dump -U excalidraw -Fc excalidraw > ~/backups/excalidraw/excalidraw-$(date +%F).dump
```

Untuk backup harian (simpan 14 hari), tambahkan lewat `crontab -e`:

```cron
30 3 * * * cd $HOME/excalidraw/deploy && docker compose exec -T db pg_dump -U excalidraw -Fc excalidraw > $HOME/backups/excalidraw/excalidraw-$(date +\%F).dump && find $HOME/backups/excalidraw -name '*.dump' -mtime +14 -delete
```

Restore:

```bash
docker compose exec -T db pg_restore -U excalidraw -d excalidraw --clean --if-exists < ~/backups/excalidraw/excalidraw-YYYY-MM-DD.dump
```

## Update ke versi baru

```bash
# (di VPS) backup dulu
cd ~/excalidraw/deploy
docker compose exec -T db pg_dump -U excalidraw -Fc excalidraw > ~/backups/excalidraw/pre-update-$(date +%F-%H%M).dump
git pull

# (di Mac) ulangi langkah 3

# (di VPS)
docker compose -f docker-compose.yml -f docker-compose.caddy-network.yml up -d --no-build
```

Hanya container excalidraw yang di-recreate, dan downtime-nya beberapa detik. Migrasi database jalan otomatis saat server start.

## Rollback

Checkout commit sebelumnya di Mac dan VPS, ulangi langkah 3, lalu `up -d --no-build`.

Kalau versi baru membawa migrasi, versi lama mungkin tidak cocok dengan skemanya. Restore dump `pre-update-*` dulu (lihat Backup), baru jalankan versi lama.

## Mematikan / menghapus

```bash
cd ~/excalidraw/deploy
docker compose -f docker-compose.yml -f docker-compose.caddy-network.yml down   # data tetap ada di volume excalidraw_pgdata
```

Hapus blok `draw.gemps.my.id` dari Caddyfile (edit di tempat), lalu validate dan reload seperti langkah 5. `down -v` juga **menghapus semua board**.

## Troubleshooting

| Gejala | Cek |
| --- | --- |
| `server` tidak healthy | `docker compose logs server db`. Biasanya `POSTGRES_PASSWORD` diganti setelah volume database dibuat |
| 502 dari Caddy | `docker network inspect medic-quickcomm_default` harus memuat container `excalidraw-web-1`. Kalau tidak ada, jalankan lagi `up` dengan kedua file compose |
| Login berhasil tapi langsung keluar lagi | Situs harus dibuka lewat **https**, karena cookie login di-set `Secure` |
| Sertifikat belum terbit | `docker logs medic-quickcomm-caddy-1`. DNS harus mengarah ke VPS dan port 80/443 terbuka |
| Upload gambar besar gagal | Batas per gambar 4 MiB, sama dengan batas Excalidraw |
| MCP membalas 401 | Token salah, sudah di-revoke, atau expired: cek `token list`. Header harus `Authorization: Bearer exa_...` |
| Link board dari MCP tidak lengkap (`/board/...`) | `PUBLIC_URL` di `deploy/.env` belum diisi; isi lalu `up -d` lagi |
