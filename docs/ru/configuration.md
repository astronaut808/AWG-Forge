# Конфигурация

Основной пример находится в [.env.example](../../.env.example).

## Основные переменные

- `WEBUI_HOST`: адрес Web UI. По умолчанию `127.0.0.1`.
- `WEBUI_PORT`: порт Web UI. По умолчанию `51821`.
- `PASSWORD`: пароль Web UI. Обязателен для публичного bind и рекомендуется всегда.
- `SESSION_COOKIE_SECURE`: режим Secure cookie для UI session. Значения: `auto`, `true`, `false`. По умолчанию `auto`.
- `EXTERNAL_INTERFACE`: внешний интерфейс сервера, через который идет egress. Часто это `eth0` или `ens3`. В bridge networking внутри контейнера обычно `eth0`.
- `APPLY_CONFIG`: если `true`, awg-forge применяет runtime-изменения через AmneziaWG tools.
- `PUBLISHED_UDP_PORTS`: опубликованные Docker UDP-порты/диапазоны, например `51820-51840,7443`.
- `AUDIT_LOG_ENABLED`: включает безопасный audit log. По умолчанию `true`.
- `AUDIT_LOG_PATH`: путь к audit log. По умолчанию `/etc/awg-forge/audit.log`.
- `AUDIT_LOG_MAX_SIZE`: размер файла до ротации. По умолчанию `5242880`.
- `AUDIT_LOG_MAX_FILES`: сколько rotated-файлов хранить. По умолчанию `3`.
- `DATABASE_MODE`: optional operational database mode. Значения: `off`, `sqlite`, `postgres`. По умолчанию `off`; `postgres` зарезервирован для будущей поддержки.
- `DATABASE_PATH`: путь к SQLite database. По умолчанию `/etc/awg-forge/awg-forge.db`.
- `DATABASE_RETENTION_DAYS`: default retention window для operational data. По умолчанию `90`.
- `DATABASE_BUSY_TIMEOUT`: SQLite busy timeout. По умолчанию `5s`.
- `DATABASE_QUERY_TIMEOUT`: timeout для database commands/queries. По умолчанию `2s`.
- `DATABASE_MAX_OPEN_CONNS`: лимит database connections. По умолчанию `1`.
- `DATABASE_MAX_IDLE_CONNS`: лимит idle connections. По умолчанию `1`.

## Инициализация первого туннеля

В новых установках `.env` хранит только runtime-настройки запуска, а настройки туннелей живут в `state.json`.

При чистой установке `install.sh` до запуска сервиса выполняет одноразовый контейнер `awg-forge init` и сразу создает `data/state.json` с выбранным первым туннелем. После этого `docker compose up -d` запускает уже готовое состояние, а туннели управляются через Web UI/API и сохраняются в `state.json`.

Установщик сначала спрашивает protocol profile, а уже потом настройки туннеля, чтобы профильные defaults совпадали. Если на вопросе профиля просто нажать Enter, будет выбран AWG 2.0:

| Профиль | Имя туннеля | Порт | Подсеть |
| --- | --- | --- | --- |
| `awg_legacy_1_0` | `awg0` | `51820` | `10.8.0.0/24` |
| `awg_1_5` | `awg15` | `51825` | `10.15.0.0/24` |
| `awg_2_0` | `awg20` | `51830` | `10.20.0.0/24` |

При создании следующих туннелей в Web UI awg-forge предлагает свободные имена, порты и подсети с учётом всех профилей. Backend всё равно отклоняет ручные конфликты.

Если ты обновляешься со старой версии awg-forge и в `.env` остались `SERVER_HOST`, `LISTEN_PORT`, `IPV4_SUBNET`, `DNS`, `ALLOWED_IPS`, `PERSISTENT_KEEPALIVE`, `MTU` или `PROTOCOL_PROFILE`, после появления `state.json` эти значения игнорируются. Проверь настройки туннелей в UI и затем удали старые tunnel-переменные из `.env`, чтобы они не путали.

## SESSION_SECRET

`SESSION_SECRET` можно не указывать. Если он отсутствует, awg-forge создаст и сохранит секрет в `state.json`.

Это нужно для подписи UI session cookie. Пользователю не нужно управлять этим вручную в обычном сценарии.

## SESSION_COOKIE_SECURE

`SESSION_COOKIE_SECURE` управляет флагом `Secure` у session cookie:

- `auto`: по умолчанию. Для `127.0.0.1`, `localhost`, `::1` cookie работает по HTTP без `Secure`; для внешних host cookie ставится с `Secure`.
- `true`: всегда ставить `Secure`. Используй для HTTPS/reverse proxy.
- `false`: не ставить `Secure`. Это позволяет логиниться через `http://domain:port`, но небезопасно для публичного интернета.

Если нужно открыть Web UI по обычному HTTP, лучше делать это только в доверенной сети или за отдельной защитой. Для production безопаснее оставить `WEBUI_HOST=127.0.0.1` и заходить через SSH tunnel, либо использовать HTTPS.

## EXTERNAL_INTERFACE

Чтобы узнать внешний интерфейс на сервере:

```bash
ip route get 1.1.1.1
```

Пример:

```text
1.1.1.1 via 203.0.113.1 dev ens3 src 203.0.113.10
```

В этом случае:

```env
EXTERNAL_INTERFACE=ens3
```

Если интерфейс указан неверно, handshake может быть, но интернет через VPN не заработает.

## Endpoint туннеля

У каждого туннеля есть поле `Server host` в Web UI. Оно задает host, который awg-forge использует в `Endpoint = <host>:<port>` для клиентских `.conf`.

В новых установках это значение попадает в `state.json` при первом `awg-forge init`. Изменение `SERVER_HOST` в `.env` после создания state не переписывает существующие туннели.

Это удобно, когда разные туннели публикуются через разные поддомены, например:

```text
legacy.example.com:44865
awg20.example.com:44867
```

Важно:

- `Server host` не должен содержать схему, путь или порт;
- порт берется из настроек туннеля;
- после изменения host клиентам нужно заново импортировать свежий config через `Config`;
- существующие импортированные клиенты не обновятся сами.

## MTU

`MTU=0` в настройках туннеля означает, что awg-forge не добавляет строку `MTU = ...` в server/client configs.

Если ты явно задаешь MTU на туннеле, то он рендерится одинаково в серверный и клиентский конфиг. awg-forge не подменяет MTU скрытыми решениями.

Практически:

- `Auto` подходит как стартовое значение;
- `1280` часто помогает на проблемных сетях, мобильных сетях, роутерах и сложных маршрутах;
- Web UI предлагает `Auto`, частые presets и `Custom` для явного MTU;
- после изменения MTU клиентам нужно заново импортировать свежий config через `Config`.

## IPv6 и AllowedIPs

Текущая версия awg-forge управляет IPv4 egress. Клиентские конфиги намеренно используют:

```ini
AllowedIPs = 0.0.0.0/0
```

`::/0` не добавляется автоматически, потому что серверная часть пока не создает IPv6 subnet, IPv6-адреса клиентов, IPv6 forwarding и NAT66/ip6tables или nftables rules. Если добавить `::/0` без полноценного IPv6 egress, часть IPv6-трафика клиента может уйти в туннель и получить blackhole.

Если нужна защита от IPv6 leak до появления полноценной IPv6-поддержки, отключи IPv6 на клиенте/роутере или настрой IPv4-only поведение на стороне клиента.

## Egress туннеля и WARP

У каждого туннеля есть режим выхода в интернет:

- `Server WAN`: клиентский трафик выходит через внешний интерфейс сервера из `EXTERNAL_INTERFACE`;
- `Cloudflare WARP`: клиентский трафик выходит через общий outbound-интерфейс `warp0`.

WARP не является protocol profile AmneziaWG. Это режим outbound routing для уже существующих туннелей. Поэтому Legacy / 1.0, AWG 1.5 и AWG 2.0 могут независимо использовать WAN или WARP egress.

Рекомендуемый путь:

1. Выбери `Cloudflare WARP` в поле `Egress` при создании туннеля или открой `Tunnel settings` у существующего туннеля.
2. Переключи `Egress` с `Server WAN` на `Cloudflare WARP`.
3. Нажми `Create tunnel` или `Save`.

Если WARP еще не настроен, awg-forge автоматически зарегистрирует Cloudflare WARP, создаст общий outbound-интерфейс `warp0`, применит runtime routing/NAT и затем переключит туннель на WARP egress.

`Maintenance` -> `WARP` нужен для обслуживания: посмотреть статус, вручную зарегистрировать или перерегистрировать WARP, перезапустить `warp0`, удалить WARP config, либо импортировать config вручную.

Ручной импорт нужен только как fallback, если у тебя уже есть готовый Cloudflare WARP WireGuard/AmneziaWG config из внешнего генератора или WARP client tool. В этом случае открой `Manual WARP config import`, вставь весь config целиком и нажми `Import WARP config`.

Клиентские конфиги не нужно менять, если меняется только egress mode: клиент продолжает подключаться к тому же AmneziaWG endpoint. Меняются только server-side routing/NAT rules.

Doctor проверяет WARP runtime, policy rules и WARP-aware firewall expectations для туннелей, которые используют WARP.

## APPLY_CONFIG

Если `APPLY_CONFIG=true`, mutating operations не только меняют state/config files, но и применяют изменения в runtime.

Если runtime apply падает, awg-forge откатывает state и rendered configs. UI покажет apply error, но не должен оставлять “созданного” клиента или туннель, который не применился.

Для локальной разработки удобно:

```env
APPLY_CONFIG=false
```

## Audit Log

Audit log хранит историю безопасных operational events: login success/fail, create/update/delete clients, create/update/delete/restart tunnels, firewall repair, backup/support/restore verify и update checks.

Он нужен для разбора случаев “вчера работало, потом поменяли настройки, теперь handshake есть, но интернета нет”.

В Web UI вкладка `Maintenance` -> `Logs` автообновляется, пока окно Maintenance открыто, и показывает новые события сверху.

Audit log не должен содержать:

- private keys;
- preshared keys;
- passwords;
- session secrets;
- full client configs;
- import keys или `vpn://`;
- raw protocol parameter values.

Посмотреть последние события:

```bash
docker exec awg-forge awg-forge logs
docker exec awg-forge awg-forge logs --tail 200
docker exec awg-forge awg-forge logs --level error
docker exec awg-forge awg-forge logs --event tunnel.apply.failed
```

## Operational Database

`DATABASE_MODE=off` используется по умолчанию. В этом режиме существующие установки остаются file-based, и база не создается.

`DATABASE_MODE=sqlite` включает локальный SQLite foundation для operational history: audit search, login attempts, health history, TLS events и traffic usage. JSONL остается надежным локальным audit trail. Он не переносит `state.json`, private keys, WARP tokens, raw configs, QR payloads или import links в базу.

Инициализировать или обновить локальную схему:

```bash
docker exec awg-forge awg-forge db migrate
```

Проверить статус базы:

```bash
docker exec awg-forge awg-forge db status
docker exec awg-forge awg-forge doctor
```

Применить retention cleanup:

```bash
docker exec awg-forge awg-forge db retention apply
```

SQLite использует локальный файл внутри `CONFIG_DIR`, WAL mode, включенные foreign keys и права `0600`. Не размещай эту базу на network filesystem.

Когда SQLite включен и миграции применены, audit events пишутся и в существующий JSONL audit log, и в `audit_events`. `Maintenance` -> `Logs` и `awg-forge logs` объединяют события из SQLite и JSONL, а если SQLite недоступен, читают JSONL. Это не дает проблемам SQLite mirror скрыть события, которые попали в `audit.log`.

Когда SQLite включен, миграции применены и `APPLY_CONFIG=true`, awg-forge раз в минуту снимает runtime transfer counters и хранит дневные aggregates трафика по клиентам. Первый sample задает baseline и не считается переданным трафиком. Строки клиентов показывают общий записанный трафик, а `Maintenance` -> `Traffic` показывает aggregate totals за сегодня, 7 дней и 30 дней по всем клиентам и туннелям.

В настройках клиента можно сохранить optional traffic limit в GiB, если включен SQLite. Лимит показывается как `итого / лимит`; пустое значение означает без лимита. Когда записанный трафик достигает или превышает лимит, awg-forge отключает клиента через обычный render/apply path и пишет audit event. Чтобы вернуть доступ, увеличь или очисти лимит и включи клиента вручную.
