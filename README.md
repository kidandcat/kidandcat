# kidandcat.com

Personal landing — dark Shinkai-dusk / game-HUD style (Japanese ma 間). Self-contained HTML.

- **Live:** https://kidandcat.com
- **Host:** vps2 → `/var/www/kidandcat` (Caddy)
- **Japón 2027:** https://kidandcat.com/2027 (source notes in `~/japan/viaje-2027.md`)

Deploy:

```bash
scp index.html vps2:/tmp/kidandcat-index.html
ssh vps2 'sudo cp /tmp/kidandcat-index.html /var/www/kidandcat/index.html'

# trip plan
scp 2027/index.html vps2:/tmp/kidandcat-2027.html
ssh vps2 'sudo mkdir -p /var/www/kidandcat/2027 && sudo cp /tmp/kidandcat-2027.html /var/www/kidandcat/2027/index.html'
```
