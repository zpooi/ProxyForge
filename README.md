# ProxyForge

ProxyForge manages a pool of Cloudflare WARP accounts and exposes them through one proxy port.

## Pool behavior

- Set `Proxy IP count` in Settings. This creates fixed credentials like `pf-001:<password>@host:7843`.
- ProxyForge automatically keeps a larger WARP candidate pool: `proxy IP count + max(3, ceil(proxy IP count / 2))`.
- The default proxy IP count is 20, so the default target WARP pool is 30 accounts.
- Periodic runs test latency, packet loss, speed, and public IP.
- Test results include Cloudflare colo and country code from `cdn-cgi/trace`.
- Duplicate public IPs are disabled except for the best scoring keeper.
- Fixed proxy slots keep their username/password and stable egress IP. If the egress IP drifts, ProxyForge first restarts the bound tunnel to recover the pinned IP. It only rebinds after repeated failures or drift.

## Local Run

Windows PowerShell:

```powershell
$env:DB_PATH="data.db"
$env:LISTEN_ADDR=":7800"
node frontend/scripts/build.mjs
go run ./backend/cmd/proxyforge
```

Linux/macOS:

```bash
node frontend/scripts/build.mjs
DB_PATH=./data.db LISTEN_ADDR=:7800 go run ./backend/cmd/proxyforge
```

Open `http://127.0.0.1:7800`. On a fresh database, ProxyForge opens a setup page where you create the management username and password. The proxy listener defaults to port `7843`.

The management UI is written in Svelte. Its source lives in `frontend`, and its build output is written directly to `backend/web` and embedded into the Go binary, so deployment only needs the Go executable.

## Docker

Single-platform local image:

```bash
docker buildx build --platform linux/amd64 -t proxyforge:latest --load .
docker run -d --name proxyforge \
  -p 7800:7800 -p 7843:7843 \
  -v proxyforge-data:/data \
  proxyforge:latest
```

Multi-architecture image:

```bash
docker buildx build \
  --platform linux/amd64,linux/arm64 \
  -t your-registry/proxyforge:latest \
  --push .
```

Runtime environment variables:

- `DB_PATH`: SQLite database path, default `/data/data.db` in Docker.
- `LISTEN_ADDR`: web UI listen address, default `:7800`.
- `PROJECT_ROOT`: optional import root for old `warp-accounts` folders.

The WARP tunnels use userspace WireGuard netstack, so Docker does not need a kernel TUN device.
