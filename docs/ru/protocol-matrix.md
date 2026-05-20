# Матрица протоколов

awg-forge — запускатор и менеджер существующих реализаций AmneziaWG. Он не реализует VPN-протокол сам: Go-код рендерит конфиги и запускает upstream-инструменты `awg`, `awg-quick` и `amneziawg-go`.

## Реализовано

| Профиль | Статус | Описание |
| --- | --- | --- |
| `awg_legacy_1_0` | Реализован | Рендерит Legacy / 1.0 поля `Jc`, `Jmin`, `Jmax`, `S1`, `S2`, `H1-H4`. Defaults генерируются для обфускации, а не для WireGuard fallback. |
| `awg_1_5` | Реализован | Добавляет `I1-I5` signature/masking packets в клиентские конфиги. Defaults включают DNS-like `I1` и небольшую CPS-цепочку для `I2-I5`. |
| `awg_2_0` | Реализован | Использует `I1-I5`, добавляет `S3/S4`, поддерживает ranges для `H1-H4`, валидирует непересечение ranges и рендерит fresh configs. `.conf` импорт проверен на desktop и iOS с совместимыми AmneziaVPN builds. |

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
| `protocol_version` | не INI field | не INI field | только metadata для future native import |

## Defaults

Legacy / 1.0 и 1.5:

- `Jc`: random `4..10`;
- `Jmin`: random `64..256`;
- `Jmax`: random `768..1024`, всегда больше `Jmin`;
- `S1/S2`: random `15..64`;
- `H1-H4`: random unique non-zero single values.

AWG 2.0:

- `Jc`: random `4..10`;
- `Jmin`: random `64..256`;
- `Jmax`: random `768..1024`;
- `S1-S3`: random `15..64`;
- `S4`: random `8..32`;
- `H1-H4`: non-overlapping ranges;
- `I1-I5`: verified CPS defaults, аналогичные текущему 1.5 профилю.

Zero-valued obfuscation params считаются слабыми defaults, потому что all-zero behavior двигает поведение в сторону обычного WireGuard.

## Статус проверки AWG 2.0

Проверено:

- `.conf` импортируется и подключается на desktop client;
- `.conf` импортируется и подключается на iOS после обновления до совместимого AmneziaVPN build;
- Docker/server-side `awg show` показывает 2.0 params, handshake и traffic для `awg20`.

Не реализовано:

- native Amnezia import payloads;
- точная native import schema для всех платформ AmneziaVPN.

## Источники

- [AmneziaWG docs](https://docs.amnezia.org/documentation/amnezia-wg/)
- [Using AmneziaWG 2.0 on self-hosted servers](https://docs.amnezia.org/documentation/instructions/new-amneziawg-selfhosted/)
- [amnezia-vpn/amneziawg-go README](https://github.com/amnezia-vpn/amneziawg-go)
- [amnezia-client `protocols_defs.h`](https://raw.githubusercontent.com/amnezia-vpn/amnezia-client/dev/client/protocols/protocols_defs.h)
- [amnezia-client `importController.cpp`](https://raw.githubusercontent.com/amnezia-vpn/amnezia-client/dev/client/ui/controllers/importController.cpp)
