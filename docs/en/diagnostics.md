# Diagnostics and Troubleshooting

## Doctor

Run:

```bash
docker exec awg-forge awg-forge doctor
```

Doctor checks:

- root/capabilities;
- `/dev/net/tun`;
- `awg`, `awg-quick`, `amneziawg-go`;
- `iptables`, `ip`, `nf_tables`;
- IPv4 forwarding;
- external interface;
- IPv4 egress route and `EXTERNAL_INTERFACE` match;
- `rp_filter` for host/default/external/tunnel interfaces;
- config directory permissions;
- UDP listen ports;
- UDP listener inspection through `ss`;
- rendered server configs;
- runtime config `/etc/amnezia/amneziawg/<interface>.conf`;
- `awg-quick strip` for runtime config validation;
- runtime tunnel links;
- runtime `awg show` listen ports;
- NAT/FORWARD firewall rules;
- runtime peers;
- stale client configs;
- handshakes and transfer counters.

## Support Bundle

Support bundles are meant for sharing diagnostics without private keys or full configs.

In the UI, open `Maintenance` -> `Support` to download a `.zip`.

In Docker:

```bash
docker exec awg-forge awg-forge support-bundle
```

With an explicit file name:

```bash
docker exec awg-forge awg-forge support-bundle /tmp/awg-forge-support.zip
docker cp awg-forge:/tmp/awg-forge-support.zip .
```

The bundle includes:

- redacted config/state summary;
- Doctor results;
- runtime `ip`, `iptables`, and `awg show` output;
- config directory inventory without `.conf` contents.

The bundle should not include:

- private keys;
- preshared keys;
- password;
- session secret;
- rendered server/client configs;
- raw protocol parameter values.

The bundle also includes `audit-log.redacted.jsonl`: recent audit events with secret-looking fields already redacted.

## Audit Log

The audit log helps reconstruct the event timeline: a client was created, tunnel settings changed, a fresh config was downloaded, firewall repair ran, backup was created, or an apply error happened.

Commands:

```bash
docker exec awg-forge awg-forge logs
docker exec awg-forge awg-forge logs --tail 200
docker exec awg-forge awg-forge logs --level warn
docker exec awg-forge awg-forge logs --event tunnel.settings.updated
docker exec awg-forge awg-forge logs --json
```

The audit log lives at `CONFIG_DIR/audit.log`, defaults to `/etc/awg-forge/audit.log`, uses `0600`, and rotates locally.

When troubleshooting “connected but no internet”, useful events include:

- `tunnel.settings.updated`;
- `tunnel.protocol.updated`;
- `client.config.downloaded`;
- `tunnel.apply.failed`;
- `firewall.repaired`;
- `doctor.completed`.

## Encrypted Backup / Restore

Backups are different from support bundles: they contain secret material, including `state.json`, private keys, preshared keys, and rendered `.conf` files.

Backups are always encrypted with a dedicated password:

```bash
docker exec -e BACKUP_PASSWORD='long-random-backup-password' awg-forge awg-forge backup /tmp/awg-forge.afbackup
docker cp awg-forge:/tmp/awg-forge.afbackup ./awg-forge-backup-YYYYMMDD-HHMMSS.afbackup
```

Restore requires the same password:

```bash
docker cp ./<backup-file>.afbackup awg-forge:/tmp/backup.afbackup
docker exec -e BACKUP_PASSWORD='long-random-backup-password' awg-forge awg-forge restore verify /tmp/backup.afbackup
docker exec -e BACKUP_PASSWORD='long-random-backup-password' awg-forge awg-forge restore /tmp/backup.afbackup
```

`docker exec` can only see files inside the container filesystem. If the backup is on the host, copy it into the container with `docker cp` first, as shown above. Alternatively, place it in the mounted volume:

```bash
cp ./<backup-file>.afbackup ./data/backup.afbackup
docker exec -e BACKUP_PASSWORD='long-random-backup-password' awg-forge awg-forge restore verify /etc/awg-forge/backup.afbackup
docker exec -e BACKUP_PASSWORD='long-random-backup-password' awg-forge awg-forge restore /etc/awg-forge/backup.afbackup
```

`restore verify` decrypts and validates the backup, renders server and client configs in memory, and prints a redacted summary. It does not write to the config directory, create a pre-restore backup, restart tunnels, or change runtime state.

In the UI, open `Maintenance` -> `Restore` to upload an `.afbackup` file and run the same verification as a dry-run. Actual restore remains CLI-only.

Before replacing the current config directory, restore keeps an encrypted pre-restore backup in `backups/` inside the restored config directory.

Restore checks:

- password and ciphertext integrity;
- `metadata.json`;
- schema version;
- file checksums;
- valid `state.json`;
- server config rendering.

Restore does not apply runtime automatically. After restore, explicitly restart tunnels, repair managed firewall rules, and check the system:

```bash
docker exec awg-forge awg-forge tunnel restart
docker exec awg-forge awg-forge firewall repair
docker exec awg-forge awg-forge doctor
```

## Firewall Check / Repair

`doctor` reports missing or duplicate managed firewall rules. To check manually:

```bash
docker exec awg-forge awg-forge firewall check
```

To restore managed rules:

```bash
docker exec awg-forge awg-forge firewall repair
```

Repair only reconciles expected awg-forge rules for enabled tunnels:

- `nat POSTROUTING MASQUERADE` for the tunnel subnet;
- `INPUT udp --dport <port> ACCEPT`;
- `FORWARD -i <interface> ACCEPT`;
- `FORWARD -o <interface> ACCEPT`.

Repair removes duplicates only for these managed rules and adds missing rules. It does not touch unrelated firewall rules. Disabled tunnels do not receive new rules.

When `APPLY_CONFIG=false`, `firewall check/repair` does not change anything and reports a warning.

In the UI, use `Maintenance` -> `Firewall` -> `Repair firewall`. When `APPLY_CONFIG=false`, the button is visually unavailable and explains why; when `APPLY_CONFIG=true`, the action requires confirmation.

## Client Status In UI

The client list shows basic runtime status without a separate diagnostics dialog:

- `active now`: the client had a recent handshake;
- `seen recently`: the client connected before, but may not be active now;
- `never seen`: no handshake has been observed yet;
- `last seen`, `received`, and `sent`: latest handshake time and runtime counters from the server side.

For deeper diagnostics, use `Maintenance` -> `Doctor`, `Support bundle`, and the CLI commands below.

## Check IPv4 Egress

After importing a client config:

```bash
curl -4 https://ifconfig.co
```

The response should show the server egress IP.

## No Internet Through VPN

Check the egress interface:

```bash
ip route get 1.1.1.1
```

If the output includes `dev ens3`, use:

```env
EXTERNAL_INTERFACE=ens3
```

Then:

- run `docker exec awg-forge awg-forge doctor`;
- check IPv4 forwarding;
- check host firewall/UFW;
- in bridge mode, check that the tunnel UDP port is published;
- download a fresh `.conf` if tunnel settings or protocol params changed.

If `doctor` reports:

```text
runtime <tunnel>/awg: <interface> link exists, but awg cannot access it: Protocol not supported
```

the Linux interface exists, but the AmneziaWG runtime cannot read it as an AWG interface. This usually means a stale or broken runtime link after a failed apply, tool version change, or manual runtime experiment. Restart the tunnel from the UI or CLI:

```bash
docker exec awg-forge awg-forge tunnel restart
docker exec awg-forge awg-forge doctor
```

If restart does not help, remove the stale link in the host/container network namespace and apply the tunnel again. With host networking this is usually:

```bash
docker exec awg-forge ip link delete <interface>
docker exec awg-forge awg-forge tunnel restart
```

If `doctor` reports an `external route` mismatch, NAT may be configured for the wrong interface. Check `ip route get 1.1.1.1` and update `EXTERNAL_INTERFACE`.

If `rp_filter` is in strict mode (`1`), reverse path filtering may drop VPN traffic on hosts with non-standard routing or additional firewall/router rules. In a simple host-networking setup it is rarely the first cause, but the WARN is useful on more complex networks.

If the client row shows `received` increasing while `sent` stays at `0 B`, and counters in:

```bash
docker exec awg-forge iptables -L FORWARD -v -n
docker exec awg-forge iptables -t nat -L POSTROUTING -v -n
```

do not increase for the tunnel subnet/interface, traffic did not reach the forwarding/NAT layer. Check `awg show <interface>`, stale links, fresh client config, and the correct protocol profile.

## UI Unavailable

Check:

- SSH tunnel;
- `WEBUI_HOST=127.0.0.1`;
- `WEBUI_PORT=51821`;
- `docker compose logs -f`.

## TUN Unavailable

Check that the host has:

```bash
ls -l /dev/net/tun
```

Compose should include:

```yaml
devices:
  - /dev/net/tun:/dev/net/tun
```

## iptables backend

Doctor expects the `nf_tables` backend:

```bash
iptables -V
```

The output should include `nf_tables`.

## Port Already In Use

If a UDP port is already in use:

- choose another tunnel port;
- or stop the process/interface currently listening on that port.
