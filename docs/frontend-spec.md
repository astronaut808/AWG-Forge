# Frontend Product Plan

awg-forge UI is a static HTML/CSS/JavaScript admin app backed by the Go JSON API. There is no React/Vue/npm build pipeline; Go owns backend, auth, state, rendering configs, and serving static assets.

## Product Principles

- The first screen is the operational dashboard, not a landing page.
- Tunnels are first-class objects; clients always belong to exactly one tunnel.
- The primary navigation is profile tabs: `1.0`, `1.5`, and `2.0`.
- Common actions should be one or two clicks: create tunnel, create client, download config, show QR, disable, delete.
- Protocol internals are advanced controls. Strong defaults are generated automatically.
- Dangerous or compatibility-breaking actions need confirmation.
- The UI must never show private keys, preshared keys, session secrets, or full configs except on explicit QR/download actions.

## Current Frontend Architecture

- `internal/server/static/index.html`: app shell.
- `internal/server/static/app.css`: product styling.
- `internal/server/static/app.js`: UI state, modals, API calls.
- `internal/server/server.go`: static file serving plus JSON API.

The static UI talks to endpoints under `/api/*`. Client config download remains a protected file response at `/clients/config/<id>` and is the recommended import path. AmneziaVPN QR import uses `/api/clients/<id>/qr`, which returns one or more QR PNG payloads for sequential scanning; `/api/clients/<id>/qr.png` is kept as a first-image compatibility endpoint. QR import is experimental on iOS until the Amnezia native QR schema is verified against the real app.

## Information Architecture

Primary areas:

- Profile tabs: `1.0`, `1.5`, `2.0`
- Tunnel cards inside each profile
- Tunnel settings modal
- Protocol parameters modal
- Client creation and QR modal

Protocol settings are scoped to a tunnel. The UI must not offer “replace this Legacy tunnel with 1.5” as the normal flow; users should create a parallel tunnel for a different profile.

## Dashboard And Tabs

Each tab shows only tunnels for that protocol profile.

Tab behavior:

- `1.0`: Legacy / 1.0 tunnels.
- `1.5`: AWG 1.5 tunnels.
- `2.0`: visible but disabled/planned until backend profile syntax is verified.

Tunnel card content:

- name/interface
- protocol profile
- endpoint host:port
- IPv4 subnet
- DNS
- MTU, with `Auto` for omitted MTU
- interface state
- enabled/total clients
- last apply error, if any

Tunnel actions:

- Create client
- Settings
- Protocol
- Restart
- Delete

## Tunnel Settings

Settings live on the tunnel card.

Editable fields:

- name/interface
- listen port
- IPv4 subnet
- DNS
- Allowed IPs
- Persistent keepalive
- MTU
- enabled flag

MTU choices:

- `Auto`: omit `MTU = ...` from generated configs.
- `1280`
- `1380`
- `1400`
- `1420`
- custom value

Changing port, MTU, DNS, Allowed IPs, or protocol params means clients should download fresh configs. The UI should add stale-config indicators in the next phase.

## Protocol Settings

Legacy / 1.0 fields:

- `Jc`
- `Jmin`
- `Jmax`
- `S1`
- `S2`
- `H1-H4`

AWG 1.5 fields:

- Legacy fields
- `I1-I5`

AWG 2.0 fields are not enabled until the backend profile is implemented and tested.

The protocol modal must not show `I1-I5` for Legacy / 1.0.

## Clients

Clients are shown inside the tunnel they belong to.

Client actions:

- Download config
- Show QR
- Disable/Enable
- Delete

Create client:

- Starts from a tunnel card.
- Requires client name.
- Creates the client in that tunnel only.
- Redirects to QR/download flow.
- Shows `.conf` download as the recommended path.
- Labels QR as experimental on iOS and tells users to use `.conf` if the iOS system VPN tunnel does not start.

## API Expectations

The frontend uses JSON APIs:

- `POST /api/login`
- `POST /api/logout`
- `GET /api/state`
- `POST /api/tunnels`
- `PATCH /api/tunnels/<id>/settings`
- `PATCH /api/tunnels/<id>/protocol`
- `POST /api/tunnels/<id>/regenerate`
- `POST /api/tunnels/<id>/restart`
- `DELETE /api/tunnels/<id>/delete`
- `POST /api/clients`
- `POST /api/clients/<id>/enable`
- `POST /api/clients/<id>/disable`
- `DELETE /api/clients/<id>/delete`
- `GET /api/clients/<id>/qr`
- `GET /api/clients/<id>/qr.png`
- `GET /clients/config/<id>`

All state-changing requests must keep Origin/Referer validation and must never log secrets.

## Docker UX

Host networking is preferred because tunnels can be created in the UI with any free UDP port.

Bridge networking is supported only when a fixed UDP range is published ahead of time. The UI should eventually warn if a new tunnel port is outside the documented published range.

## Next UX Hardening

- Add idempotency tokens for create/delete/restart/regenerate.
- Add stale-config indicators after tunnel settings or protocol changes.
- Add doctor UI.
- Add port-range warnings for bridge Docker mode.
- Add client rename.

## Acceptance Criteria

- The UI renders as static HTML/CSS/JS with no npm build.
- Users can create separate Legacy and 1.5 tunnels with unique ports/subnets.
- Clients are always created inside a selected tunnel.
- Protocol changes affect only the selected tunnel.
- Legacy protocol settings never show `I1-I5`.
- MTU can be `Auto` or explicit per tunnel.
- Users can download/QR a client config from a clear flow.
