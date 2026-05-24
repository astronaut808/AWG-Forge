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
- `Health`: handshake и runtime traffic counters по клиентам.
- `Restart`: перезапустить туннель.
- `Delete`: удалить туннель или клиента.

Maintenance actions доступны через кнопку `Maintenance`:

- `Doctor`: системная и runtime диагностика.
- `Repair firewall`: ручное восстановление managed firewall rules из Doctor modal.
- `Backup`: скачать encrypted backup с отдельным паролем.
- `Support bundle`: скачать support bundle без секретов.
- `Updates`: проверка, есть ли новые upstream refs у используемых AmneziaWG tools.
- `Restore`: подсказка по CLI-only restore.

## Stale Configs

Изменение настроек туннеля или protocol params может сделать старые клиентские конфиги неактуальными.

После таких изменений затронутые клиенты показывают badge `stale`, пока для них не скачан свежий `.conf`.

Client rename и notes — metadata-only изменения, они не делают configs stale.

## CLI В Docker

```bash
docker exec awg-forge awg-forge doctor
docker exec -e BACKUP_PASSWORD='long-random-backup-password' awg-forge awg-forge backup /tmp/awg-forge.afbackup
docker exec -e BACKUP_PASSWORD='long-random-backup-password' awg-forge awg-forge restore verify /tmp/awg-forge.afbackup
docker exec -e BACKUP_PASSWORD='long-random-backup-password' awg-forge awg-forge restore /tmp/awg-forge.afbackup
docker exec awg-forge awg-forge firewall check
docker exec awg-forge awg-forge firewall repair
docker exec awg-forge awg-forge support-bundle
docker exec awg-forge awg-forge updates
docker exec awg-forge awg-forge client add phone
docker exec awg-forge awg-forge client add laptop awg15
docker exec awg-forge awg-forge client config <client-id>
docker exec awg-forge awg-forge client disable <client-id>
docker exec awg-forge awg-forge client enable <client-id>
docker exec awg-forge awg-forge client remove <client-id>
docker exec awg-forge awg-forge tunnel create awg_1_5 awg15 51825 10.15.0.0/24
docker exec awg-forge awg-forge tunnel restart
```

## Локальный CLI

```bash
awg-forge init
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
```

## Client Config Import

Поддерживаемый путь — `.conf` файл.

Действие `Import key` экспериментальное. Оно возвращает `vpn://` key, внутри которого лежит тот же сгенерированный клиентский конфиг, закодированный для AmneziaVPN-style text import. Мы проверили его на iOS, но сам формат не iOS-specific. Используй его только для проверки совместимости с AmneziaVPN или DefaultVPN. Для роутеров, native AmneziaWG app и production fallback нужно продолжать использовать `.conf`.

QR import не показывается в UI и не поддерживается как product path.
