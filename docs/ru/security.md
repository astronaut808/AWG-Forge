# Безопасность

## Web UI Bind

По умолчанию Web UI слушает:

```env
WEBUI_HOST=127.0.0.1
WEBUI_PORT=51821
```

Production-рекомендация: держать UI на loopback и заходить через SSH tunnel:

```bash
ssh -L 51821:127.0.0.1:51821 user@server
```

Если UI публикуется наружу, пароль обязателен.

## TLS и reverse proxy

Встроенный TLS Web UI поддерживает `off`, `reverse-proxy`, `manual`, `acme-domain` и `acme-ip`; подробности есть в [конфигурации](configuration.md#tls-для-web-ui). Режим `manual` требует TLS 1.3 и отклоняет невалидные certificates, несовпадающие keys, истёкшие certificates, symlink для key, private key без прав `0600` и каталог key без прав `0700`. ACME-режимы используют только HTTP-01 на TCP/80, принимают одно точное публичное DNS-имя или публичный IP, хранят account и certificate cache в `CONFIG_DIR/tls/acme` с правами `0700` и требуют secure cookies при выключенных trusted proxy headers. IP-сертификаты используют короткоживущий профиль Let's Encrypt.

Для reverse-proxy обязательны пароль Web UI, `WEBUI_TRUST_PROXY_HEADERS=true` и прямые адреса proxy в `WEBUI_TRUSTED_PROXY_CIDRS`. Untrusted forwarded headers игнорируются. TLS termination и bind Web UI независимы: proxy может публиковать HTTPS на любом порту, пока awg-forge остаётся на loopback.

## Sessions

UI sessions истекают через 30 минут.

В режиме контроллера для входа нужны пароль администратора и текущий код TOTP
или одноразовый код восстановления. Opaque-сессии контроллера хранятся на
сервере в SQLite. Первая сессия выдаётся при активации; код подтверждения TOTP
повторно использовать нельзя. Повторная аутентификация меняет cookie сессии и
открывает пятиминутный интервал recent-auth для замены кодов восстановления.
Выход отзывает серверную сессию, а восстановление админа под root отзывает все
сессии. При активации прежние standalone cookie и `PASSWORD` перестают работать
без перезапуска. Для запуска контроллера нужны существующие SQLite и
`controller-auth.keys`; при их отсутствии вход не открывается.

`SESSION_SECRET` можно не задавать вручную. Если он отсутствует, awg-forge создаст и сохранит его в `state.json`.

По умолчанию `SESSION_COOKIE_SECURE=auto`: cookie без `Secure` разрешается только для loopback HTTP (`127.0.0.1`, `localhost`, `::1`), а для внешних host используется `Secure`. Для обычного HTTP на внешнем host можно явно указать `SESSION_COOKIE_SECURE=false`, но doctor покажет предупреждение. Такой режим стоит использовать только в доверенной сети или за отдельной защитой.

## Origin / Referer Checks

State-changing requests проверяют Origin/Referer.

POST без Origin/Referer разрешен только для loopback Host (`127.0.0.1`, `localhost`, `::1`). Это сохраняет localhost/SSH tunnel workflow и не открывает такой же сценарий для публичного Host.

Opaque origins вроде `null` и browser-extension origins отклоняются для mutating requests.

## Secrets

Нельзя логировать:

- private keys;
- preshared keys;
- passwords;
- секреты TOTP и коды восстановления;
- session secrets;
- backup passwords;
- полные client configs;
- import keys, `vpn://` links, QR payloads или packed AmneziaVPN QR strings.

## File Permissions

Config directory и generated config files должны иметь ограниченные права.

Doctor проверяет права config directory и предупреждает о проблемах.

## Runtime Apply Rollback

Если mutating operation меняет state/configs, но runtime apply падает, awg-forge откатывает state и rendered configs.

Это защищает от ситуации, когда UI показывает созданного клиента или измененный туннель, хотя runtime-состояние не было успешно применено.

Control TLS контроллера автоматически продлевает серверный сертификат под прежним CA после двух третей срока действия. Ранее включённый контроллер может продлить целый истёкший серверный сертификат до открытия loopback listener только при действующем CA, целой identity, доступных registry/auth и завершённом recovery. Отсутствующие или повреждённые данные и истёкший CA требуют локального восстановления. Отключённый или восстановленный из backup контроллер остаётся отключённым; отзывы node-сертификатов и конфигурация туннелей сохраняются. Это не включает удалённый доступ.

## Локальное восстановление node

Если controller недоступен или credentials существующего managed node истекли
либо отозваны, остановите его процесс `serve` и запускайте CLI от Linux root с тем
же `CONFIG_DIR`. Обе команды требуют текущие `managed_node.node_id` и
`managed_node.controller_id` из локального `state.json`. Просматривайте только эти
поля: полный state содержит секреты. Работающий сервер, неверное подтверждение,
небезопасные права каталогов/файлов, pending restore и пересекающиеся переходы
identity/desired state блокируют восстановление.

```bash
awg-forge node detach --confirm-node-id <current-node-uuid> --confirm-controller-id <current-controller-uuid>
awg-forge node rebind --confirm-node-id <current-node-uuid> --confirm-controller-id <current-controller-uuid> --input-file /protected/invitation.json --name node --timeout 10m
```

`detach` удаляет привязку и replay metadata и возвращает standalone mode.
После detach используйте обычный `node enroll` для нового enrollment. `rebind`
выполняет новое enrollment напрямую, сохраняя прежнюю локальную привязку до
успешных pinned TLS handshake, сверки comparison code и approval администратора
controller. Invitation должен быть обычным файлом владельца root с правами
`0600`, без symlink/hardlink; его секрет нельзя передавать в аргументах команды.
Прежние ограничения loopback transport сохраняются.

Rebind явно сбрасывает локальную node identity: устанавливает новый node ID,
state epoch и credentials, создаёт новое пространство boot/desired generation
и receipts. Туннели, клиенты, локальная аутентификация, operational history и
`ConfigRevision` сохраняются. Прежняя запись controller registry остаётся
отозванной либо исторической: rebind не возвращает ей authority. Локальное
удаление не отзывает скопированный сертификат на прежнем controller; при
необходимости отдельно отзовите там прежний node. Cleanup удаляет только прежнее
активное поколение credentials и записанный корректный pending renewal;
посторонние исторические каталоги не удаляются.

До state commit отказ, timeout или отмена сохраняют прежнюю привязку. Приватный
journal `.node-local-recovery.json` разрешает прерванные staging/cleanup через
проверку точного старого/нового полного state и никогда не откатывает state.
После успеха перезапустите `serve`. После сообщения о неопределённом commit
перезапуск позволяет однозначно сверить journal. Повреждённые evidence,
изменённый state и небезопасные пути требуют offline inspection и остаются
заблокированными. Не удаляйте evidence для обхода проверки. Backup/restore
отклоняют pending recovery; старый node archive не может заменить identity,
созданную новым rebind.
