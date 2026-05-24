# Setup

## Host Networking

Host networking is the recommended production mode for awg-forge. In this mode, tunnels created in the UI can use any free UDP ports without changing Docker port mappings.

Interactive quick start:

```bash
curl -fsSL https://raw.githubusercontent.com/astronaut808/awg-forge/master/install.sh -o install.sh
chmod +x install.sh
sudo ./install.sh
```

More details: [Quick install](quick-install.md).

Manual setup:

```bash
cp .env.example .env
mkdir -p data
docker compose up -d
```

By default the Web UI listens on `127.0.0.1:51821`. Access it through an SSH tunnel:

```bash
ssh -L 51821:127.0.0.1:51821 user@server
```

Then open:

```text
http://127.0.0.1:51821
```

When using `network_mode: host`, do not add `ports:` to `docker-compose.yml`.

## Bridge Networking

Bridge networking can work, but UDP ports must be published before the container starts. Since awg-forge lets you create tunnels in the UI, publish a fixed UDP range and create tunnels only inside that range.

```bash
cp .env.example .env
mkdir -p data
docker compose -f docker-compose.bridge.yml up -d
```

The bridge example publishes:

- Web UI: `127.0.0.1:51821:51821/tcp`;
- tunnel UDP ports: `51820-51840:51820-51840/udp`.

In bridge mode, keep tunnel listen ports inside `51820-51840` unless you update the compose file and recreate the container.

Set:

```env
PUBLISHED_UDP_PORTS=51820-51840
```

This lets the UI and `doctor` warn when a tunnel uses an unpublished UDP port.

## Startup Check

```bash
docker compose ps
docker exec awg-forge awg-forge doctor
```

If the UI is unavailable, check:

- SSH tunnel;
- `WEBUI_HOST`;
- `WEBUI_PORT`;
- `docker compose logs -f`.
