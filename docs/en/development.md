# Development

Use the Go version in `go.mod` and the Node.js and npm versions pinned in
`package.json`. CI tool versions are defined in the workflows and the Makefile.

## Build and test

```bash
npm ci
npx --no-install playwright install chromium
npm run ui:build
make ci
git diff --check
```

On Linux, add `--with-deps` when installing Chromium if its system libraries are
missing. `make ci` includes Go, shell, and browser tests, linting, and the OpenAPI
contract check. Run `make test-shell` to check the installer, upgrade, and
uninstall scripts separately.

Edit the frontend in `web/` and regenerate the assets embedded in the Go binary
with `npm run ui:build`.

## Run the UI locally

```bash
config_dir="$(mktemp -d "${TMPDIR:-/tmp}/awg-forge-dev.XXXXXX")" &&
CONFIG_DIR="$config_dir" \
WEBUI_HOST=127.0.0.1 \
WEBUI_PORT=51821 \
WEBUI_USERNAME=admin \
PASSWORD=test \
APPLY_CONFIG=false \
go run ./cmd/awg-forge serve
```

Open <http://127.0.0.1:51821> and sign in with `admin` / `test`. The temporary
directory isolates local test data and `APPLY_CONFIG=false` prevents changes
to the host's tunnels.

For live frontend changes, run `npm run ui:dev` and open
<http://127.0.0.1:51923/static/>. The dev server uses the local backend at
`127.0.0.1:51821` by default. Set `AWG_UI_BACKEND` if the backend uses another
address. If browser test ports are occupied, set `UI_TEST_PORT`.

## Release checks

```bash
make security
make docker-build
make docker-smoke IMAGE=awg-forge:local
make docker-runtime-smoke IMAGE=awg-forge:local
```

`make security-fast` runs a smaller set of scans for local iteration. The runtime
smoke test needs a Linux Docker engine with `/dev/net/tun`. It checks encrypted
IPv4 traffic for all supported profiles in an isolated Docker network. Verify
upgrades, rollbacks, and native VPN clients separately on a test deployment.

Prepare a reviewed candidate on a release branch and submit a PR to `master`.
Finalize the dated changelog section and OpenAPI version after deployment
validation. Create the signed version tag after the release commit is on `master`.

Tag CI checks ancestry and release notes, builds the supported `linux/amd64`
image, runs both Docker smoke tests and Trivy, then transfers and publishes
that verified image. GitHub Release publication depends on the image job.
