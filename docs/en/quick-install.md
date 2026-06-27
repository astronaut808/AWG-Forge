# Quick Install

`install.sh` is an interactive installer for a fresh Linux/VPS server. It creates runtime `.env`, prepares `data/`, bootstraps the first tunnel into `state.json`, starts Docker Compose, and prints the next steps.

Install [Docker Engine from the official documentation](https://docs.docker.com/engine/install/) first. If Docker or Docker Compose is unavailable, the installer exits before creating `/opt/awg-forge` or any project files.

```bash
curl -fsSL https://raw.githubusercontent.com/astronaut808/awg-forge/master/install.sh -o install.sh
chmod +x install.sh
sudo ./install.sh
```

Downloading the script first is recommended for interactive installs. In some `curl | sudo bash` TTY/sudo environments the prompt can appear stuck because the script body and interactive answers use different input streams.

By default, it installs into:

```text
/opt/awg-forge
```

You can choose a custom path:

```bash
curl -fsSL https://raw.githubusercontent.com/astronaut808/awg-forge/master/install.sh -o install.sh
chmod +x install.sh
sudo AWG_FORGE_HOME=/srv/awg-forge ./install.sh
```

If the repository is already cloned, you can run the local file:

```bash
./install.sh
```

## What It Does

- checks Linux, Docker, Docker Compose, and `/dev/net/tun`;
- detects an existing install on repeated runs and offers reconfigure or full reinstall;
- offers to remove old AWG-like runtime interfaces, such as `awg0`, `awg0-1`, `awg15`, or `awg20`;
- detects the external interface with `ip route get 1.1.1.1`;
- on fresh installs, suggests the first tunnel endpoint host from the detected source IP, while allowing a custom domain;
- on fresh installs, asks for the protocol profile first, then tunnel UDP port, Web UI host/port, subnet, DNS, and MTU;
- defaults the first tunnel profile to AmneziaWG 2.0 when you press Enter;
- generates `PASSWORD` and `SESSION_SECRET`;
- creates runtime `.env` with `0600` permissions;
- creates `data/` with `0700` permissions;
- creates one-time `data/bootstrap.env`, which awg-forge consumes into `data/state.json` on first startup;
- creates `docker-compose.yml` if it does not exist;
- uses the host networking Compose file;
- runs `docker compose up -d`;
- runs `docker exec awg-forge awg-forge doctor`;
- prints the password, `.env` path, and SSH tunnel command.

If `.env` already exists, the installer creates a backup:

```text
.env.backup-YYYYMMDD-HHMMSS
```

## Repeated Runs and Full Reinstall

If the working directory already contains `.env`, `data/`, or `docker-compose.yml`, the installer asks what to do:

```text
1) Reconfigure existing install, keep data and backup .env
2) Full reinstall, backup and remove old data/config first
3) Abort
```

`Reconfigure` keeps `data/`, backs up the old `.env`, and recreates the container with the new runtime environment. Existing tunnels remain in `data/state.json` and are not rebuilt from `.env`.

`Full reinstall` first saves the current files into a directory like:

```text
reinstall-backup-YYYYMMDD-HHMMSS/
```

It then stops the container, removes managed firewall rules, AWG runtime interfaces, `.env`, `data/`, and `docker-compose.yml`, and continues as a fresh install.

Important: after a full reinstall, old client configs no longer match the server because state, keys, and tunnel parameters are recreated. Issue fresh `.conf` files to clients.

## Old Tunnel Variables In `.env`

Older awg-forge versions stored first-tunnel fields in `.env`, such as `SERVER_HOST`, `LISTEN_PORT`, `IPV4_SUBNET`, `DNS`, `ALLOWED_IPS`, `PERSISTENT_KEEPALIVE`, `MTU`, and `PROTOCOL_PROFILE`.

Current versions use `.env` only for runtime settings after `state.json` exists. If Doctor warns about legacy tunnel env variables, verify tunnel settings in the Web UI and then remove those old lines from `.env`.

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

## Uninstall

If you need to remove awg-forge, run uninstall while `data/state.json` still exists. This lets the script remove exact managed firewall rules for each tunnel.

```bash
cd /opt/awg-forge
sudo ./uninstall.sh
```

Remove the container, runtime interfaces, firewall rules, and local install files:

```bash
cd /opt/awg-forge
sudo ./uninstall.sh --purge
```

Preview all actions without stopping the container or modifying the host:

```bash
sudo ./uninstall.sh --dry-run --yes
```

By default, the script removes only interfaces and firewall rules that can be matched to tunnels in `data/state.json`. If state is already missing, unknown `awg*` interfaces are preserved to avoid deleting an unrelated AmneziaWG tunnel.

After reviewing those interfaces, remove them explicitly:

```bash
sudo ./uninstall.sh --remove-orphans
```

For a no-clone install:

```bash
curl -fsSL https://raw.githubusercontent.com/astronaut808/awg-forge/master/uninstall.sh | sudo bash
```
