# Матрица протоколов

awg-forge — запускатор и менеджер существующих реализаций AmneziaWG. Он не реализует VPN-протокол сам: Go-код рендерит конфиги и запускает upstream-инструменты `awg`, `awg-quick` и `amneziawg-go`.

## Реализовано

| Профиль | Статус | Описание |
| --- | --- | --- |
| `awg_legacy_1_0` | Реализован | Рендерит Legacy / 1.0 поля `Jc`, `Jmin`, `Jmax`, `S1`, `S2`, `H1-H4`. Defaults генерируются для обфускации, а не для WireGuard fallback. |
| `awg_1_5` | Реализован | Добавляет `I1-I5` signature/masking packets в клиентские конфиги. Defaults включают DNS-like `I1` и небольшую CPS-цепочку для `I2-I5`. |
| `awg_2_0` | Реализован | Использует `I1-I5`, добавляет `S3/S4`, поддерживает ranges для `H1-H4`, валидирует непересечение ranges и рендерит fresh configs. Defaults используют генерируемый QUIC Initial-like `I1`. `.conf` импорт проверен на desktop и iOS с совместимыми AmneziaVPN builds. |
| `awg_3` | Экспериментальный | Один профиль семейства AWG 3.x входит в стандартный образ и помечен как экспериментальный в Web UI. Использует закрепленные userspace-исходники `amneziawg-go` 3.1.20260828 и `amneziawg-tools` 3.1.20260812 и рендерит `HeaderProtectionKey`, диапазоны AWG3, `RandomTrailers` и `DisableCookies`. Экспорт поддерживает `.conf`, QR с исходным конфигом для AmneziaWG, структурированный QR для AmneziaVPN и `vpn://`; kernel runtime и использование в production намеренно не поддерживаются. |

## Границы экспериментального AWG 3.x

Единый экспериментальный профиль AWG 3.x построен по текущему self-hosted генератору AmneziaVPN и закрепленным ревизиям исходников `amneziawg-go` 3.1.20260828 / `amneziawg-tools` 3.1.20260812; точные commit SHA записаны в `build/amneziawg.refs`. Он использует:

- `Jc`, `Jmin`, `Jmax`, `S1-S4`, `H1-H4`, `I1-I5`;
- `HeaderProtectionKey`, который генерируется один раз для туннеля и не попадает в публичное состояние и диагностику; аутентифицированный экспорт клиента включает ключ, потому что он необходим клиенту;
- `ContentPaddingAddition`, `RekeyAfterTime`, `RekeyTimeout`, `RejectAfterTime`, `KeepaliveTimeout`, `MaxHandshakeAttempts` и `PersistentKeepalive` как одиночные значения либо возрастающие диапазоны `uint16`.
- `RandomTrailers` и `DisableCookies` как state-значения upstream `on`/`off`; оба по умолчанию имеют значение `off`, server runtime config рендерит состояние явно, а client export не содержит выключенные опции.

Числовые значения и диапазоны по умолчанию повторяют upstream-генератор клиента. `I1` переиспользует механизм генерируемого QUIC Initial-like CPS из профиля 2.0. Header Protection по умолчанию использует значения совместимости `H1-H4=1/2/3/4`, которые отключают пользовательские заголовки сообщений, и требует, чтобы каждое значение `S1-S4` было не меньше `12`. Чувствительные к безопасности переключатели `RandomTrailers` и `DisableCookies` по умолчанию выключены. При включении `RandomTrailers` upstream рекомендует одинаковые значения `S1-S4`, чтобы снизить риск ошибочной классификации пакетов. AWG3 parsing закрепленных tools принимает расширенные ranges как `uint16`; awg-forge отклоняет значения выше `65535`, а не допускает их последующее усечение upstream.

AWG3 принудительно запускается через закрепленный userspace runtime. Весь профиль экспериментальный, его включение и использование выполняются оператором на свой риск. Оставляй `RandomTrailers` выключенным, пока upstream не исправит проблемы классификации transport-пакетов и первого пакета. `DisableCookies=on` прекращает отправку Cookie Reply; в закрепленном runtime он обходит принимающую under-load ветку cookie challenge, проверки MAC2 и rate limit, при этом обработка входящих Cookie Reply сохраняется. Это существенно ослабляет защиту от handshake-нагрузки, поэтому вне контролируемого теста параметр должен оставаться выключенным. Серверный конфиг сохраняет явные значения `off`, потому что обновление runtime использует `syncconf`; клиентские экспорты не содержат выключенные опции и соответствуют каноническому экспорту AmneziaVPN 5.0.1.5. Для тестов совместимости используй AmneziaVPN 5.0.1.5+ и не используй 5.0.0.5 для AWG 3.1. QR для AmneziaWG и `vpn://` содержат тот же отрендеренный `.conf`, что и скачиваемый файл. QR для AmneziaVPN использует типизированный `last_config` с версией протокола `3.1`, Header Protection, временными полями, MTU и диапазоном keepalive AWG 3.x. Если QR или импорт `vpn://` не работает на конкретной платформе, резервным способом остается `.conf`.

Явно заданный MTU туннеля сохраняется во всех экспортах. Новый туннель AWG 3.x по умолчанию получает `1280`, если MTU не задан; явные значения и существующее состояние туннелей сохраняются. Если handshake проходит, но крупные пакеты зависают, смотри [MTU по умолчанию и ограничения импорта клиента](configuration.md#mtu).

Ручная end-to-end проверка стандартного образа подтвердила импорт `.conf`, handshake, передачу трафика, восстановление после restart, WAN egress и WARP egress с совместимыми актуальными клиентами AmneziaVPN, AmneziaWG и DefaultVPN. Это ограниченный acceptance result, а не гарантия для каждой платформы, сборки клиента, сети или будущего 3.x runtime.

Сериализаторы QR для AmneziaVPN и `vpn://` для AWG 3.x покрыты автоматическими проверками схемы и полного цикла кодирования-декодирования по именам полей и логике импорта AmneziaVPN 5.0.1.5. Сканирование QR и обработку `vpn://` всё ещё нужно проверить отдельно на реальных платформах, прежде чем считать эти пути равнозначными проверенному `.conf`.

## AWG 2.0

Используй сборку AmneziaVPN с поддержкой AmneziaWG 2.0. Переход с 1.0/Legacy на 2.0 не является in-place upgrade: нужны новые guest configs/keys.

Ключевые отличия 2.0 от 1.5:

- добавляет `S3` и `S4`;
- добавляет ranges для `H1-H4`;
- ranges `H1-H4` не должны пересекаться;
- убирает старые `j1-j3` и `itime`;
- сохраняет `I1-I5`, появившиеся в 1.5.

## Диапазоны параметров

| Параметр | Диапазон / синтаксис | Примечание |
| --- | --- | --- |
| `I1-I5` | CPS signature strings | Последовательность тегов `<b 0x...>`, `<r N>`, `<rd N>`, `<rc N>`, `<t>`. |
| `S1-S3` | `0..64` | Fixed random prefix sizes. |
| `S4` | `0..32` | Fixed random prefix size для transport data packets. |
| `Jc` | `0..10` | awg-forge держится внутри official docs range. |
| `Jmin/Jmax` | `64..1024`, `Jmin <= Jmax` | Желательно держать `Jmax` ниже effective MTU. |
| `H1-H4` | `uint32` или range `x-y` | В 2.0 ranges не должны пересекаться. |

## Правила рендера

| Поле | Legacy / 1.0 | AWG 1.5 | AWG 2.0 | AWG 3.x |
| --- | --- | --- | --- | --- |
| `Jc/Jmin/Jmax` | server и client interface | server и client interface | server и client interface | server и client interface |
| `S1/S2` | server и client interface | server и client interface | server и client interface | server и client interface |
| `S3/S4` | не рендерится | не рендерится | server и client interface | server и client interface |
| `H1-H4` | single values | single values | ranges by default | server и client interface |
| `I1-I5` | не рендерится | только client interface | server и client interface | client interface; в server config сохраняются только комментарии, а `I1` создаётся общим QUIC Initial-like генератором |
| `HeaderProtectionKey` | не рендерится | не рендерится | не рендерится | server и client interface; генерируется один раз для туннеля |
| AWG 3.x timing ranges | не рендерятся | не рендерятся | не рендерятся | server и client interface |
| `RandomTrailers/DisableCookies` | не рендерятся | не рендерятся | не рендерятся | явное состояние в server runtime config; в client export только включённые значения |
| MTU, когда значение не задано | не добавляется (`Авто`) | не добавляется (`Авто`) | не добавляется (`Авто`) | новые туннели получают `1280`; существующее состояние `0` сохраняется |
| `protocol_version` | не INI field | не INI field | только metadata для AmneziaVPN JSON import | не INI field |

## Defaults

Legacy / 1.0 и 1.5:

- `Jc`: random `4..10`;
- `Jmin`: random `64..256`;
- `Jmax`: random `768..1024`, всегда больше `Jmin`;
- `S1/S2`: random `15..64`;
- `H1-H4`: crypto-random unique non-zero single values, без modulo reduction.

AWG 2.0:

- `Jc`: random `4..10`;
- `Jmin`: random `64..256`;
- `Jmax`: random `768..1024`;
- `S1-S3`: random `15..64`;
- `S4`: random `8..32`;
- `H1-H4`: crypto-random non-overlapping ranges шириной `30000..65535`;
- `I1`: генерируется для каждого туннеля как `1200..1232` byte QUIC Initial-like CPS packet: randomized protected first byte, QUIC v1 marker, один из нескольких destination/source connection ID profiles, корректный QUIC varint length и runtime-random protected payload, разбитый на parser-safe randomized `<r ...>` chunks не больше `999` bytes каждый;
- `I2-I5`: небольшая CPS-цепочка, аналогичная текущему 1.5 профилю.

Экспериментальный AWG 3.x:

- `Jc`: random `4..6`; `Jmin`: `10`; `Jmax`: `50`;
- `S1/S2`: разные random-значения `12..149`; `S3`: random `12..63`; `S4`: `12`;
- `H1-H4`: разные значения `1`, `2`, `3`, `4`, защищённые отдельным `HeaderProtectionKey` туннеля;
- `I1`: тот же генерируемый `1200..1232` byte QUIC Initial-like CPS packet, что и в AWG 2.0; `I2-I5` пустые;
- ranges padding и timing: `10-100`, `100-120`, `3-7`, `150-180`, `5-15`, `15-20`; `PersistentKeepalive`: `25-35`;
- `RandomTrailers` и `DisableCookies`: `off`.

Zero-valued obfuscation params считаются слабыми defaults, потому что all-zero behavior двигает поведение в сторону обычного WireGuard.

AWG 2.0 по умолчанию использует рандомизированную QUIC Initial-like сигнатуру `I1`. Моделируется только форма UDP payload: Ethernet/IP/UDP headers из packet capture в конфиг не попадают. Сигнатура нужна для AmneziaWG CPS-маскировки, а не для установки настоящей QUIC-сессии. Размер рандомизируется в диапазоне `1200..1232` bytes, а крупные random-блоки разбиваются на randomized CPS `<r ...>` части ниже границы парсера.

## Статус проверки AWG 2.0

Проверено:

- `.conf` импортируется и подключается на desktop client;
- `.conf` импортируется и подключается на iOS после обновления до совместимого AmneziaVPN build;
- AmneziaVPN-compatible QR export реализован как structured JSON с `last_config`, zlib/qCompress-style wrapper, base64url payload и compatibility-critical JSON field types;
- Docker/server-side `awg show` показывает 2.0 params, handshake и traffic для `awg20`.

Требует более широкой проверки:

- QR import behavior на AmneziaVPN iOS, Android и Desktop builds;
- отличия native import schema между платформами AmneziaVPN.

## Источники

- [AmneziaWG docs](https://docs.amnezia.org/documentation/amnezia-wg/)
- [Using AmneziaWG 2.0 on self-hosted servers](https://docs.amnezia.org/documentation/instructions/new-amneziawg-selfhosted/)
- [amnezia-vpn/amneziawg-go README](https://github.com/amnezia-vpn/amneziawg-go)
- [amneziawg-go v3.1.20260828](https://github.com/amnezia-vpn/amneziawg-go/releases/tag/v3.1.20260828)
- [Исправление парсера amneziawg-apple v3.1.3](https://github.com/amnezia-vpn/amneziawg-apple/pull/43)
- [Стабильный релиз AmneziaVPN 5.0.1.5](https://github.com/amnezia-vpn/amnezia-client/releases/tag/5.0.1.5)
- [Проблема cross-version совместимости AWG 3.1 в AmneziaVPN](https://github.com/amnezia-vpn/amnezia-client/issues/3016)
- [Исправление классификации transport-пакетов при RandomTrailers](https://github.com/amnezia-vpn/amneziawg-go/pull/183)
- [Исправление гонки первого пакета S4](https://github.com/amnezia-vpn/amneziawg-go/pull/184)
- [Проблема MTU при RandomTrailers и ContentPaddingAddition](https://github.com/amnezia-vpn/amneziawg-go/issues/185)
- [Исправление импорта MTU в AmneziaVPN](https://github.com/amnezia-vpn/amnezia-client/pull/3065)
- [Сохранение явно импортированного MTU в AmneziaVPN](https://github.com/amnezia-vpn/amnezia-client/pull/3113)
- [Отчет о несовпадении server/client MTU в AWG 3.1](https://github.com/amnezia-vpn/amnezia-client/issues/3089)
- [Логика выбора MTU в закрепленном `awg-quick`](https://github.com/amnezia-vpn/amneziawg-tools/blob/ee0f0a9aa34ff0a0da4b3433b9512781cfe02843/src/wg-quick/linux.bash)
- [MTU defaults клиента Amnezia в development-ветке](https://github.com/amnezia-vpn/amnezia-client/blob/dev/client/core/utils/constants/protocolConstants.h)
- [RFC 8200, минимальный MTU канала IPv6](https://www.rfc-editor.org/rfc/rfc8200.html#section-5)
- [RFC 9000, QUIC: A UDP-Based Multiplexed and Secure Transport](https://www.rfc-editor.org/rfc/rfc9000)
