# Contributing

Thanks for considering a contribution to awg-forge.

awg-forge manages VPN configuration and secret material, so reliability and conservative changes matter more than feature volume.

## Development

Run the full local check before opening a pull request:

```bash
make ci
```

Run the Web UI locally without applying runtime tunnel changes:

```bash
CONFIG_DIR=/private/tmp/awg-forge-dev \
WEBUI_HOST=127.0.0.1 \
WEBUI_PORT=51821 \
PASSWORD=test \
APPLY_CONFIG=false \
SERVER_HOST=127.0.0.1 \
go run ./cmd/awg-forge serve
```

Then open:

```text
http://127.0.0.1:51821
```

## Pull Requests

- Keep changes focused.
- Add or update tests for behavior changes.
- Do not commit generated runtime data, real configs, backups, `.env`, or support bundles.
- Do not log or expose private keys, preshared keys, session secrets, backup passwords, or full client configs.
- Prefer existing project patterns over adding dependencies.
- For protocol changes, cite upstream AmneziaWG behavior or generated config examples.
- For UI changes, keep all existing API payloads and idempotency behavior intact.

## Security

Do not report vulnerabilities in public issues. See [SECURITY.md](SECURITY.md).

## Release Checks

Before a release:

```bash
make ci
docker build --platform linux/amd64 -t awg-forge:local .
```

If runtime behavior changed, verify `doctor`, client creation, config download, tunnel restart, backup/restore, and support bundle redaction.
