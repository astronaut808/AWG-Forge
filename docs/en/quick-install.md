# Quick Install

`install.sh` is an interactive installer for a fresh Linux/VPS server. It creates `.env`, prepares `data/`, starts Docker Compose, and prints the next steps.

```bash
curl -fsSL https://raw.githubusercontent.com/astronaut808/awg-forge/master/install.sh | sudo bash
```

By default, it installs into:

```text
/opt/awg-forge
```

You can choose a custom path:

```bash
curl -fsSL https://raw.githubusercontent.com/astronaut808/awg-forge/master/install.sh | sudo AWG_FORGE_HOME=/srv/awg-forge bash
```

If the repository is already cloned, you can run the local file:

```bash
./install.sh
```

## What It Does

- checks Linux, Docker, Docker Compose, and `/dev/net/tun`;
- detects the external interface with `ip route get 1.1.1.1`;
- suggests `SERVER_HOST` from the detected source IP, while allowing a custom domain;
- asks for the tunnel UDP port, Web UI host/port, subnet, DNS, MTU, and protocol profile;
- generates `PASSWORD` and `SESSION_SECRET`;
- creates `.env` with `0600` permissions;
- creates `data/` with `0700` permissions;
- creates `docker-compose.yml` if it does not exist;
- uses the host networking Compose file;
- runs `docker compose up -d`;
- runs `docker exec awg-forge awg-forge doctor`;
- prints the password, `.env` path, and SSH tunnel command.

If `.env` already exists, the installer creates a backup:

```text
.env.backup-YYYYMMDD-HHMMSS
```

## Security

By default, the Web UI binds to `127.0.0.1`, and access goes through an SSH tunnel:

```bash
ssh -L 51821:127.0.0.1:51821 user@server
```

If you choose `WEBUI_HOST=0.0.0.0` or `::`, the script shows a warning and requires explicit confirmation. Use public binds only behind a firewall, VPN, or reverse proxy.

The password is shown at the end and stored in `/opt/awg-forge/.env`, or in `.env` inside `AWG_FORGE_HOME`:

```env
PASSWORD=...
```

## After Install

Open the UI, create a client, import the `.conf` into AmneziaVPN, and check IPv4 egress:

```bash
curl -4 https://ifconfig.co
```

Useful commands:

```bash
docker compose ps
docker compose logs -f
docker exec awg-forge awg-forge doctor
```
