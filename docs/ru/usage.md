# Web UI и CLI

## Web UI

Основной workflow:

1. Открой UI через SSH tunnel или защищенный admin endpoint.
2. Войди по паролю.
3. Выбери вкладку профиля: `1.0`, `1.5` или `2.0`.
4. Создай туннель, если он еще не создан.
5. Создай клиента внутри нужного туннеля.
6. После успешного создания клиента `.conf` скачается автоматически.
7. Импортируй `.conf` в совместимый клиент AmneziaVPN.

## Действия В UI

Tunnel actions:

- `Create tunnel`: создать новый туннель внутри выбранного профиля.
- `Create client`: создать клиента внутри конкретного туннеля.
- `Config`: скачать `.conf` существующего клиента.
- `Import key`: сгенерировать experimental `vpn://` key для проверки в AmneziaVPN / DefaultVPN.
- `Edit`: переименовать клиента или сохранить admin-only notes без изменения VPN-конфига.
- `Settings`: настройки туннеля, включая optional per-tunnel `Server host` endpoint override.
- `Protocol`: protocol params и regenerate.
- `Restart`: перезапустить туннель.
- `Delete`: удалить туннель или клиента.

Maintenance actions доступны через кнопку `Maintenance`:

- `Overview`: общий статус runtime, clients, firewall и recovery.
- `Doctor`: системная и runtime диагностика с группировкой OK/WARN/FAIL.
- `Firewall`: статус managed firewall rules по туннелям и repair action.
- `Backup`: скачать encrypted backup с отдельным паролем.
- `Restore`: проверить `.afbackup` через dry-run без записи в `CONFIG_DIR`; настоящий restore остается CLI-only.
- `Updates`: проверка, есть ли новые upstream refs у используемых AmneziaWG tools.
- `Support`: скачать support bundle без секретов.
- `Logs`: посмотреть последние безопасные audit events. Панель автообновляется, пока открыт Maintenance, и показывает новые события сверху.
- `System`: текущий режим, server host, tunnels, profiles и полезные команды.

## Stale Configs

Изменение настроек туннеля или protocol params может сделать старые клиентские конфиги неактуальными.

После таких изменений затронутые клиенты показывают badge `stale`, пока для них не скачан свежий `.conf`.

Client rename и notes — metadata-only изменения, они не делают configs stale.

## Client Runtime Status

Список клиентов показывает два разных типа состояния:

- `enabled` / `disabled`: клиент разрешен или отключен в конфиге awg-forge;
- `active now`, `seen recently`, `offline`, `never seen`: примерный runtime status из `awg show` и сохраненного `last_seen_at`;
- `last seen`, `received`, `sent`: время последнего handshake и runtime counters со стороны сервера.

AmneziaWG/WireGuard не держит постоянное TCP-like соединение, поэтому `active now` — это приблизительный online-индикатор, а не строгий online/offline статус. В dashboard active означает handshake младше примерно 3 минут. UI также показывает `received` / `sent` counters, если runtime их отдает.

Когда runtime сообщает handshake, awg-forge сохраняет в `state.json`, что клиент уже подключался, и время последнего handshake. После рестарта интерфейса клиент может показываться как `last seen`, пока не появится новый runtime handshake.

Doctor может предупреждать о клиентах, у которых еще не было handshake. Это полезно для поиска неиспользуемых или неправильно импортированных конфигов, но не означает, что весь tunnel сломан, если другие клиенты в этом же tunnel работают.

Для глубокой диагностики используй `Maintenance` -> `Doctor`.

## Client Expiration

При создании или редактировании клиента можно выбрать срок действия:

- `Never expires`;
- `1 hour`;
- `1 day`;
- `7 days`;
- `30 days`;
- custom date and time.

Если срок истек, клиент остается в UI и `state.json`, но считается `expired` и больше не рендерится в server config как peer. Это безопаснее удаления: сохраняются имя, notes, last seen и история в support bundle. В UI это отображается как `expired` / `not rendered since <date>`.

В режиме `serve` awg-forge периодически применяет истекшие сроки и перерендеривает затронутые tunnels. Обычно это занимает до одной минуты после фактического истечения.

## CLI В Docker

```bash
docker exec awg-forge awg-forge doctor
docker exec -e BACKUP_PASSWORD='long-random-backup-password' awg-forge awg-forge backup /tmp/awg-forge.afbackup
docker cp awg-forge:/tmp/awg-forge.afbackup ./awg-forge-backup-YYYYMMDD-HHMMSS.afbackup
docker cp ./<backup-file>.afbackup awg-forge:/tmp/backup.afbackup
docker exec -e BACKUP_PASSWORD='long-random-backup-password' awg-forge awg-forge restore verify /tmp/backup.afbackup
docker exec -e BACKUP_PASSWORD='long-random-backup-password' awg-forge awg-forge restore /tmp/backup.afbackup
docker exec awg-forge awg-forge tunnel restart
docker exec awg-forge awg-forge firewall repair
docker exec awg-forge awg-forge firewall check
docker exec awg-forge awg-forge support-bundle
docker exec awg-forge awg-forge updates
docker exec awg-forge awg-forge logs
docker exec awg-forge awg-forge logs --tail 200 --level error
docker exec awg-forge awg-forge client add phone
docker exec awg-forge awg-forge client add laptop awg15
docker exec awg-forge awg-forge client config <client-id>
docker exec awg-forge awg-forge client disable <client-id>
docker exec awg-forge awg-forge client enable <client-id>
docker exec awg-forge awg-forge client remove <client-id>
docker exec awg-forge awg-forge tunnel create awg_1_5 awg15 51825 10.15.0.0/24
```

## Локальный CLI

```bash
awg-forge init
awg-forge init --server-host vpn.example.com --external-interface eth0 --profile awg_2_0 --tunnel-name awg20 --listen-port 51830 --ipv4-subnet 10.20.0.0/24
awg-forge serve
awg-forge render
awg-forge doctor
BACKUP_PASSWORD='long-random-backup-password' awg-forge backup ./awg-forge.afbackup
BACKUP_PASSWORD='long-random-backup-password' awg-forge restore verify ./awg-forge.afbackup
BACKUP_PASSWORD='long-random-backup-password' awg-forge restore ./awg-forge.afbackup
awg-forge firewall check
awg-forge firewall repair
awg-forge support-bundle
awg-forge updates
awg-forge logs
```

## Client Config Import

Поддерживаемый путь — `.conf` файл.

Действие `Import key` экспериментальное. Оно возвращает `vpn://` key, внутри которого лежит тот же сгенерированный клиентский конфиг, закодированный для AmneziaVPN-style text import. Мы проверили его на iOS, но сам формат не iOS-specific. Используй его только для проверки совместимости с AmneziaVPN или DefaultVPN. Для роутеров, native AmneziaWG app и production fallback нужно продолжать использовать `.conf`.

QR import пока не показывается в UI и не поддерживается как product path. Future QR support должен быть отдельной проверенной совместимостью:

- native AmneziaWG app: QR от полного `.conf`, например `qrencode -t ansiutf8 < tunnel.conf`;
- AmneziaVPN: отдельная проверка формата импорта, потому что поведение может отличаться от native AmneziaWG.
