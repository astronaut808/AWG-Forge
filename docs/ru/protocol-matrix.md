# Матрица протоколов

awg-forge — запускатор и менеджер существующих реализаций AmneziaWG. Он не реализует VPN-протокол сам: Go-код рендерит конфиги и запускает upstream-инструменты `awg`, `awg-quick` и `amneziawg-go`.

## Реализовано

| Профиль | Статус | Описание |
| --- | --- | --- |
| `awg_legacy_1_0` | Реализован | Рендерит Legacy / 1.0 поля `Jc`, `Jmin`, `Jmax`, `S1`, `S2`, `H1-H4`. Defaults генерируются для обфускации, а не для WireGuard fallback. |
| `awg_1_5` | Реализован | Добавляет `I1-I5` signature/masking packets в клиентские конфиги. Defaults включают DNS-like `I1` и небольшую CPS-цепочку для `I2-I5`. |
| `awg_2_0` | Реализован | Использует `I1-I5`, добавляет `S3/S4`, поддерживает ranges для `H1-H4`, валидирует непересечение ranges и рендерит fresh configs. Defaults используют генерируемый QUIC Initial-like `I1`. `.conf` импорт проверен на desktop и iOS с совместимыми AmneziaVPN builds. |

## Запланировано

| Профиль | Статус | Описание |
| --- | --- | --- |
| `custom` | Запланирован | Зарезервирован под пользовательские protocol params после стабилизации validation rules. |

## AWG 2.0

По официальным материалам AmneziaWG 2.0 требует AmneziaVPN `4.8.12.9` или новее. Переход с 1.0/Legacy на 2.0 не является in-place upgrade: нужны новые guest configs/keys.

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

| Поле | Legacy / 1.0 | AWG 1.5 | AWG 2.0 |
| --- | --- | --- | --- |
| `Jc/Jmin/Jmax` | server и client interface | server и client interface | server и client interface |
| `S1/S2` | server и client interface | server и client interface | server и client interface |
| `S3/S4` | не рендерится | не рендерится | server и client interface |
| `H1-H4` | single values | single values | ranges by default |
| `I1-I5` | не рендерится | client interface only | server и client interface |
| `protocol_version` | не INI field | не INI field | только metadata для AmneziaVPN JSON import |

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
- [amnezia-client `protocols_defs.h`](https://raw.githubusercontent.com/amnezia-vpn/amnezia-client/dev/client/protocols/protocols_defs.h)
- [amnezia-client `importController.cpp`](https://raw.githubusercontent.com/amnezia-vpn/amnezia-client/dev/client/ui/controllers/importController.cpp)
- [RFC 9000, QUIC: A UDP-Based Multiplexed and Secure Transport](https://www.rfc-editor.org/rfc/rfc9000)
