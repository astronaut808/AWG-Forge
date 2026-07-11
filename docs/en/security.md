# Security

## Web UI Bind

By default the Web UI listens on:

```env
WEBUI_HOST=127.0.0.1
WEBUI_PORT=51821
```

Production recommendation: keep the UI on loopback and access it through an SSH tunnel:

```bash
ssh -L 51821:127.0.0.1:51821 user@server
```

If the UI is published publicly, a password is required.

## TLS and Reverse Proxies

Built-in Web UI TLS supports `off`, `reverse-proxy`, and `manual`; see [configuration](configuration.md#web-ui-tls). `manual` mode requires TLS 1.3 and rejects invalid certificates, mismatched keys, expired certificates, key symlinks, private keys without mode `0600`, and key directories without mode `0700`.

Reverse-proxy mode requires a Web UI password, `WEBUI_TRUST_PROXY_HEADERS=true`, and direct proxy addresses in `WEBUI_TRUSTED_PROXY_CIDRS`. Untrusted forwarded headers are ignored. Keep TLS termination and the Web UI bind separate: the proxy can expose HTTPS on any port while awg-forge remains on loopback.

## Sessions

UI sessions expire after 30 minutes.

`SESSION_SECRET` can be omitted. If absent, awg-forge creates and stores it in `state.json`.

By default `SESSION_COOKIE_SECURE=auto`: non-`Secure` cookies are allowed only for loopback HTTP (`127.0.0.1`, `localhost`, `::1`), while external hosts use `Secure`. For plain HTTP on an external host, explicitly set `SESSION_COOKIE_SECURE=false`; doctor will warn about this. Use that mode only on a trusted network or behind separate protection.

## Origin / Referer Checks

State-changing requests validate Origin/Referer.

POST without Origin/Referer is allowed only for loopback Host (`127.0.0.1`, `localhost`, `::1`). This preserves localhost/SSH tunnel workflows without allowing the same behavior for public Hosts.

Opaque origins such as `null` and browser-extension origins are rejected for mutating requests.

## Secrets

Do not log:

- private keys;
- preshared keys;
- passwords;
- session secrets;
- backup passwords;
- full client configs;
- import keys, `vpn://` links, QR payloads, or packed AmneziaVPN QR strings.

## File Permissions

Config directories and generated config files should have restrictive permissions.

Doctor checks config directory permissions and warns about problems.

## Runtime Apply Rollback

If a mutating operation changes state/configs but runtime apply fails, awg-forge rolls back state and rendered configs.

This prevents the UI from showing a created client or modified tunnel when runtime state was not successfully applied.
