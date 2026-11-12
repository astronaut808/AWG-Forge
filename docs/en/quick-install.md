# Quick Install

`install.sh` is an interactive installer for a fresh Linux/VPS server. It creates runtime `.env`, prepares `data/`, initializes the first tunnel into `state.json`, starts Docker Compose, and prints the next steps.

The installer offers to prepare missing dependencies on Ubuntu 22.04/24.04/26.04, Debian 12/13, CentOS Stream 9/10 and RHEL 8/9/10 (systemd, x86_64). It installs Docker Engine and the Compose plugin from Docker's official signed package repository when needed, plus `curl`, CA certificates, OpenSSL, `ip`/`ss`, `iptables`, `modprobe` and `awk`. Run it with `sudo`; installing packages and starting Docker require confirmation. An existing working Docker/Compose installation is reused, and conflicting container runtimes are never removed automatically. Other Linux distributions need these dependencies prepared manually. ARM64 images are not published yet.

TUN must be available at `/dev/net/tun`. The installer attempts `modprobe tun` and stops before creating project files if the VPS provider or kernel does not provide TUN. The AmneziaWG userspace runtime is bundled in the image; host AmneziaWG packages or a DKMS module are not required. Provider firewall rules and public UDP/TCP reachability remain the operator's responsibility.

On SELinux hosts (typically CentOS/RHEL), new installations label only the private `data/` volume using `:Z`. SELinux stays enabled. For an existing custom Compose file, configure the appropriate volume labels manually; the installer does not rewrite it. RHEL must have access to its normal package repositories (subscription or an equivalent mirror).

```bash
curl -fsSL https://raw.githubusercontent.com/astronaut808/awg-forge/master/install.sh -o install.sh
chmod +x install.sh
sudo ./install.sh
```

The `master` URL provides the current stable installer. Unreleased changes are accumulated in `develop`; use an explicit test image when testing them. Downloading the script first is recommended for interactive installs. In some `curl | sudo bash` TTY/sudo environments the prompt can appear stuck because the script body and interactive answers use different input streams.

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

- detects the distribution, offers missing dependencies, starts Docker when necessary, and checks Compose and `/dev/net/tun` before writing project files;
- detects an existing install on repeated runs and offers reconfigure or full reinstall;
- offers to remove old AWG-like runtime interfaces, such as `awg0`, `awg0-1`, `awg15`, or `awg20`;
- detects the external interface with `ip route get 1.1.1.1`;
- on fresh installs, suggests the first tunnel endpoint host from the detected source IP, while allowing a custom domain;
- on fresh installs, asks for the first tunnel's profile. Press Enter for AmneziaWG 2.0, or choose option `4` for experimental AmneziaWG 3.x with a compatible Docker image. Then choose a free UDP port automatically from `30000-49999` or enter one manually;
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

The first command checks the service before the upgrade, and the last checks it after startup. Use the installer from the current release so that its compatibility checks and migrations match the release.

After a successful upgrade, the installer shows a sign-in reminder. Use `admin` and your existing password, or use the username configured in `WEBUI_USERNAME`.

By default, the script pulls the image configured for the `awg-forge` service in Docker Compose. Set `IMAGE` to a version tag or digest to select a target for this upgrade, for example `sudo IMAGE=ghcr.io/astronaut808/awg-forge:VERSION AWG_FORGE_HOME=/opt/awg-forge bash ./install.sh upgrade` after replacing `VERSION`. Migration and startup use the inspected image ID. The Compose file keeps its configured image for subsequent Compose commands or upgrades without `IMAGE`.

The installer checks the running container, the resolved target Compose service, and host mount points before stopping the service. It uses the project name recorded on the existing container, including when the installation directory is reached through a symlink. It requires a local Docker daemon, Compose that produces resolved JSON and reports `env_file` in its config model, and the host tools `flock`, `findmnt`, `jq`, `timeout`, and `curl`. If Compose cannot provide these results, update it and retry. Installation, reconfiguration, upgrade, and uninstall use one lock per installation directory.

It stops the container, backs up `.env` and `data/`, and pins migration and startup to the inspected image ID. If copying fails, it attempts to restart the old container. After migration, it waits up to 45 seconds for repeated HTTP or HTTPS readiness responses without a restart. With SQLite enabled, the reported schema must match the migration result. For manual TLS, the local readiness probe accepts private or self-signed certificates and sends no credentials. Check certificate trust and hostname from a browser separately. Doctor remains a separate diagnostic check. Migration, startup, readiness, or SQLite failure triggers restoration of the previous image, `.env`, and data.

If SQLite is off, the installer offers to enable it. The default answer is `No`. If SQLite is enabled but its database file is missing, the installer asks before creating an empty database.

For another installation directory, set `AWG_FORGE_HOME` as in `sudo AWG_FORGE_HOME=/srv/awg-forge ./install.sh upgrade`. The installer also offers upgrade from an existing installation directory. Custom Compose overrides, mounts beyond the managed data bind and read-only `/lib/modules`, mount points at or below `./data`, linked data, and external config, database, or audit paths require a manual upgrade. Back up the relevant volumes first.

If rollback fails or an upgrade is interrupted after its backup, `.awg-forge-upgrade-pending` records the backup directory and blocks another installer or uninstall run. The backup contains `previous-image-id`, which identifies the image to use for recovery. Keep the service stopped, preserve the backup, the previous local image and any `.upgrade-restore.*` directory, and recover the previous image, `.env`, and data manually. Remove the marker only after checking the recovered service and signing in.

## Old Tunnel Variables In `.env`

Older awg-forge versions stored first-tunnel fields in `.env`, such as `SERVER_HOST`, `LISTEN_PORT`, `IPV4_SUBNET`, `DNS`, `ALLOWED_IPS`, `PERSISTENT_KEEPALIVE`, `MTU`, and `PROTOCOL_PROFILE`.

Current versions use `.env` only for runtime settings after `state.json` exists. If Doctor warns about legacy tunnel env variables, verify tunnel settings in the Web UI and then remove those old lines from `.env`.

## Security

By default, the Web UI binds to `127.0.0.1`, and access goes through an SSH tunnel:

```bash
ssh -L 51821:127.0.0.1:51821 user@server
```

If you choose `WEBUI_HOST=0.0.0.0` or `::`, the script shows a warning and requires explicit confirmation. Use public binds only behind a firewall, VPN, or reverse proxy.

For a fresh installation, the script also offers an ACME certificate for a public DNS domain or a short-lived public IP certificate. Select either only after TCP/80 is reachable from the Internet. The panel continues on the chosen Web UI port. The installer does not wait for the CA: an IP certificate starts its first issuance attempt after the listeners are ready, while a domain certificate is requested by the first HTTPS request for the configured name. Doctor and `tls status` report pending and retry state; see [TLS configuration](configuration.md#web-ui-tls) for constraints and recovery.

The password is shown at the end and stored in `/opt/awg-forge/.env`, or in `.env` inside `AWG_FORGE_HOME`:

```env
PASSWORD=...
```

## After Install

Open the UI, sign in as `admin` with the installation password, create a client, and choose `Actions` → `Connect`. Use one of the offered import methods. AWG 3.x supports `.conf`, AmneziaWG QR, AmneziaVPN QR, and `vpn://`; use AmneziaVPN 5.0.1.5+ and keep `.conf` as the fallback. Then check IPv4 egress:

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

If you need to remove awg-forge, run uninstall while `data/state.json` still exists. This lets the script remove exact managed tunnel interfaces, firewall rules (current tagged scoped rules and legacy untagged rules), and WARP policy routes when applicable.

Download the current script and preview the changes before uninstalling.

```bash
curl -fsSL https://raw.githubusercontent.com/astronaut808/awg-forge/master/uninstall.sh -o uninstall.sh
chmod +x uninstall.sh
sudo ./uninstall.sh --dry-run --yes
```

To uninstall while keeping local installation files, run:

```bash
sudo ./uninstall.sh
```

By default, the script removes only interfaces and firewall rules that can be matched to tunnels in `data/state.json`. If state is already missing, unknown `awg*` interfaces are preserved to avoid deleting an unrelated AmneziaWG tunnel.

`--yes` confirms the runtime cleanup without prompts and keeps `data/`, `.env`, and `docker-compose.yml`. Add `--purge` only when those local installation files must also be removed.

After reviewing those interfaces, remove them explicitly:

```bash
sudo ./uninstall.sh --remove-orphans
```
