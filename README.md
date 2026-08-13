# kidandcat.com

Personal landing — dark Shinkai-dusk / game-HUD style (Japanese ma 間). Self-contained HTML.

- **Live:** https://kidandcat.com
- **Host:** vps2 → `/var/www/kidandcat` (Caddy)
- **Japón 2027:** https://kidandcat.com/2027 (source notes in `~/japan/viaje-2027.md`)
- **File drop:** https://kidandcat.com/up — any file up to 1 GiB, stored on vps2 at `/var/lib/kidandcat-up` (not served back). Unit `kidandcat-up.service`.

Deploy:

```bash
scp index.html vps2:/tmp/kidandcat-index.html
ssh vps2 'sudo cp /tmp/kidandcat-index.html /var/www/kidandcat/index.html'

# trip plan
scp 2027/index.html vps2:/tmp/kidandcat-2027.html
ssh vps2 'sudo mkdir -p /var/www/kidandcat/2027 && sudo cp /tmp/kidandcat-2027.html /var/www/kidandcat/2027/index.html'

# /up
cd up && GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o kidandcat-up .
scp kidandcat-up kidandcat-up.service vps2:/tmp/
ssh vps2 'sudo install -m 755 /tmp/kidandcat-up /opt/kidandcat-up/kidandcat-up && sudo cp /tmp/kidandcat-up.service /etc/systemd/system/kidandcat-up.service && sudo systemctl daemon-reload && sudo systemctl restart kidandcat-up'

# list received files
ssh vps2 'ls -lh /var/lib/kidandcat-up'
```
