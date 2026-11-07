# Безопасность

## Web UI Bind

По умолчанию Web UI слушает:

```env
WEBUI_HOST=127.0.0.1
WEBUI_PORT=51821
```

Для доступа к панели оставь адрес `127.0.0.1` и подключайся через SSH-туннель.

```bash
ssh -L 51821:127.0.0.1:51821 user@server
```

Перед открытием доступа к панели задай надёжный `PASSWORD`. Пустой пароль допустим только при привязке к явному loopback IP, например `127.0.0.1` или `::1`, с выключенными trusted proxy headers. Любой другой адрес привязки, включая имена хостов вроде `localhost`, требует пароль. Логин по умолчанию — `admin`. Чтобы его изменить, задай `WEBUI_USERNAME`.

## TLS и reverse proxy

Встроенный TLS Web UI поддерживает `off`, `reverse-proxy`, `manual`, `acme-domain` и `acme-ip`; подробности есть в [конфигурации](configuration.md#tls-для-web-ui). Режим `manual` требует TLS 1.3 и отклоняет невалидные certificates, несовпадающие keys, истёкшие certificates, symlink для key, private key без прав `0600` и каталог key без прав `0700`. ACME-режимы используют только HTTP-01 на TCP/80, принимают одно точное публичное DNS-имя или публичный IP, хранят account и certificate cache в `CONFIG_DIR/tls/acme` с правами `0700` и требуют secure cookies при выключенных trusted proxy headers. IP-сертификаты используют короткоживущий профиль Let's Encrypt.

Для reverse-proxy обязательны пароль Web UI, `WEBUI_TRUST_PROXY_HEADERS=true` и прямые адреса proxy в `WEBUI_TRUSTED_PROXY_CIDRS`. Untrusted forwarded headers игнорируются. TLS termination и bind Web UI независимы: proxy может публиковать HTTPS на любом порту, пока awg-forge остаётся на loopback.

## Sessions

Сессия панели действует 30 минут.

`SESSION_SECRET` можно не задавать вручную. Если он отсутствует, awg-forge создаст и сохранит его в `state.json`.

По умолчанию `SESSION_COOKIE_SECURE=auto`: cookie без `Secure` разрешается только для loopback HTTP (`127.0.0.1`, `localhost`, `::1`), а для внешних host используется `Secure`. Для обычного HTTP на внешнем host можно явно указать `SESSION_COOKIE_SECURE=false`, но doctor покажет предупреждение. Такой режим стоит использовать только в доверенной сети или за отдельной защитой.

## Origin / Referer Checks

Сервер проверяет заголовки `Origin` и `Referer` у запросов, которые меняют настройки.

Запрос без обоих заголовков разрешён при обращении напрямую к панели по локальному адресу (`127.0.0.1`, `localhost` или `::1`).

Источники вроде `null` и URL расширений браузера отклоняются.

## Secrets

Нельзя логировать:

- private keys;
- preshared keys;
- passwords;
- session secrets;
- backup passwords;
- полные client configs;
- import keys, `vpn://` links, QR payloads или packed AmneziaVPN QR strings.

## File Permissions

Ограничь доступ к каталогу конфигурации и файлам в нём. Doctor проверяет права доступа и предупреждает о проблемах.

## Runtime Apply Rollback

Если mutating operation меняет state/configs, но runtime apply падает, awg-forge откатывает state и rendered configs.

Это защищает от ситуации, когда UI показывает созданного клиента или измененный туннель, хотя runtime-состояние не было успешно применено.
