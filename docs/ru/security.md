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

## Sessions

UI sessions истекают через 30 минут.

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
- session secrets;
- полные client configs.

## File Permissions

Config directory и generated config files должны иметь ограниченные права.

Doctor проверяет права config directory и предупреждает о проблемах.

## Runtime Apply Rollback

Если mutating operation меняет state/configs, но runtime apply падает, awg-forge откатывает state и rendered configs.

Это защищает от ситуации, когда UI показывает созданного клиента или измененный туннель, хотя runtime-состояние не было успешно применено.
