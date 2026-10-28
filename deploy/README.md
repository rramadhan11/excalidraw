# Deploy ke VPS

Excalidraw + board server, berdampingan dengan app lain yang sudah jalan di VPS.

```
Internet ──HTTPS──► Caddy (sudah ada, + 1 site baru)
                       │ reverse_proxy 127.0.0.1:8090
                       ▼
              web (nginx: frontend + /api proxy) ──► server (Go API) ──► Postgres yang sudah ada
                                                                        (database & role baru)
```

**Prinsip: app yang sudah jalan tidak boleh terganggu.**

- **Postgres yang ada tidak di-restart.** Hanya dibuatkan database dan role baru lewat `psql`.
- **Caddy tidak diganti.** Caddy hanya ditambah satu blok site, lalu di-_reload_. Reload Caddy itu graceful: koneksi ke site lain tidak terputus. Kalau config baru tidak valid, Caddy tetap memakai config lama.
- **Container excalidraw hanya listen di `127.0.0.1`.** Akses publiknya lewat Caddy.
- **RAM server dibatasi** (`mem_limit`) supaya tidak berebut dengan app lain.
- **Build image tidak menyentuh container yang sedang jalan**, tapi build frontend makan CPU dan RAM besar selama beberapa menit (lihat langkah 4).

Semua perintah di bawah dijalankan di VPS, kecuali yang ditandai **(di Mac)**.

---

## 0. Cek kondisi VPS (read-only, aman)

Perintah-perintah ini hanya membaca, tidak mengubah apa pun.

```bash
docker ps --format 'table {{.Names}}\t{{.Image}}\t{{.Ports}}'   # cari nama container Postgres & Caddy
uname -m          # x86_64 = amd64, aarch64 = arm64 (dipakai di langkah 4B)
nproc; free -h    # CPU dan RAM yang tersedia
df -h /           # sisa disk (image + build butuh sekitar 3 GB)
ss -ltnp | grep ':8090 ' || echo "port 8090 kosong"
which caddy || echo "caddy bukan di host (kemungkinan di Docker)"
```

Lalu cek Postgres. Ganti `<pg>` dengan nama container Postgres:

```bash
docker exec <pg> printenv POSTGRES_USER                  # superuser-nya (sering "postgres")
docker exec <pg> psql -U <superuser> -tAc 'show server_version'    # wajib >= 13
docker inspect <pg> --format '{{range $k, $v := .NetworkSettings.Networks}}{{$k}} {{end}}'   # nama network
```

## 1. Ambil kode

```bash
sudo mkdir -p /opt/excalidraw && sudo chown $USER /opt/excalidraw
git clone git@github.com:rramadhan11/excalidraw.git /opt/excalidraw
cd /opt/excalidraw && git checkout selfhost
```

## 2. Buat database & role di Postgres yang sudah ada

Langkah ini tidak me-restart Postgres dan tidak menyentuh database lain. Ganti password dengan yang acak, misalnya dari `openssl rand -base64 24 | tr -d '/+='`:

```bash
docker exec -i <pg> psql -v ON_ERROR_STOP=1 -U <superuser> <<'SQL'
CREATE ROLE excalidraw LOGIN PASSWORD 'GANTI_PASSWORD_ACAK';
CREATE DATABASE excalidraw OWNER excalidraw;
SQL
```

Role `excalidraw` bukan superuser dan hanya menjadi pemilik database `excalidraw`. Tabel-tabelnya dibuat otomatis oleh server saat pertama kali start.

## 3. Konfigurasi

```bash
cd /opt/excalidraw/deploy
cp .env.example .env && chmod 600 .env
nano .env
```

| Variabel | Isi |
| --- | --- |
| `POSTGRES_NETWORK` | nama network dari langkah 0 |
| `DATABASE_URL` | `postgres://excalidraw:<password>@<pg>:5432/excalidraw?sslmode=disable`, dengan `<pg>` = nama container Postgres |
| `WEB_PORT` | `8090`, atau port lain yang kosong |
| `CADDY_NETWORK` | hanya kalau Caddy berjalan di Docker (langkah 6B) |

## 4. Build image

Pilih salah satu cara.

### 4A. Build di VPS

Build frontend butuh sekitar **3 GB RAM bebas** dan memakai CPU penuh selama beberapa menit. Kalau `free -h` menunjukkan _available_ < 3 GB, atau app assessment sedang ramai dipakai, gunakan **4B**.

```bash
cd /opt/excalidraw/deploy
docker compose build        # container yang sedang jalan tidak disentuh
```

### 4B. Build di Mac, kirim ke VPS (VPS tidak terbebani)

**(di Mac)**, dari root repo. Sesuaikan `--platform` dengan `uname -m` di VPS (`linux/amd64` untuk x86_64, `linux/arm64` untuk aarch64):

```bash
docker buildx build --platform linux/amd64 -f deploy/web.Dockerfile -t excalidraw-web:local --load .
docker buildx build --platform linux/amd64 -t excalidraw-server:local --load excalidraw-server
docker save excalidraw-web:local excalidraw-server:local | gzip | ssh user@vps 'gunzip | docker load'
```

Di langkah 5, pakai `--no-build` supaya VPS memakai image kiriman ini.

## 5. Jalankan

```bash
cd /opt/excalidraw/deploy
docker compose up -d            # atau: docker compose up -d --no-build (setelah 4B)
docker compose ps               # server dan web harus "healthy"
curl -fsS http://127.0.0.1:8090/api/healthz && echo    # {"status":"ok"}
docker compose logs server | head    # harus ada "migration applied" dan "listening"
```

Lalu buat akun untuk dirimu, keluarga, atau teman. Password diketik di prompt:

```bash
docker compose exec server excalidraw-server user create --email kamu@example.com --name "Nama Kamu"
docker compose exec server excalidraw-server user list
```

Setiap user otomatis mendapat workspace pribadi.

## 6. Subdomain & HTTPS di Caddy

1. Arahkan DNS dulu: buat **A record** `draw.domainmu.com` → IP VPS. Cek dengan `dig +short draw.domainmu.com`.
2. **Backup Caddyfile** sebelum mengedit:
   ```bash
   sudo cp /etc/caddy/Caddyfile /etc/caddy/Caddyfile.bak-$(date +%F)
   ```
3. Tambahkan blok baru di **akhir** Caddyfile. Blok lain jangan diubah.

### 6A. Caddy di host (systemd)

```caddyfile
draw.domainmu.com {
	encode zstd gzip
	reverse_proxy 127.0.0.1:8090
}
```

```bash
sudo caddy validate --config /etc/caddy/Caddyfile --adapter caddyfile   # harus "Valid configuration"
sudo systemctl reload caddy                                             # reload, BUKAN restart
```

### 6B. Caddy di dalam Docker

Container Caddy tidak bisa menjangkau `127.0.0.1` milik host. Karena itu container `web` ikut dimasukkan ke network Caddy:

```bash
docker inspect <caddy> --format '{{range $k, $v := .NetworkSettings.Networks}}{{$k}} {{end}}'
# isi CADDY_NETWORK di deploy/.env dengan nama network itu, lalu:
docker compose -f docker-compose.yml -f docker-compose.caddy-network.yml up -d
```

Blok Caddyfile-nya (Caddyfile biasanya di-mount dari host; cek dengan `docker inspect <caddy>` di bagian `Mounts`):

```caddyfile
draw.domainmu.com {
	encode zstd gzip
	reverse_proxy excalidraw-web:80
}
```

```bash
docker exec <caddy> caddy validate --config /etc/caddy/Caddyfile --adapter caddyfile
docker exec <caddy> caddy reload --config /etc/caddy/Caddyfile --adapter caddyfile
```

**Kalau `validate` gagal, jangan reload.** Perbaiki dulu, atau kembalikan backup-nya. Site yang sudah ada tetap jalan dengan config lama. Setelah reload, cek juga bahwa subdomain lama (app assessment) masih bisa dibuka.

Caddy otomatis mengurus sertifikat Let's Encrypt untuk subdomain baru. Buka `https://draw.domainmu.com`, login, lalu buat board.

---

## Backup

Data board ada di database `excalidraw`, termasuk gambar dan thumbnail. Satu file dump sudah cukup:

```bash
sudo mkdir -p /opt/backups/excalidraw
docker exec <pg> pg_dump -U excalidraw -Fc excalidraw > /opt/backups/excalidraw/excalidraw-$(date +%F).dump
```

Untuk backup harian (simpan 14 hari), tambahkan lewat `crontab -e`:

```cron
30 3 * * * docker exec <pg> pg_dump -U excalidraw -Fc excalidraw > /opt/backups/excalidraw/excalidraw-$(date +\%F).dump && find /opt/backups/excalidraw -name '*.dump' -mtime +14 -delete
```

Restore (hanya database excalidraw yang ditimpa):

```bash
docker exec -i <pg> pg_restore -U excalidraw -d excalidraw --clean --if-exists < /opt/backups/excalidraw/excalidraw-YYYY-MM-DD.dump
```

## Update ke versi baru

```bash
docker exec <pg> pg_dump -U excalidraw -Fc excalidraw > /opt/backups/excalidraw/pre-update-$(date +%F-%H%M).dump
cd /opt/excalidraw && git pull
cd deploy && docker compose build && docker compose up -d   # atau cara 4B + --no-build
```

Hanya container excalidraw yang di-recreate, dan downtime-nya beberapa detik. Migrasi database jalan otomatis saat server start.

## Rollback

```bash
cd /opt/excalidraw && git checkout <commit-sebelumnya>
cd deploy && docker compose build && docker compose up -d
```

Kalau versi baru membawa migrasi, versi lama mungkin tidak cocok dengan skemanya. Restore dump `pre-update-*` dulu (lihat Backup), baru jalankan versi lama.

## Mematikan / menghapus

```bash
cd /opt/excalidraw/deploy && docker compose down   # hanya container excalidraw; data di Postgres tetap ada
```

Blok di Caddyfile dihapus lalu di-reload dengan cara yang sama.

## Troubleshooting

| Gejala | Cek |
| --- | --- |
| `server` tidak healthy | `docker compose logs server`. Biasanya `DATABASE_URL` salah, atau `POSTGRES_NETWORK` bukan network container Postgres |
| Login berhasil tapi langsung keluar lagi | Situs harus dibuka lewat **https**, karena cookie login di-set `Secure` |
| 502 dari Caddy | Pastikan `curl 127.0.0.1:8090` jalan di VPS (6A), atau container web sudah masuk network Caddy (6B) |
| Upload gambar besar gagal | Batas per gambar 4 MiB, sama dengan batas Excalidraw |
