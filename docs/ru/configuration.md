# Конфигурация

Основной пример находится в [.env.example](../../.env.example).

## Основные переменные

- `SERVER_HOST`: публичный DNS name или IP, к которому подключаются клиенты.
- `TUNNEL_NAME`: имя первого туннеля и интерфейса, например `awg0`, `awg15` или `awg20`.
- `LISTEN_PORT`: порт первого туннеля по умолчанию.
- `WEBUI_HOST`: адрес Web UI. По умолчанию `127.0.0.1`.
- `WEBUI_PORT`: порт Web UI. По умолчанию `51821`.
- `PASSWORD`: пароль Web UI. Обязателен для публичного bind и рекомендуется всегда.
- `SESSION_COOKIE_SECURE`: режим Secure cookie для UI session. Значения: `auto`, `true`, `false`. По умолчанию `auto`.
- `EXTERNAL_INTERFACE`: внешний интерфейс сервера, через который идет egress. Часто это `eth0` или `ens3`. В bridge networking внутри контейнера обычно `eth0`.
- `IPV4_SUBNET`: subnet первого туннеля, например `10.8.0.0/24`.
- `DNS`: DNS, который попадет в клиентские конфиги.
- `ALLOWED_IPS`: client-side allowed IPs. Обычно `0.0.0.0/0`.
- `PERSISTENT_KEEPALIVE`: значение `PersistentKeepalive` в клиентском конфиге. По умолчанию `0`.
- `MTU`: tunnel MTU. `0` означает auto/omit, то есть строка `MTU = ...` не будет рендериться. Частые явные значения: `1280`, `1380`, `1400`, `1420`.
- `PROTOCOL_PROFILE`: профиль первого туннеля. Обычно `awg_legacy_1_0`.
- `APPLY_CONFIG`: если `true`, awg-forge применяет runtime-изменения через AmneziaWG tools.
- `PUBLISHED_UDP_PORTS`: опубликованные Docker UDP-порты/диапазоны, например `51820-51840,7443`.
- `AUDIT_LOG_ENABLED`: включает безопасный audit log. По умолчанию `true`.
- `AUDIT_LOG_PATH`: путь к audit log. По умолчанию `/etc/awg-forge/audit.log`.
- `AUDIT_LOG_MAX_SIZE`: размер файла до ротации. По умолчанию `5242880`.
- `AUDIT_LOG_MAX_FILES`: сколько rotated-файлов хранить. По умолчанию `3`.

Quick installer сначала спрашивает `PROTOCOL_PROFILE`, а уже потом настройки туннеля, чтобы профильные defaults совпадали:

| Профиль | Имя туннеля | Порт | Подсеть |
| --- | --- | --- | --- |
| `awg_legacy_1_0` | `awg0` | `51820` | `10.8.0.0/24` |
| `awg_1_5` | `awg15` | `51825` | `10.15.0.0/24` |
| `awg_2_0` | `awg20` | `51830` | `10.20.0.0/24` |

При создании следующих туннелей в Web UI awg-forge предлагает свободные имена, порты и подсети с учётом всех профилей. Backend всё равно отклоняет ручные конфликты.

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

## SERVER_HOST и endpoint туннеля

`SERVER_HOST` задает глобальный host, который awg-forge использует в `Endpoint = <host>:<port>` для клиентских `.conf`.

В Web UI у каждого туннеля есть поле `Server host`. Если оно пустое, туннель наследует глобальный `SERVER_HOST`. Если указать значение, оно переопределит endpoint только для этого туннеля.

Это удобно, когда разные туннели публикуются через разные поддомены, например:

```text
legacy.example.com:44865
awg20.example.com:44867
```

Важно:

- `Server host` не должен содержать схему, путь или порт;
- порт берется из настроек туннеля;
- после изменения host клиентам нужно скачать свежий `.conf`;
- существующие импортированные клиенты не обновятся сами.

## MTU

`MTU=0` означает, что awg-forge не добавляет строку `MTU = ...` в server/client configs.

Если ты явно задаешь MTU на туннеле, то он рендерится одинаково в серверный и клиентский конфиг. awg-forge не подменяет MTU скрытыми решениями.

Практически:

- `Auto` подходит как стартовое значение;
- `1280` часто помогает на проблемных сетях, мобильных сетях, роутерах и сложных маршрутах;
- после изменения MTU клиентам нужно скачать свежий `.conf`.

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
