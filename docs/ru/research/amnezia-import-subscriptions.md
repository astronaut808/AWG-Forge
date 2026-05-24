# Исследование импорта и подписок AmneziaVPN

Статус: research note, не план реализации.

Дата: 2026-05-22.

## Цель

Этот документ отвечает на продуктовый вопрос:

Может ли awg-forge сделать подписку или автообновляемый импорт для AmneziaVPN, чтобы пользователи получали новый endpoint/config без ручной загрузки свежего `.conf`?

Короткий вывод: пока нет, не как безопасную production-фичу.

awg-forge должен оставить скачивание и импорт `.conf` как поддерживаемый путь. Будущий `vpn://` или native Amnezia import можно исследовать за experimental flag, но только после проверки на реальных desktop, iOS и Android клиентах AmneziaVPN.

## Текущее состояние awg-forge

Сейчас awg-forge поддерживает:

- рендер `.conf` для AmneziaWG Legacy / 1.0, 1.5-oriented и 2.0;
- скачивание client config как основной способ импорта;
- stale config detection после изменения настроек туннеля;
- per-tunnel `server_host` override;
- encrypted backup/restore и restore dry-run;
- support bundle без секретов.

QR import намеренно не показывается в UI, потому что plain `.conf` оказался надежнее на текущих клиентах AmneziaVPN.

## Источники

Использованные первичные источники:

- официальная документация Amnezia: [AmneziaWG docs](https://docs.amnezia.org/documentation/amnezia-wg/);
- официальная инструкция Amnezia по self-hosted 2.0: [Using AmneziaWG 2.0 on self-hosted servers](https://docs.amnezia.org/documentation/instructions/new-amneziawg-selfhosted/);
- официальные docs по импорту: [Supported Configuration Formats](https://docs.amnezia.org/documentation/supported-configuration-formats/), [Connecting via File with Connection Settings](https://docs.amnezia.org/documentation/instructions/connect-via-config/), [Connecting via Text Key](https://docs.amnezia.org/documentation/instructions/connect-via-text-key/) и [Connecting via QR Code](https://docs.amnezia.org/documentation/instructions/connect-via-qr-code/);
- код Amnezia client: [`importController.cpp`](https://github.com/amnezia-vpn/amnezia-client/blob/4787f3915b7bed3472c9c822551e95d3f9c7dfb6/client/core/controllers/selfhosted/importController.cpp);
- код Amnezia client: [`exportController.cpp`](https://github.com/amnezia-vpn/amnezia-client/blob/4787f3915b7bed3472c9c822551e95d3f9c7dfb6/client/core/controllers/selfhosted/exportController.cpp);
- код Amnezia client: [`subscriptionController.cpp`](https://github.com/amnezia-vpn/amnezia-client/blob/4787f3915b7bed3472c9c822551e95d3f9c7dfb6/client/core/controllers/api/subscriptionController.cpp);
- код Amnezia client: [`subscriptionUiController.cpp`](https://github.com/amnezia-vpn/amnezia-client/blob/4787f3915b7bed3472c9c822551e95d3f9c7dfb6/client/ui/controllers/api/subscriptionUiController.cpp).

## Что умеет импортировать AmneziaVPN

Официальные docs Amnezia перечисляют `.vpn`, `.ovpn`, `.conf` и отдельные `.json` форматы, а также описывают импорт `.conf`/`.json` файла как обычный способ создания подключения. Import controller в Amnezia client дополнительно принимает несколько форматов:

- native Amnezia JSON;
- `vpn://` строки с base64-url encoded и опционально compressed JSON;
- обычные WireGuard/AmneziaWG configs с секциями `[Interface]` и `[Peer]`;
- несколько не-AWG URI схем: VLESS, VMess, Trojan, Shadowsocks.

Для awg-forge важно, что `.conf` является полноценным и поддерживаемым способом импорта. Код клиента распознает WireGuard/AWG config по `[Interface]` и `[Peer]`, затем перекладывает параметры в внутренний Amnezia JSON server object.

Этот же код определяет версии AWG по полям:

- Legacy/AWG detection требует базовые junk/header fields;
- AWG 1.5 определяется, когда есть `I1-I5` и нет пары `S3/S4`;
- AWG 2.0 определяется, когда config содержит и `S3`, и `S4`.

Значит текущий `.conf` рендер awg-forge остается совместимым с импортом AmneziaVPN.

## Что такое `vpn://`

В self-hosted export code Amnezia client `vpn://` создается так:

1. внутренний Amnezia server JSON превращается в JSON bytes;
2. bytes сжимаются через Qt compression;
3. результат кодируется base64-url без trailing `=`;
4. добавляется prefix `vpn://`.

При импорте Amnezia делает обратное: убирает `vpn://`, декодирует base64-url, пробует decompression, парсит JSON и классифицирует как Amnezia config.

Это не универсальный WireGuard subscription format. Это формат payload именно приложения AmneziaVPN.

## Что такое subscription в AmneziaVPN

Subscription code path в AmneziaVPN выглядит привязанным к Amnezia API/Gateway, а не к произвольной ссылке на конфиг.

Код subscription controller:

- генерирует client-side protocol data, включая AWG/WireGuard key pair для текущего клиента;
- отправляет API payloads на gateway endpoints вроде `v1/config`, `v1/subscriptions`, `v1/native_config` и revoke endpoints;
- хранит `api_config`, `auth_data`, service type, service protocol, country metadata и VPN key;
- умеет обновлять API-backed configs через `updateServiceFromGateway`;
- умеет export native configs через gateway path.

Для awg-forge это означает: в публичном коде клиента нет подтверждения, что произвольный self-hosted URL можно добавить как auto-refresh subscription с поведением Amnezia Gateway.

## Разница между `.conf`, `vpn://` и subscription

| Путь | Что это | Production status для awg-forge |
| --- | --- | --- |
| `.conf` download | Обычный WireGuard/AmneziaWG INI config | Поддерживается и рекомендуется |
| QR с `.conf` текстом | QR-encoded config text | Не показываем; был нестабилен на iOS |
| `vpn://...` | Native Amnezia compressed JSON import payload | Только research; нужна проверка схемы и платформ |
| Subscription/update | API-backed refresh/reissue flow в AmneziaVPN | Пока не подходит awg-forge без совместимого API contract |

## Продуктовый анализ

Желаемый UX понятен:

- админ меняет endpoint или параметры туннеля;
- пользователю не надо вручную получать новый файл;
- клиент обновляется безопасно.

Но риск высокий, если приблизительно повторить undocumented payload:

- native Amnezia JSON schema — внутреннее поведение приложения, а не стабильный публичный API;
- update behavior выглядит связанным с Amnezia Gateway fields и API endpoints;
- awg-forge придется отдавать private client configs через сетевой endpoint;
- revocation, expiration, audit, rate limiting и token rotation становятся security-critical;
- разные платформы AmneziaVPN могут парсить и обновлять config по-разному;
- роутеры и не-Amnezia клиенты все равно останутся на `.conf`.

## Требования безопасности, если делать позже

Любой будущий import/subscription endpoint надо проектировать как выдачу секретов, а не как удобную ссылку.

Минимум:

- per-client random token минимум 128 bits entropy;
- хранить hash token в state, не plaintext;
- опциональный expiration и manual rotation;
- немедленная revocation при удалении клиента;
- никаких общих tunnel-wide subscription URL;
- TLS настоятельно рекомендуется для публичного доступа;
- не писать secrets в access logs, errors, support bundle или doctor output;
- rate limiting для endpoint выдачи конфигов;
- без directory listing и predictable URL paths;
- response headers против caching shared proxies;
- явное UI предупреждение, что ссылка дает доступ к client private key;
- явная совместимость: только AmneziaVPN, не routers, не generic WireGuard clients.

## Архитектура для будущего эксперимента

Безопасный experimental design:

1. Оставить `.conf` canonical rendered artifact.
2. Добавить клиенту `import_token_hash`, `import_token_created_at`, `import_token_last_used_at`, `import_token_revoked_at`.
3. Добавить public read-only endpoint вроде `/s/<token>`, который возвращает:
   - plain `.conf` с `text/plain`, или
   - Amnezia `vpn://` payload только при включенном feature flag.
4. Добавить UI actions:
   - “Generate import link”;
   - “Rotate import link”;
   - “Revoke import link”;
   - “Download .conf”.
5. Логировать только hash prefix token и client ID, никогда config.
6. Добавить tests, которые декодируют `vpn://` payload и сравнивают JSON shape.
7. Добавить real-device acceptance tests для desktop, iOS и Android AmneziaVPN.

## Открытые вопросы

Перед реализацией надо ответить:

- Поддерживает ли текущий AmneziaVPN self-hosted non-Gateway subscription URL?
- Может ли `vpn://` payload обновить существующий профиль, или всегда создает новый?
- Делает ли AmneziaVPN polling/update self-hosted `vpn://` payload без действия пользователя?
- Какая точная native JSON schema нужна для AWG 2.0 на всех платформах?
- Отличается ли iOS поведение для `vpn://` link из Safari/Mail/Notes от QR или file import?
- Можно ли применить только endpoint change без rotation keys?
- Как AmneziaVPN определяет duplicate configs: CRC, host, public key, stored VPN key или другое поле?

## Рекомендация

Не внедрять subscription или `vpn://` import как production feature в v0.6.x/v0.7.x.

Рекомендуемый roadmap:

1. Оставить `.conf` download/import единственным стабильным production flow.
2. Улучшить stale config UX: явно показывать, каким клиентам нужен новый `.conf`.
3. Добавить безопасный “reissue config” flow, который скачивает fresh `.conf` после endpoint/protocol changes.
4. Позже открыть отдельную experimental branch для Amnezia native import research.
5. Переводить native import/subscription в stable только после source-level compatibility и real-device tests.

## Решение для awg-forge

Текущее решение:

- `.conf` остается canonical;
- `vpn://` и subscription support не входят в stable product;
- awg-forge может показывать authenticated experimental `Import key` action для проверки в AmneziaVPN / DefaultVPN. Этот key не является subscription endpoint и не должен считаться поддержкой роутеров или production-клиентов;
- будущий Amnezia-native import должен быть explicit experimental и документироваться как AmneziaVPN-specific.

Так awg-forge остается предсказуемым для routers, desktop clients, iOS, Android и ручной production эксплуатации.
