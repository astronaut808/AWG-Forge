# Web UI и CLI

## Web UI

Основной сценарий:

1. Открой UI через SSH tunnel или защищенный admin endpoint.
2. Войди по паролю.
3. При необходимости переключи язык панели кнопкой `RU` / `EN` в верхней панели. Выбор сохраняется в браузере.
4. Выбери вкладку профиля: `1.0`, `1.5` или `2.0`.
5. Создай туннель, если он еще не создан.
6. Создай клиента внутри нужного туннеля.
7. Открой `Config` у клиента.
8. Выбери AmneziaVPN QR, AmneziaWG `.conf` QR, скачивание `.conf` или копирование `vpn://` ключа.
9. Импортируй конфиг в совместимый клиент AmneziaWG или AmneziaVPN.

## Действия в UI

Действия с туннелями:

- `Create tunnel`: создать новый туннель внутри выбранного профиля.
- `Create client`: создать клиента внутри конкретного туннеля.
- `Config`: выбрать способ импорта клиента: AmneziaVPN QR, AmneziaWG `.conf` QR, скачивание `.conf` или копирование `vpn://` ключа.
- `Edit`: переименовать клиента или сохранить админские заметки без изменения VPN-конфига.
- `Settings`: настройки туннеля, включая переопределение `Server host` для отдельного туннеля.
- `Protocol`: параметры протокола и перегенерация.
- `Restart`: перезапустить туннель.
- `Delete`: удалить туннель или клиента.

Действия обслуживания доступны через кнопку `Maintenance`:

- `Overview`: общий статус runtime, клиентов, firewall и восстановления.
- `Doctor`: системная и runtime-диагностика с группировкой OK/WARN/FAIL.
- `Firewall`: статус managed firewall rules по туннелям и восстановление правил awg-forge.
- `Backup`: скачать зашифрованный backup с отдельным паролем.
- `Restore`: проверить `.afbackup` в dry-run режиме без записи в `CONFIG_DIR`; настоящий restore остается CLI-only.
- `Updates`: проверка, есть ли новые upstream refs у используемых AmneziaWG tools.
- `Support`: скачать support bundle без секретов.
- `Logs`: посмотреть последние безопасные события аудита. Панель автообновляется, пока открыт Maintenance, и показывает новые события сверху.
- `System`: текущий режим, server host, туннели, профили и полезные команды.

## Устаревшие конфиги

Изменение настроек туннеля или параметров протокола может сделать старые клиентские конфиги неактуальными.

После таких изменений затронутые клиенты показывают badge `stale`, пока для них не экспортирован свежий конфиг через `Config`.

Переименование клиента и notes — metadata-only изменения, они не делают конфиги устаревшими.

## Runtime-статус клиента

Список клиентов показывает два разных типа состояния:

- `enabled` / `disabled`: клиент разрешен или отключен в конфиге awg-forge;
- `active now`, `seen recently`, `offline`, `never seen`: примерный runtime-статус из `awg show` и сохраненного `last_seen_at`;
- `last seen`, `received`, `sent`: время последнего handshake и runtime-счетчики со стороны сервера.

AmneziaWG/WireGuard не держит постоянное TCP-like соединение, поэтому `active now` — это приблизительный online-индикатор, а не строгий online/offline статус. В dashboard active означает handshake младше примерно 3 минут. UI также показывает `received` / `sent` counters, если runtime их отдает.

Когда runtime сообщает handshake, awg-forge сохраняет в `state.json`, что клиент уже подключался, и время последнего handshake. После рестарта интерфейса клиент может показываться как `last seen`, пока не появится новый runtime handshake.

Doctor может предупреждать о клиентах, у которых еще не было handshake. Это полезно для поиска неиспользуемых или неправильно импортированных конфигов, но не означает, что весь туннель сломан, если другие клиенты в этом же туннеле работают.

Для глубокой диагностики используй `Maintenance` -> `Doctor`.

## Срок действия клиента

При создании или редактировании клиента можно выбрать срок действия:

- `Never expires`;
- `1 hour`;
- `1 day`;
- `7 days`;
- `30 days`;
- своя дата и время.

Если срок истек, клиент остается в UI и `state.json`, но считается `expired` и больше не рендерится в server config как peer. Это безопаснее удаления: сохраняются имя, notes, last seen и история в support bundle. В UI это отображается как `expired`.

В режиме `serve` awg-forge периодически применяет истекшие сроки и перерендеривает затронутые туннели. Обычно это занимает до одной минуты после фактического истечения.

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

## Импорт конфига клиента

Самый надежный путь — импорт `.conf` файла. UI также дает отдельные QR-варианты для разных официальных клиентов. Любой вариант содержит секреты клиента, поэтому показывай QR только на доверенном экране и не пересылай публично.

Вариант `AmneziaVPN` показывает QR-код для импорта в AmneziaVPN. Payload — это JSON wrapper с `last_config`, сжатый через zlib, обернутый в Qt/qCompress-style binary header, который ожидает AmneziaVPN, и закодированный как base64url перед генерацией QR. Если конкретная сборка AmneziaVPN его не сканирует, используй fallback через `.conf`.

Вариант `AmneziaWG` показывает QR с raw full `.conf`. Он предназначен для AmneziaWG-compatible клиентов, которые умеют сканировать config QR. AmneziaVPN на некоторых платформах может игнорировать raw `.conf` QR.

В действии `Config` доступны три варианта:

- `AmneziaVPN`: AmneziaVPN-compatible QR import;
- `AmneziaWG`: QR с raw full `.conf` для AmneziaWG-compatible import;
- `.conf / vpn://`: надежный fallback для AmneziaWG, AmneziaVPN, роутеров и ручного импорта, плюс копирование `vpn://` ключа для клиентов, которые поддерживают текстовый импорт.

Если официальный клиент не импортирует QR на конкретной платформе или версии, скачай и импортируй `.conf` файлом.
