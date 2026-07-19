# Quick Install

`install.sh` is an interactive installer for a fresh Linux/VPS server. It creates runtime `.env`, prepares `data/`, initializes the first tunnel into `state.json`, starts Docker Compose, and prints the next steps.

Install [Docker Engine from the official documentation](https://docs.docker.com/engine/install/) first. If Docker or Docker Compose is unavailable, the installer exits before creating `/opt/awg-forge` or any project files.

```bash
curl -fsSL https://raw.githubusercontent.com/astronaut808/awg-forge/master/install.sh -o install.sh
chmod +x install.sh
sudo ./install.sh
```

Downloading the script first is recommended for interactive installs. In some `curl | sudo bash` TTY/sudo environments the prompt can appear stuck because the script body and interactive answers use different input streams.

To test a non-release image, pass `IMAGE`:

```bash
sudo IMAGE=ghcr.io/astronaut808/awg-forge:test ./install.sh
```

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
- enables SQLite and applies its initial schema before the service starts;
- creates `data/` with `0700` permissions;
- before starting the service, runs a one-shot `docker run ... init` command that creates `data/state.json` with the first tunnel;
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
3) Upgrade image, keep data and run required database migrations
4) Abort
```

`Reconfigure` keeps `data/`, backs up the old `.env`, updates only the runtime values selected by the installer, and recreates the container. Existing operational settings such as SQLite, TLS, and trusted-proxy configuration remain unchanged. Existing tunnels remain in `data/state.json` and are not rebuilt from `.env`.

`Full reinstall` first saves the current files into a directory like:

```text
reinstall-backup-YYYYMMDD-HHMMSS/
```

It then stops the container, removes managed firewall rules, AWG runtime interfaces, `.env`, `data/`, and `docker-compose.yml`, and continues as a fresh install.

Important: after a full reinstall, old client configs no longer match the server because state, keys, and tunnel parameters are recreated. Issue fresh `.conf` files to clients.

## Upgrade

For a managed installation using the standard `.env`, `docker-compose.yml`, and `./data` layout, update with:

```bash
sudo docker exec awg-forge awg-forge doctor
curl -fsSL https://raw.githubusercontent.com/astronaut808/awg-forge/master/install.sh -o install.sh
chmod +x install.sh
sudo AWG_FORGE_HOME=/opt/awg-forge ./install.sh upgrade
sudo docker exec awg-forge awg-forge doctor
```

The first command shows the pre-upgrade state; the last checks it afterwards. Download the current `install.sh` before every upgrade: it contains the compatibility checks and migrations for the current version. The script pulls the target image, stops the current container, backs up `.env` and `data/`, applies SQLite migrations before the new container starts, then checks that the container is running and verifies `db status`. It also prints Doctor output for review. If SQLite is currently off, it asks whether to enable it; the default is `No`. If SQLite is enabled but its database file is missing, it requires confirmation before creating a new empty database. A failed migration, failed container start, or failed database status check restores the backup and the previous image.

Set `AWG_FORGE_HOME` for another installation directory: `sudo AWG_FORGE_HOME=/srv/awg-forge ./install.sh upgrade`. When `./install.sh` runs from an existing managed installation directory, it also offers this upgrade path in its action menu. Custom Compose files, `CONFIG_DIR`, or database paths outside `./data` require a manual upgrade so the operator can back up the correct volumes.

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

Open the UI, create a client, open `Config`, import through AmneziaVPN QR or a downloaded `.conf`, and check IPv4 egress:

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
