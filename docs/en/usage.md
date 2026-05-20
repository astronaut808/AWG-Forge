# Web UI and CLI

## Web UI

Main workflow:

1. Open the UI through an SSH tunnel or a protected admin endpoint.
2. Log in.
3. Select profile tab `1.0`, `1.5`, or `2.0`.
4. Create a tunnel if needed.
5. Create a client inside the selected tunnel.
6. After successful creation, the `.conf` file downloads automatically.
7. Import the `.conf` into a compatible AmneziaVPN client.

## UI Actions

- `Create tunnel`: create a new tunnel inside the selected profile.
- `Create client`: create a client inside a specific tunnel.
- `Config`: download an existing client's `.conf`.
- `Settings`: tunnel settings.
- `Protocol`: protocol params and regenerate.
- `Health`: handshake and runtime traffic counters for clients.
- `Doctor`: system and runtime diagnostics.
- `Updates`: check whether bundled AmneziaWG upstream refs are behind.
- `Restart`: restart a tunnel.
- `Delete`: delete a tunnel or client.

## Stale Configs

Changing tunnel settings or protocol params can make old client configs stale.

After such changes, download fresh `.conf` files for affected clients.

## CLI In Docker

```bash
docker exec awg-forge awg-forge doctor
docker exec awg-forge awg-forge updates
docker exec awg-forge awg-forge client add phone
docker exec awg-forge awg-forge client add laptop awg15
docker exec awg-forge awg-forge client config <client-id>
docker exec awg-forge awg-forge client disable <client-id>
docker exec awg-forge awg-forge client enable <client-id>
docker exec awg-forge awg-forge client remove <client-id>
docker exec awg-forge awg-forge tunnel create awg_1_5 awg15 51825 10.15.0.0/24
docker exec awg-forge awg-forge tunnel restart
```

## Local CLI

```bash
awg-forge init
awg-forge serve
awg-forge render
awg-forge doctor
awg-forge updates
```

## Client Config Import

The supported path is `.conf` file import.

QR import is not shown in the UI and is not supported as a product path.
