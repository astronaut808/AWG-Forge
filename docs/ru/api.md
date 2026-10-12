# API панели управления

[`api/openapi.json`](../../api/openapi.json) содержит OpenAPI 3.1-контракт стабильных запросов панели
управления. Он описывает аутентификацию, активацию контроллера, control lifecycle
и одобрение enrollment, состояние, операции с туннелями и клиентами, лимиты трафика
и WARP. Обычные config/QR downloads, backup/restore, диагностика и support bundle
остаются внутренними деталями Web UI вне контракта; отдельный backup route для
включения control listener документирован.

Это **не** публичный API удалённого управления. CORS не включён; API использует
HttpOnly cookie браузерной сессии: подписанную в standalone-режиме и opaque
с хранением в SQLite в controller-режиме.
Оставляйте панель за обычными механизмами защиты AWG-Forge. Не открывайте её
недоверенным origin и не автоматизируйте запросы скопированными cookie браузера.

## Безопасность изменений

Встроенный UI передаёт заголовок `Idempotency-Key` для изменений туннелей, клиентов,
лимитов трафика и WARP. Аутентификация, controller lifecycle и одобрение enrollment
используют собственные transaction/replay rules, а не этот response cache.
Ключ необязателен ради обратной
совместимости, но без него нет защиты от повторного применения операции. Он
ограничен 128 байтами и относится к конкретной операции. Повтор того же тела
запроса в течение десяти минут возвращает исходный JSON-результат.
Повторное использование ключа с другим телом даёт `409` и код
`idempotency_key_reused`, а не применяет изменение второй раз.

Каждый ответ содержит `X-Request-ID`. По нему можно сопоставить неудачное
действие в UI с runtime-логами или support bundle. В ошибках сохранено понятное
поле `error` и добавлено стабильное машинное поле `code`; внутренние команды,
пути и секреты в ответ не попадают.

## Развитие контракта

Первый контракт является границей совместимости для встроенного UI, а не
обещанием стабильного стороннего API. Для внешней multi-node автоматизации
появится отдельно версионируемый `/api/v1`: с ограниченными API-токенами,
доступом только по TLS, пагинацией и специально спроектированной моделью
аутентификации. Cookie браузерной сессии не будет использоваться как внешний
учётный ключ.

## Доставка обновления снимка

`POST /api/controller/nodes/{node_id}/refresh` требует действующую сессию
администратора и существующую same-origin policy. Строгое JSON-тело содержит UUID
`operation_id`, UUID `idempotency_key`, `expected_state_epoch`,
`expected_binding_epoch`, `expected_desired_generation`. Оба ID передаются в теле;
маршрут использует durable replay в SQLite, отдельно от кеша локальных mutations.
Одинаковый intent возвращает ту же операцию, изменённый с повторным ID —
`409 operation_fenced`. Безопасный статус читается через
`GET /api/controller/nodes/{node_id}/operations/{operation_id}`. Ответы `no-store`
не содержат lease, session или certificate credentials.

Лимиты: строгое тело 4096 байт, один pending-запрос на узел, 64 сохранённых записи
на узел, 4096 всего, минимум пять секунд между новыми запросами узла и 64 создания
за минуту глобально. Превышение — `429 operation_limit`, неподдерживаемая capability —
`409 refresh_unsupported`, отсутствие текущей presence/projection — `409 node_unavailable`,
ошибка хранилища — `503 control_unavailable`. TTL — две минуты; retention завершается
через 24 часа после expiry. Terminal statuses: succeeded, conflict, failed, expired,
rejected.

Реализованы mTLS-маршруты `POST /control/v1/node/operations/poll` и
`PUT /control/v1/node/operations/{operation_id}/result`; см.
[внутренний control contract](../../api/control-v1.openapi.json) и
[дизайн доставки](../design/snapshot-refresh-delivery.md). Перед success контроллер
проверяет текущую identity, lease и связь с принятым снимком. Публичного operator
API этот протокол не добавляет.
