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
docker compose run --rm --no-deps awg-forge init \
  --server-host vpn.example.com \
  --external-interface eth0 \
  --profile awg_2_0 \
  --tunnel-name awg20 \
  --listen-port 51830 \
  --ipv4-subnet 10.20.0.0/24
docker compose run --rm --no-deps awg-forge db migrate
docker compose up -d
```

Replace the example host, interface, port, and subnet before running `init`. This creates the first persistent tunnel in `data/state.json`; changing legacy tunnel variables in `.env` afterwards does not update it.

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
docker compose -f docker-compose.bridge.yml run --rm --no-deps awg-forge init \
  --server-host vpn.example.com \
  --external-interface eth0 \
  --profile awg_2_0 \
  --tunnel-name awg20 \
  --listen-port 51830 \
  --ipv4-subnet 10.20.0.0/24
docker compose -f docker-compose.bridge.yml run --rm --no-deps awg-forge db migrate
docker compose -f docker-compose.bridge.yml up -d
```

For bridge networking, choose a `--listen-port` from the published UDP range.

The bridge example publishes:

- Web UI: `127.0.0.1:51821:51821/tcp`;
- tunnel UDP ports: `51820-51840:51820-51840/udp`.

This example is for loopback HTTP or SSH-tunnel access. To use built-in `acme-domain` or `acme-ip` TLS in bridge mode, also publish the HTTP-01 listener and allow TCP/80 through the host firewall and provider security group:

```yaml
ports:
  - "80:80/tcp"
```

TCP/80 is used only for ACME validation; the Web UI remains on `WEBUI_PORT`.

In bridge mode, keep tunnel listen ports inside `51820-51840` unless you update the compose file and recreate the container.

Set:

```env
PUBLISHED_UDP_PORTS=51820-51840
TUNNEL_UDP_PORT_RANGE=51820-51840
```

Automatic selection uses only the overlap between `TUNNEL_UDP_PORT_RANGE` and `PUBLISHED_UDP_PORTS`. The example keeps the existing published range for backward compatibility.

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
