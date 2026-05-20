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
