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
- on fresh installs, asks for the protocol profile first, then offers automatic free UDP port selection from `30000-49999` or manual input;
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

The first command shows the pre-upgrade state; the last checks it afterwards. Use the installer from the current release so its compatibility checks and migrations match that release. The script pulls the target image, stops the current container, backs up `.env` and `data/`, applies SQLite migrations before the new container starts, then checks that the container is running and verifies `db status`. It also prints Doctor output for review. If SQLite is currently off, it asks whether to enable it; the default is `No`. If SQLite is enabled but its database file is missing, it requires confirmation before creating a new empty database. A failed migration, failed container start, or failed database status check restores the backup and the previous image.

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

For a fresh installation, the script also offers an ACME certificate for a public DNS domain or a short-lived public IP certificate. Select either only after TCP/80 is reachable from the Internet. The panel continues on the chosen Web UI port. The installer does not wait for the CA: an IP certificate starts its first issuance attempt after the listeners are ready, while a domain certificate is requested by the first HTTPS request for the configured name. Doctor and `tls status` report pending and retry state; see [TLS configuration](configuration.md#web-ui-tls) for constraints and recovery.

The password is shown at the end and stored in `/opt/awg-forge/.env`, or in `.env` inside `AWG_FORGE_HOME`:

```env
PASSWORD=...
```

## After Install

Open the UI, create a client, open `Config`, and use one of the offered import methods. AWG 3.x supports `.conf`, AmneziaWG QR, AmneziaVPN QR, and `vpn://`; use AmneziaVPN 5.0.1.5+ and keep `.conf` as the fallback. Then check IPv4 egress:

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

Always run the current `uninstall.sh` from `master` before removal. It contains cleanup logic for current and legacy installations.

```bash
curl -fsSL https://raw.githubusercontent.com/astronaut808/awg-forge/master/uninstall.sh | sudo bash
```

Remove the container, runtime interfaces, firewall rules, and local install files:

```bash
curl -fsSL https://raw.githubusercontent.com/astronaut808/awg-forge/master/uninstall.sh | sudo bash -s -- --purge
```

Preview all actions without stopping the container or modifying the host:

```bash
curl -fsSL https://raw.githubusercontent.com/astronaut808/awg-forge/master/uninstall.sh | sudo bash -s -- --dry-run --yes
```

By default, the script removes only interfaces and firewall rules that can be matched to tunnels in `data/state.json`. If state is already missing, unknown `awg*` interfaces are preserved to avoid deleting an unrelated AmneziaWG tunnel.

`--yes` confirms the runtime cleanup without prompts and keeps `data/`, `.env`, and `docker-compose.yml`. Add `--purge` only when those local installation files must also be removed.

After reviewing those interfaces, remove them explicitly:

```bash
curl -fsSL https://raw.githubusercontent.com/astronaut808/awg-forge/master/uninstall.sh | sudo bash -s -- --remove-orphans
```

## Explicit installer connection to a controller

Ordinary installation remains standalone. In Maintenance → Controller, use recent MFA to prepare the exact control endpoint, download and retain a verified encrypted backup, and enable it. An external endpoint needs its separate consent checkbox. Then choose Add node, fresh or existing installation, and the node name. Compare the displayed code and name with the node terminal before approving; Connected appears only after authenticated presence from that enrollment. Closing the flow, logout, account change, rejection or expiry clears the invitation and stops polling.

This development build has no published compatible installer artifact. The UI therefore copies **public arguments only**, and reports the missing release. It does not offer a release URL or fall back to `latest`. For local testing, use a downloaded/reviewed script with its verified SHA-256, and an explicitly built local image ID with matching compiled version and commit. The skeleton below contains only public placeholders; replace each from trusted artifact metadata and the UI:

```bash
sudo bash ./install.sh join --workdir /opt/awg-node --mode fresh \
  --image sha256:IMAGE_ID_64_HEX --script-sha256 SCRIPT_SHA256_64_HEX \
  --artifact-version local-SOURCE_HASH_12_HEX --artifact-commit COMMIT_40_HEX \
  --controller-url 'https://controller.example:9443' \
  --ca-pin 'sha256:CA_SPKI_PIN_64_HEX' --invitation-id INVITATION_UUID --name 'node'
```

The downloaded script and local image must support `installer-onboarding-v1`; metadata mismatch, an older image or a missing local image fails before stopping a service. No image is pulled implicitly. A published immutable GHCR digest is accepted only with matching compiled metadata. Obtain the invitation secret separately from the authenticated UI and paste it into the hidden terminal prompt. Never put it in the command, exported variables, a heredoc or shell substitution. A noninteractive caller may explicitly supply a private pipe/FD as stdin with `--secret-fd 0`; stdin is never used implicitly, including with `curl | bash`.

Fresh join requires a nonexistent direct-child workdir. It generates protected local login credentials in `.env`, binds the break-glass Web UI to loopback, uses DB-off, and creates no tunnel or WARP/ACME setup. Access through an SSH tunnel; inspect the local protected `.env` for the password without copying it into logs.

For an existing root-run Compose service, replace `--mode fresh` with `--mode existing --maintenance`, and optionally select `--container NAME`. The service must carry the exact Compose working-directory/service labels and use the exact verified image. Update an older installation separately first. The installer stops and restarts only that container ID, reuses its mounts and environment, and retains Compose, networks, logging, UI/TLS/session policy, history and local tunnel configuration. Unsupported container users, explicit user namespaces, SELinux process/mount labels or multiline environment values fail before stop. Installer join/rebind requires Docker without SELinux labels and rejects an SELinux-enabled daemon before creating fresh files; deployments using private `:Z` volumes require a deployment-specific offline enrollment/recovery workflow. Do not disable SELinux or relabel existing data to bypass this check. This action interrupts VPN service; an initially stopped service stays stopped unless `--start-service` is explicit.

A managed node requires the separate `rebind` action with `--maintenance`, a fresh invitation, and both exact local `--confirm-node-id` and `--confirm-controller-id`. It calls offline recovery directly, never detach-first. Revoke copied old credentials on the former controller separately.

A failed/interrupted fresh join retains its protected workdir for offline inspection: failure may follow an identity commit. Do not rerun a consumed invitation or delete a pending journal blindly. Before commit, an existing service resumes its previous identity when helper termination is proven. After commit, startup/connectivity failure remains pending; retain the new identity and diagnose/retry service start. Only the controller's authenticated presence confirms connectivity. Controller outage after setup leaves local forwarding independent.
