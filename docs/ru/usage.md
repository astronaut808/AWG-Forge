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

- `Create tunnel`: создать новый туннель внутри выбранного профиля.
- `Create client`: создать клиента внутри конкретного туннеля.
- `Config`: скачать `.conf` существующего клиента.
- `Settings`: настройки туннеля.
- `Protocol`: protocol params и regenerate.
- `Health`: handshake и runtime traffic counters по клиентам.
- `Doctor`: системная и runtime диагностика.
- `Updates`: проверка, есть ли новые upstream refs у используемых AmneziaWG tools.
- `Restart`: перезапустить туннель.
- `Delete`: удалить туннель или клиента.

## Stale Configs

Изменение настроек туннеля или protocol params может сделать старые клиентские конфиги неактуальными.

После таких изменений скачай свежий `.conf` для затронутых клиентов.

## CLI В Docker

```bash
docker exec awg-forge awg-forge doctor
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
awg-forge updates
```

## Client Config Import

Поддерживаемый путь — `.conf` файл.

QR import не показывается в UI и не поддерживается как product path.
