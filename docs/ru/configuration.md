# Конфигурация

Основной пример находится в [.env.example](../../.env.example).

## Основные переменные

- `WEBUI_HOST`: адрес Web UI. По умолчанию `127.0.0.1`.
- `WEBUI_PORT`: порт Web UI. По умолчанию `51821`.
- `PASSWORD`: пароль Web UI. Обязателен для публичного bind и рекомендуется всегда.
- `SESSION_COOKIE_SECURE`: режим Secure cookie для UI session. Значения: `auto`, `true`, `false`. По умолчанию `auto`.
- `WEBUI_TRUST_PROXY_HEADERS`: разрешает обработку trusted `X-Forwarded-Proto` и `X-Forwarded-For`. По умолчанию `false`.
- `WEBUI_TRUSTED_PROXY_CIDRS`: CIDR через запятую, которым разрешено передавать forwarded headers. Обязателен при `WEBUI_TRUST_PROXY_HEADERS=true`.
- `EXTERNAL_INTERFACE`: внешний интерфейс сервера, через который идет egress. Часто это `eth0` или `ens3`. В bridge networking внутри контейнера обычно `eth0`.
- `APPLY_CONFIG`: если `true`, awg-forge применяет runtime-изменения через AmneziaWG tools.
- `PUBLISHED_UDP_PORTS`: опубликованные Docker UDP-порты/диапазоны, например `51820-51840,7443`.
- `TUNNEL_UDP_PORT_RANGE`: диапазон для автоматического выбора порта туннеля. По умолчанию `30000-49999`. В bridge mode выбор ограничен пересечением с `PUBLISHED_UDP_PORTS`.
- `AUDIT_LOG_ENABLED`: включает безопасный audit log. По умолчанию `true`.
- `AUDIT_LOG_PATH`: путь к audit log. По умолчанию `/etc/awg-forge/audit.log`.
- `AUDIT_LOG_MAX_SIZE`: размер файла до ротации. По умолчанию `5242880`.
- `AUDIT_LOG_MAX_FILES`: сколько rotated-файлов хранить. По умолчанию `3`.
- `LOG_LEVEL`: уровень runtime-логов: `debug`, `info`, `warn` или `error`. По умолчанию `info`.
- `DATABASE_MODE`: режим operational database. Значения: `off`, `sqlite`, `postgres`. Дефолт приложения — `off`, чистая установка использует `sqlite`; `postgres` зарезервирован для будущей поддержки.
- `DATABASE_PATH`: путь к SQLite database. По умолчанию `/etc/awg-forge/awg-forge.db`.
- `DATABASE_RETENTION_DAYS`: default retention window для operational data. По умолчанию `90`.
- `DATABASE_BUSY_TIMEOUT`: SQLite busy timeout. По умолчанию `5s`.
- `DATABASE_QUERY_TIMEOUT`: таймаут операций с базой данных, для которых задан срок выполнения запроса. По умолчанию `2s`. При запуске контроллера, настройке auth runtime после активации, восстановлении незавершённой активации и запуске `db migrate` миграция схемы получает отдельный срок не менее `30s`. Открытие базы, проверка её состояния и настройка администратора используют свежие сроки запросов; операция `db retention apply` ограничена сроком не менее `30s`.
- `DATABASE_MAX_OPEN_CONNS`: лимит database connections. По умолчанию `1`.
- `DATABASE_MAX_IDLE_CONNS`: лимит idle connections. По умолчанию `1`.

## Инициализация первого туннеля

В новых установках `.env` хранит только runtime-настройки запуска, а настройки туннелей живут в `state.json`.

При чистой установке `install.sh` до запуска сервиса выполняет одноразовый контейнер `awg-forge init` и сразу создает `data/state.json` с выбранным первым туннелем. После этого `docker compose up -d` запускает уже готовое состояние, а туннели управляются через Web UI/API и сохраняются в `state.json`.

Установщик сначала спрашивает protocol profile, а уже потом настройки туннеля, чтобы профильные defaults совпадали. Если на вопросе профиля просто нажать Enter, будет выбран AWG 2.0. По умолчанию установщик выбирает свободный UDP-порт из `30000-49999`; значения ниже остаются defaults для ручного выбора:

| Профиль | Имя туннеля | Порт | Подсеть |
| --- | --- | --- | --- |
| `awg_legacy_1_0` | `awg0` | `51820` | `10.8.0.0/24` |
| `awg_1_5` | `awg15` | `51825` | `10.15.0.0/24` |
| `awg_2_0` | `awg20` | `51830` | `10.20.0.0/24` |

При создании следующих туннелей Web UI может автоматически выбрать свободный порт из эффективного диапазона. Порты ниже `1024` и распространённые инфраструктурные порты исключены из автоматического выбора. Выбранный порт сохраняется как обычное значение в `state.json` и не меняется после создания. Ручной выбор остаётся доступен, а backend отклоняет конфликты.

Настройки существующих туннелей читаются из `state.json` и не перезаписываются legacy tunnel-переменными из `.env`. Однако `DNS`, `ALLOWED_IPS`, `PERSISTENT_KEEPALIVE` и `MTU` по-прежнему задают значения по умолчанию для новых туннелей. `PROTOCOL_PROFILE` задает профиль по умолчанию, если запрос создания туннеля его не указывает. Новые туннели AWG 3.x используют fallback `1280` только при исходном MTU=`0`; явно заданное legacy-значение `MTU` сохраняется вместо него. Перед удалением legacy-переменных проверь настройки существующих туннелей и нужные defaults для новых. После изменения `.env` пересоздай контейнер.

## SESSION_SECRET

`SESSION_SECRET` можно не указывать. Если он отсутствует, awg-forge создаст и сохранит секрет в `state.json`.

Это нужно для подписи UI session cookie. Пользователю не нужно управлять этим вручную в обычном сценарии.

## SESSION_COOKIE_SECURE

`SESSION_COOKIE_SECURE` управляет флагом `Secure` у session cookie:

- `auto`: по умолчанию. Для `127.0.0.1`, `localhost`, `::1` cookie работает по HTTP без `Secure`; для внешних host cookie ставится с `Secure`.
- `true`: всегда ставить `Secure`. Используй для HTTPS/reverse proxy.
- `false`: не ставить `Secure`. Это позволяет логиниться через `http://domain:port`, но небезопасно для публичного интернета.

Если нужно открыть Web UI по обычному HTTP, лучше делать это только в доверенной сети или за отдельной защитой. Для production безопаснее оставить `WEBUI_HOST=127.0.0.1` и заходить через SSH tunnel, либо использовать HTTPS.

## TLS для Web UI

Конфигурация TLS хранится только в `CONFIG_DIR/tls/config.json`, отдельно от VPN `state.json` и ACME cache. Встроенные режимы:

- `off`: текущий HTTP workflow для loopback или SSH tunnel;
- `reverse-proxy`: HTTPS завершается в Caddy, Nginx или другом proxy;
- `manual`: awg-forge сам использует предоставленные certificate chain и private key.
- `acme-domain`: awg-forge получает и обновляет сертификат для одного публичного DNS-имени через HTTP-01.
- `acme-ip`: awg-forge получает и обновляет короткоживущий сертификат для одного публичного IP через HTTP-01.

DNS-01, wildcard certificates и TLS-ALPN-01 не реализованы.

### ACME Domain TLS

Используй этот режим, только когда A/AAAA-записи домена ведут на этот сервер и внешний TCP-порт `80` доступен. HTTP-01 всегда обслуживается на порту `80`; сам Web UI остаётся на `WEBUI_PORT`, в том числе на нестандартном порту. awg-forge не открывает host firewall или security group провайдера автоматически.

При чистой managed-установке выбери **ACME certificate** в `install.sh`. Затем установщик предложит публичный IPv4 wildcard bind `0.0.0.0`; при необходимости можно указать точный публичный адрес. В уже существующей установке сначала укажи публичный `WEBUI_HOST` в `.env`, оставь `SESSION_COOKIE_SECURE=auto` или `true` и `WEBUI_TRUST_PROXY_HEADERS=false`. Пересоздай контейнер, чтобы CLI проверил этот deployment context, затем настрой ACME:

```bash
cd /opt/awg-forge
# Измени .env: WEBUI_HOST=0.0.0.0 и WEBUI_TRUST_PROXY_HEADERS=false
docker compose up -d --force-recreate
docker exec awg-forge awg-forge tls use acme-domain \
  --domain panel.example.com \
  --email admin@example.com \
  --accept-tos
docker restart awg-forge
```

Режим требует non-loopback `WEBUI_HOST`, `PASSWORD`, `SESSION_COOKIE_SECURE=auto` или `true` и выключенные trusted proxy headers. HTTP-listener обслуживает только ACME challenge для настроенного домена; остальные запросы получают `404`. Обычные запросы этого домена перенаправляются на `https://panel.example.com:WEBUI_PORT/`.

Установщик запускает сервис, не ожидая выпуска сертификата. Первый HTTPS-запрос к настроенному домену запускает выпуск: установка не зависит от временной недоступности CA или сети. До этого `doctor`, `tls status` и Maintenance -> Support показывают `pending`; после успешного запроса — активный cached certificate. Эти проверки не могут подтвердить доступность порта `80` из Интернета.

### ACME IP TLS

Используй этот режим только для публично маршрутизируемого IPv4 или IPv6 сервера. IP-сертификаты Let's Encrypt используют профиль `shortlived` и действуют около шести дней; awg-forge начинает обновление за 72 часа до окончания. Доменные имена, private и loopback-адреса, а также DNS-01 для этого режима не принимаются.

Требования такие же, как для domain HTTP-01: non-loopback `WEBUI_HOST`, `PASSWORD`, `SESSION_COOKIE_SECURE=auto` или `true`, выключенные trusted proxy headers и внешний доступ к TCP/80. Bind Web UI должен совпадать с семейством IP сертификата: для IPv4 используй `WEBUI_HOST=0.0.0.0` (или точный адрес), для IPv6 — `WEBUI_HOST=::` (или точный адрес). Это намеренно не полагается на неявное dual-stack поведение. При чистой установке installer сначала спросит IP сертификата и автоматически предложит соответствующий bind. Поддержка IPv6 здесь относится только к listener и сертификату Web UI; IPv6 egress туннелей и IPv6-адреса клиентов пока не поддерживаются. Настрой существующую установку так:

```bash
cd /opt/awg-forge
# Укажи в .env WEBUI_HOST=0.0.0.0 для IPv4 или WEBUI_HOST=:: для IPv6.
docker compose up -d --force-recreate
docker exec awg-forge awg-forge tls use acme-ip \
  --ip <public-ipv4-or-ipv6> --email admin@example.com --accept-tos
docker restart awg-forge
```

Работающий сервис начинает первую попытку выпуска сразу после готовности HTTPS и HTTP-01 listener. При ошибке он не переходит на публичный HTTP. `tls status`, `doctor` и Maintenance покажут безопасный статус ошибки и время следующей попытки. Повторы используют ограниченный backoff, поэтому после исправления адреса или TCP/80 перезапуск не нужен.

Материалы ACME account и сертификаты хранятся в `CONFIG_DIR/tls/acme` с правами каталога `0700`. Они попадают только в зашифрованный backup awg-forge и никогда в support bundle. Сертификат обновляет работающий процесс.

#### Проблемы выпуска ACME и восстановление

Если выпуск сертификата не удался, awg-forge не переключается на публичный HTTP Web UI. HTTPS handshake не пройдёт, пока проблема не исправлена: это не даёт незаметно понизить защищённую сессию до HTTP. Сначала проверь состояние:

```bash
docker exec awg-forge awg-forge tls status
docker exec awg-forge awg-forge doctor
```

Для domain mode убедись, что точные A/AAAA-записи ведут на этот сервер. Для IP mode убедись, что настроенный публичный IP маршрутизируется на этот сервер. В обоих режимах входящий TCP/80 должен быть доступен из Интернета, а порт `80` не должен быть занят другим сервисом или proxy.

ACME IP повторяет попытку автоматически во время, указанное в `tls status` и Doctor. После ошибки первого выпуска для домена исправь DNS/TCP/80 и повтори HTTPS-запрос к настроенному домену после задержки между попытками. Domain mode не показывает время следующей попытки.

Если вместо этого нужно вернуть доступ только через SSH, используй процедуру ниже. Она работает и при restart loop основного контейнера, когда `docker exec` недоступен.

Чтобы убрать публичный ACME-доступ и вернуть панель к доступу только через SSH, сначала выключи TLS, а затем измени `WEBUI_HOST` на loopback:

```bash
cd /opt/awg-forge
docker compose run --rm --no-deps awg-forge tls disable
# Укажи WEBUI_HOST=127.0.0.1 в .env, затем:
docker compose up -d --force-recreate
```

`docker compose run` работает и при restart loop основного контейнера: он запускает отдельный одноразовый CLI-контейнер с тем же volume `data/`. Повторный запуск `install.sh` с выбором **Reconfigure** предложит этот шаг автоматически, если увидит ACME TLS и loopback bind.

### Manual TLS

Private key должен быть regular file с правами `0600` в каталоге с правами `0700`; symbolic links отклоняются. До запуска awg-forge проверяет PEM parsing, соответствие certificate/key, срок действия certificate и настроенное server name по SAN certificate. Если manual TLS невалиден, перехода на HTTP не будет.

Сохрани проверенную manual-конфигурацию через CLI контейнера:

```bash
docker exec awg-forge awg-forge tls use manual \
  --cert /mnt/awg-forge-tls/fullchain.pem \
  --key /mnt/awg-forge-tls/privkey.pem \
  --server-name panel.example.com
docker restart awg-forge
```

Все TLS-режимы, включая `off`, хранятся в `CONFIG_DIR/tls/config.json` с правами `0600`. Это единственный источник TLS-конфигурации; `.env` задает только deployment context: bind-адрес Web UI, policy session cookie и trusted proxy CIDR.

```bash
docker exec awg-forge awg-forge tls disable
docker restart awg-forge
```

`tls status` показывает configured settings; `Maintenance` -> `Support` показывает TLS runtime текущего процесса.

Если manual certificate находится вне `./data`, добавь явный read-only mount в `docker-compose.yml`:

```yaml
volumes:
  - ./data:/etc/awg-forge
  - /srv/awg-forge/manual-tls:/mnt/awg-forge-tls:ro
```

Encrypted backup сохраняет `tls/config.json` и встроенный ACME cache. Certificate и key из внешнего mount в backup не копируются; их нужно хранить отдельно.

Проверить активный режим и безопасные metadata certificate без вывода PEM и key paths:

```bash
docker exec awg-forge awg-forge tls status
```

Та же безопасная сводка режима, certificate и trusted proxy доступна в `Maintenance` -> `Support` без PEM, private keys и file paths.

### Reverse Proxy

Когда возможно, оставь `WEBUI_HOST=127.0.0.1` и настрой HTTPS в proxy. При переходе с ACME или manual TLS сначала сохрани `off` через одноразовый CLI-контейнер:

```bash
cd /opt/awg-forge
docker compose run --rm --no-deps awg-forge tls disable
```

Затем задай trusted forwarded headers и явные CIDR proxy в `.env`:

```env
WEBUI_TRUST_PROXY_HEADERS=true
WEBUI_TRUSTED_PROXY_CIDRS=127.0.0.1/32,::1/128
```

Перезагрузи runtime-конфигурацию, сохрани reverse-proxy mode, затем перезапусти процесс, чтобы он перечитал TLS-файл:

```bash
cd /opt/awg-forge
docker compose up -d --force-recreate
docker exec awg-forge awg-forge tls use reverse-proxy
docker restart awg-forge
```

Proxy должен сохранять request `Host` и передавать `X-Forwarded-Proto: https`. awg-forge принимает только `http` или `https` от прямого peer из настроенных CIDR; spoofed headers от обычных клиентов игнорируются. Определённая схема управляет `Secure` cookie и Origin/Referer validation.

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

Значение MTU по умолчанию зависит от профиля:

| Профиль | Новый туннель без заданного MTU |
| --- | --- |
| Legacy / 1.0, AWG 1.5, AWG 2.0 | Сохраняет `0` (`Авто`) и не добавляет `MTU = ...` в серверный и клиентский конфиги |
| AWG 3.x | Сохраняет и добавляет `1280` как консервативное значение совместимости |

Для стабильных профилей режим `Авто` передает выбор на сервере закрепленному `awg-quick`. Он определяет MTU маршрута или интерфейса для известных адресов пиров, затем использует маршрут по умолчанию, а при отсутствии данных — `1500`, после чего вычитает `80`. Поэтому для обычного интерфейса с MTU `1500` туннель получает `1420`. Это локальная эвристика по маршруту и интерфейсу, а не сквозное определение PMTU. Если клиентский конфиг также не содержит MTU, клиент выбирает значение самостоятельно, поэтому эффективные значения на сервере и клиенте могут отличаться.

AWG 3.x получает явный `1280` только при создании нового туннеля, когда исходное значение настройки равно `0`. Это запасное значение совместимости, выбранное в awg-forge, а не требование протокола AWG. `1280` является минимальным MTU канала для IPv6. awg-forge сейчас создает клиентские маршруты только для IPv4, поэтому минимум IPv6 здесь служит консервативным инженерным ориентиром, а не протокольной гарантией. Меньшее значение также оставляет больше места для накладных данных экспериментального профиля и не зависит от платформенного значения клиента.

Это не измеренный PMTU и не гарантия для вложенных VPN, PPPoE, оверлеев или другого пути с уменьшенным MTU. Существующие туннели AWG 3.x с `MTU=0` не мигрируются, а последующий выбор `Авто` остается явным решением оператора.

Если ты явно задаешь MTU на туннеле, то он рендерится одинаково в серверный и клиентский конфиг. Значения существующих туннелей не мигрируются и не заменяются.

Клиент всё ещё может перезаписать экспортированный MTU при импорте. На дату проверки 2026-09-03 [PR AmneziaVPN #3113 о сохранении MTU](https://github.com/amnezia-vpn/amnezia-client/pull/3113) и [PR #3065 о значении по умолчанию](https://github.com/amnezia-vpn/amnezia-client/pull/3065) открыты; это не гарантия совместимости выпущенного клиента. Проверяй эффективный MTU на обеих сторонах, особенно если handshake проходит, но крупные пакеты зависают. Изменение серверного экспорта само по себе не исправит клиент, который перезаписывает значение.

WARP не связан с выбором MTU профиля и сохраняет собственное значение по умолчанию `1280`.

Практически:

- `Авто` сохраняет историческое поведение Legacy, 1.5 и 2.0, но не измеряет полный сетевой путь;
- `1280` часто помогает на проблемных сетях, мобильных сетях, роутерах и сложных маршрутах;
- Web UI предлагает `Авто`, частые значения и `Произвольный` для явного MTU;
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

WARP не является protocol profile AmneziaWG. Это режим outbound routing для уже существующих туннелей. Любой доступный профиль туннеля, включая экспериментальный AWG 3.x, может независимо использовать WAN или WARP egress.

Рекомендуемый путь:

1. Выбери `Cloudflare WARP` в поле `Egress` при создании туннеля или открой `Tunnel settings` у существующего туннеля.
2. Переключи `Egress` с `Server WAN` на `Cloudflare WARP`.
3. Нажми `Create tunnel` или `Save`.

Если WARP еще не настроен, awg-forge автоматически зарегистрирует Cloudflare WARP, создаст общий outbound-интерфейс `warp0`, применит runtime routing/NAT и затем переключит туннель на WARP egress.

`Maintenance` -> `WARP` нужен для обслуживания: посмотреть статус, вручную зарегистрировать или перерегистрировать WARP, перезапустить `warp0`, удалить WARP config, либо импортировать config вручную.

Ручной импорт нужен только как fallback, если у тебя уже есть готовый Cloudflare WARP WireGuard/AmneziaWG config из внешнего генератора или WARP client tool. В этом случае открой `Manual WARP config import`, вставь весь config целиком и нажми `Import WARP config`.

Клиентские конфиги не нужно менять, если меняется только egress mode: клиент продолжает подключаться к тому же AmneziaWG endpoint. Меняются только server-side routing/NAT rules.

Doctor проверяет WARP runtime, policy rules и WARP-aware firewall expectations для туннелей, которые используют WARP.

## Экспериментальный профиль AWG 3.x

AWG 3.x входит в стандартный Docker-образ как один экспериментальный профиль. Его выбор в Web UI является явным согласием: upstream-реализация семейства 3.x продолжает меняться, поэтому включение и использование всего профиля выполняются на свой риск. Существующие туннели не преобразуются, а AWG 2.0 остается профилем по умолчанию и поддерживаемым production-вариантом.

Образ закрепляет `amneziawg-go` 3.1.20260828 и `amneziawg-tools` 3.1.20260812, принудительно запускает AWG 3.x через userspace и показывает профиль `awg_3` без отдельной переменной окружения, образа или Compose override. Профиль переиспользует тот же генератор QUIC Initial-like `I1`, что и AWG 2.0.

Экспорт клиентской конфигурации поддерживает скачивание `.conf`, QR для AmneziaWG с тем же исходным конфигом, структурированный QR для AmneziaVPN и `vpn://` с `.conf`, закодированным как base64url. Минимальная поддерживаемая версия AmneziaVPN для AWG 3.1 — 5.0.1.5; версия 5.0.0.5 несовместима. При проблеме импорта на конкретном клиенте или платформе стабильным резервным способом остается `.conf`.

Сгенерированные параметры Header Protection соответствуют upstream-правилам совместимости. `RandomTrailers` и `DisableCookies` по умолчанию выключены; вне контролируемого теста оставляй оба параметра в таком состоянии. `DisableCookies` прекращает отправку Cookie Reply и заставляет закрепленный runtime обходить принимающий under-load путь cookie challenge и rate limit, существенно ослабляя защиту от handshake-нагрузки. Точные правила параметров, runtime-ограничения, известные upstream-проблемы, статус проверки и ссылки на источники собраны в [матрице протоколов](protocol-matrix.md#границы-экспериментального-awg-3x).

Серверный конфиг, скачиваемый `.conf`, структурированный QR для AmneziaVPN и raw-config `vpn://` используют один MTU туннеля. Значения по умолчанию и ограничения импорта клиента описаны в разделе [MTU](#mtu).

Ручная end-to-end проверка подтвердила импорт `.conf`, handshake, передачу трафика, восстановление после restart, WAN egress и WARP egress с совместимыми клиентами AmneziaVPN, AmneziaWG и DefaultVPN. Это не гарантирует совместимость с каждой платформой, сборкой клиента, сетью или будущим runtime 3.x.

Перед возвратом на релиз без поддержки `awg_3` скачай нужные клиентские конфиги и удали все туннели AWG 3.x. Старый binary не сможет интерпретировать такой профиль в `state.json`.

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

В Web UI вкладка `Maintenance` -> `Аудит` автообновляется, пока открыта вкладка `Аудит`, и показывает новые события сверху.

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

## Runtime-логи

Runtime-логи в формате JSON пишутся в stderr контейнера и читаются через Docker. Они предназначены для lifecycle сервиса, ошибок runtime apply, операций WARP/firewall, ошибок traffic history и HTTP-ошибок. Это отдельный поток, не заменяющий постоянный audit trail.

```bash
docker compose logs -f awg-forge
docker compose logs --tail 200 awg-forge
docker compose logs --no-log-prefix awg-forge | jq 'select(.level == "ERROR")'
```

Включай `LOG_LEVEL=debug` только на время диагностики, затем верни `info` и пересоздай контейнер. Debug добавляет operational context, но использует те же правила редактирования секретов. Runtime-логи не содержат паролей, session cookies, private keys, preshared keys, полных конфигов, QR payload, тел запросов и query strings.

Новые managed Compose-файлы используют Docker logging driver `local`: размер `10m`, хранится три файла. В custom Compose-инсталляции политика logging driver остаётся за оператором.

## Operational Database

Если `DATABASE_MODE` отсутствует, дефолт приложения — `off`: существующая установка остается file-based, и база не создается. Чистая установка текущим installer использует `DATABASE_MODE=sqlite`; существующая установка не меняется, пока SQLite явно не включен через `install.sh upgrade`.

`DATABASE_MODE=sqlite` включает локальную operational history для индексированных audit events и traffic usage. JSONL остается надежным локальным audit trail. Схема резервирует таблицы для login, health и TLS history, но эти записи пока не собираются. Он не переносит `state.json`, private keys, WARP tokens, raw configs, QR payloads или import links в базу.

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

Когда SQLite включен и миграции применены, audit events пишутся и в существующий JSONL audit log, и в `audit_events`. `Maintenance` -> `Аудит` и `awg-forge logs` объединяют события из SQLite и JSONL, а если SQLite недоступен, читают JSONL. Это не дает проблемам SQLite mirror скрыть события, которые попали в `audit.log`.

Когда SQLite включен, миграции применены и `APPLY_CONFIG=true`, awg-forge раз в минуту снимает runtime transfer counters и хранит дневные aggregates трафика по клиентам. Первый sample задает baseline и не считается переданным трафиком. Строки клиентов показывают общий записанный трафик, а `Maintenance` -> `Traffic` показывает aggregate totals за сегодня, 7 дней и 30 дней по всем клиентам и туннелям.

При создании клиента и в настройках клиента можно сохранить optional traffic limit, если включен SQLite. Web UI принимает MiB, GiB или TiB. Лимит может действовать за всё записанное время (`За всё время`) или за предыдущие 30 дней UTC (`Последние 30 дней (UTC)`); существующие лимиты после миграции остаются пожизненными. Безлимитный режим не хранит строку лимита.

Когда записанный трафик достигает или превышает лимит, awg-forge записывает маркер блокировки, отключает клиента через обычный render/apply path и пишет audit event. Если маркер записать не удалось, клиент остаётся включённым, а применение лимита повторяется при следующем проходе. Попытки включить клиента отклоняются, пока лимит активного периода исчерпан. Скользящее окно сдвигается по мере того, как дневные агрегаты UTC выходят за 30 дней. awg-forge автоматически включает только клиентов, которых сам отключил из-за этой квоты; ручное отключение очищает маркер квоты и никогда не отменяется автоматически. HTTP API возвращает `409 Conflict`; CLI возвращает ошибку.

Явный `install.sh join --mode fresh` — отдельный путь: без `init`/default tunnel, с loopback local UI, защищёнными credentials и `DATABASE_MODE=off`. Existing join/rebind используют environment/mounts собственного service без переписывания Compose и deployment settings; см. [Quick Install](quick-install.md).
