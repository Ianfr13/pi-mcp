# pi-dashboard deploy (systemd user unit)

The dashboard is read-only and binds the host's **Tailscale IP** (`100.x.y.z:7777`).
It must run as the **same user** as the pi-mcp server so `~/.local/state/pi-mcp/registry.json`
resolves identically.

## Install

```bash
# 1. Build + install the binary
go build -o /usr/local/bin/pi-dashboard ./cmd/pi-dashboard   # may need sudo for /usr/local/bin

# 2. Install the user unit
mkdir -p ~/.config/systemd/user
cp deploy/pi-dashboard.service ~/.config/systemd/user/

# 3. Survive logout / start at boot (no root login needed)
loginctl enable-linger "$USER"

# 4. Enable + start
systemctl --user daemon-reload
systemctl --user enable --now pi-dashboard
```

## Verify / operate

```bash
systemctl --user status pi-dashboard
journalctl --user -u pi-dashboard -f      # logs (waits for tailnet, bind, etc.)
```

Open `http://<tailscale-ip>:7777/` from any device on your tailnet
(`tailscale ip -4` shows the IP). HTTP is plain on the tailnet — tailnet
membership is the access boundary (no app-level auth).

## Update

```bash
go build -o /usr/local/bin/pi-dashboard ./cmd/pi-dashboard
systemctl --user restart pi-dashboard
```

## Notes

- On boot the service logs `waiting for tailnet IP…` until tailscaled is up, then
  binds; it never falls back to a LAN/public interface.
- Override the bind with `ExecStart=/usr/local/bin/pi-dashboard --addr 100.x.y.z:7777`
  if auto-detection ever fails.
