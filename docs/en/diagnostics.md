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
- config directory permissions;
- UDP listen ports;
- rendered server configs;
- runtime tunnel links;
- runtime `awg show` listen ports;
- NAT/FORWARD firewall rules;
- runtime peers;
- stale client configs;
- handshakes and transfer counters.

## Support Bundle

Support bundles are meant for sharing diagnostics without private keys or full configs.

In the UI, click `Support` to download a `.zip`.

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

## Encrypted Backup / Restore

Backups are different from support bundles: they contain secret material, including `state.json`, private keys, preshared keys, and rendered `.conf` files.

Backups are always encrypted with a dedicated password:

```bash
docker exec -e BACKUP_PASSWORD='long-random-backup-password' awg-forge awg-forge backup /tmp/awg-forge.afbackup
docker cp awg-forge:/tmp/awg-forge.afbackup .
```

Restore requires the same password:

```bash
docker cp awg-forge.afbackup awg-forge:/tmp/awg-forge.afbackup
docker exec -e BACKUP_PASSWORD='long-random-backup-password' awg-forge awg-forge restore /tmp/awg-forge.afbackup
```

Before replacing the current config directory, restore keeps an encrypted pre-restore backup in `backups/` inside the restored config directory.

Restore checks:

- password and ciphertext integrity;
- `metadata.json`;
- schema version;
- file checksums;
- valid `state.json`;
- server config rendering.

Restore does not apply runtime automatically. After restore, restart the container or explicitly restart tunnels.

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

In the UI, use `Doctor` -> `Repair firewall`. When `APPLY_CONFIG=false`, the button is visually unavailable and explains why; when `APPLY_CONFIG=true`, the action requires confirmation.

## Health In UI

The tunnel `Health` action samples runtime counters and shows client status.

Possible statuses:

- `traffic flowing`: handshake exists and rx/tx counters are moving;
- `idle, handshake ok`: handshake exists, but traffic did not move during the short sample window;
- `client sends traffic, server sends 0 bytes back`: possible NAT, forwarding, route, DNS, or upstream firewall issue.

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
