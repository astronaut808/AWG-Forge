# Frontend product plan

awg-forge UI — статическое HTML/CSS/JavaScript admin-приложение поверх Go JSON API. Здесь нет React/Vue/npm build pipeline.

## Принципы

- Первый экран — operational dashboard, не landing page.
- Tunnels — first-class objects.
- Clients всегда принадлежат одному tunnel.
- Основная навигация — tabs профилей: `1.0`, `1.5`, `2.0`.
- Частые действия должны быть в один-два клика: create tunnel, create client, download config, disable, delete.
- Protocol internals — advanced controls.
- Опасные действия требуют подтверждения.
- UI не должен показывать private keys, preshared keys, session secrets или full configs, кроме явного download.

## Архитектура

- `internal/server/static/index.html`: app shell;
- `internal/server/static/app.css`: styling;
- `internal/server/static/html.js`: безопасный рендер динамических HTML-фрагментов;
- `internal/server/static/app.js`: dashboard state, profile rendering и API glue;
- `internal/server/static/forms.js`: формы туннелей и клиентов;
- `internal/server/static/maintenance.js`: views Maintenance Center;
- `internal/server/static/maintenance-actions.js`: операции Maintenance Center;
- `internal/server/static/ui.js`: общие dialogs и UI helpers;
- `internal/server/static/boot.js`: финальная инициализация в браузере;
- `internal/server/server.go`: static serving and JSON API.

Динамические HTML-фрагменты проходят через небольшой sanitizer в `html.js` перед добавлением в DOM. Явное экранирование значений при сборке HTML остается обязательным.

Client config download идет через protected response:

```text
/clients/config/<id>
```

## Dashboard

Каждая вкладка показывает туннели только своего protocol profile.

Tunnel card показывает:

- name/interface;
- protocol profile;
- endpoint host:port;
- IPv4 subnet;
- DNS;
- MTU;
- interface state;
- compact runtime summary по runtime, firewall rules и stale client configs;
- enabled/total clients;
- last apply error, если есть.

Tunnel actions:

- Create client;
- Settings;
- Protocol;
- Health;
- Restart;
- Delete.

Maintenance-действия сгруппированы в Maintenance Center:

- Overview;
- Doctor;
- Firewall;
- Encrypted backup;
- Restore verify dry-run;
- Updates;
- Support bundle;
- System info.

## Settings

Editable fields:

- name/interface;
- endpoint server host override;
- listen port;
- IPv4 subnet;
- DNS;
- allowed IPs;
- persistent keepalive;
- MTU;
- enabled flag.

MTU choices:

- `Auto`;
- `1280`;
- `1380`;
- `1400`;
- `1420`;
- custom value.

Изменение server host, port, MTU, DNS, allowed IPs, keepalive или protocol params требует fresh client configs. UI показывает stale badge у клиентов, чей скачанный config revision отстал от tunnel revision.

## Protocol modal

Legacy / 1.0:

- `Jc`;
- `Jmin`;
- `Jmax`;
- `S1`;
- `S2`;
- `H1-H4`.

AWG 1.5:

- Legacy fields;
- `I1-I5`.

AWG 2.0:

- Legacy fields;
- `S3/S4`;
- `H1-H4` ranges;
- `I1-I5`.

Legacy modal не должен показывать `I1-I5`.

## Clients

Client actions:

- Download config;
- Edit name и admin notes;
- Disable/Enable;
- Delete.

Client rename и notes — metadata-only изменения. Они не должны регенерировать keys, менять peer settings, рестартить tunnels или помечать configs stale.

Create client:

- запускается из tunnel card;
- требует имя клиента;
- создает клиента только в этом tunnel;
- после успешного создания запускает `.conf` download.

## API

Frontend использует:

- `POST /api/login`;
- `POST /api/logout`;
- `GET /api/state` с `apply_enabled` для отображения dry-run режима maintenance-действий;
- `GET /api/doctor`;
- `POST /api/backup`;
- `POST /api/firewall/repair`;
- `GET /api/support-bundle`;
- `GET /api/updates`;
- `POST /api/tunnels`;
- `PATCH /api/tunnels/<id>/settings`;
- `PATCH /api/tunnels/<id>/protocol`;
- `POST /api/tunnels/<id>/regenerate`;
- `POST /api/tunnels/<id>/restart`;
- `GET /api/tunnels/<id>/health`;
- `DELETE /api/tunnels/<id>/delete`;
- `POST /api/clients`;
- `PATCH /api/clients/<id>/settings`;
- `POST /api/clients/<id>/enable`;
- `POST /api/clients/<id>/disable`;
- `DELETE /api/clients/<id>/delete`;
- `GET /clients/config/<id>`.

State-changing requests должны сохранять Origin/Referer validation и не логировать secrets.

## Acceptance criteria

- UI работает как static HTML/CSS/JS без npm build.
- Можно создавать отдельные Legacy, 1.5 и 2.0 tunnels.
- Clients создаются внутри выбранного tunnel.
- Protocol changes затрагивают только выбранный tunnel.
- Legacy settings не показывают `I1-I5`.
- MTU может быть `Auto` или explicit per tunnel.
- `.conf` download доступен из понятного flow.
