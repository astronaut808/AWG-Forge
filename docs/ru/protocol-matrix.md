# Матрица протоколов

awg-forge — запускатор и менеджер существующих реализаций AmneziaWG. Он не реализует VPN-протокол сам: Go-код рендерит конфиги и запускает upstream-инструменты `awg`, `awg-quick` и `amneziawg-go`.

## Реализовано

| Профиль | Статус | Описание |
| --- | --- | --- |
| `awg_legacy_1_0` | Реализован | Рендерит Legacy / 1.0 поля `Jc`, `Jmin`, `Jmax`, `S1`, `S2`, `H1-H4`. Defaults генерируются для обфускации, а не для WireGuard fallback. |
| `awg_1_5` | Реализован | Добавляет `I1-I5` signature/masking packets в клиентские конфиги. Defaults включают DNS-like `I1` и небольшую CPS-цепочку для `I2-I5`. |
| `awg_2_0` | Реализован | Использует `I1-I5`, добавляет `S3/S4`, поддерживает ranges для `H1-H4`, валидирует непересечение ranges и рендерит fresh configs. Defaults используют генерируемый QUIC Initial-like `I1`. `.conf` импорт проверен на desktop и iOS с совместимыми AmneziaVPN builds. |
| `awg_3_0` | Экспериментальный | По умолчанию выключен. Добавляет upstream AWG 3 поля и генерируемый `HeaderProtectionKey`; ключ хранится отдельно от публичных параметров и не возвращается API или support bundle. Используй только закреплённый экспериментальный образ ниже. Для AmneziaVPN 5.0.0.5 используй отдельный QR AmneziaVPN: прямой импорт `.conf` теряет поля AWG 3. Raw `.conf` QR и `vpn://` экспорт остаются недоступны. |

## Запланировано

| Профиль | Статус | Описание |
| --- | --- | --- |
| `custom` | Запланирован | Зарезервирован под пользовательские protocol params после стабилизации validation rules. |

## Экспериментальный AWG 3.0

AWG 3.0 не является профилем по умолчанию и не входит в стандартный release image. Локально собери userspace image с явно закреплёнными версиями:

```bash
make docker-build-awg3
```

Укажи `AWG3_EXPERIMENTAL=true` в `.env`, затем используй в Compose image `awg-forge:awg3-experimental`. Этот образ выставляет внутренний признак совместимого runtime `AWG3_RUNTIME=true`; стандартный образ не сможет открыть AWG 3 только изменением `.env`. Сборка закрепляет `amneziawg-go` на стабильном релизе `v3.0.2` (`0527dfa47639714dd8f5c9ffbd9d40d19083f0ba`) и `amneziawg-tools` на `05434cab7d91bbbc607d18ec5fade91f4b83774c`; не меняй эти references без отдельной compatibility-проверки.

В этом эксперименте AWG 3 поддерживается только через userspace `amneziawg-go`. Для этого профиля AWG-Forge явно запускает `amneziawg-go`, даже если загружен Linux kernel module `amneziawg`. Закреплённый toolchain появился до нового Netlink API AWG 3, а совместимость с kernel module не проверена end-to-end. Раннее расхождение по диапазонам `PersistentKeepalive` связано с [upstream issue #196](https://github.com/amnezia-vpn/amneziawg-linux-kernel-module/issues/196), который закрыт без связанной проверки. Не включай kernel mode, пока отдельная закреплённая пара tools/kernel не пройдёт scalar и range `setconf`/`showconf` тесты для каждого поля AWG 3. При `AWG3_EXPERIMENTAL=true` экспериментальный image также запускает WARP через userspace, потому что его peer использует `PersistentKeepalive`; AWG 1.x и 2.0 сохраняют обычный выбор runtime. Стандартный image не меняет выбор runtime для WARP. Не удаляй `PersistentKeepalive` и не приводи его к числу как обход: AWG 3 поддерживает и диапазоны.

До использования проверь локально собранный образ:

```bash
docker image inspect awg-forge:awg3-experimental --format '{{ index .Config.Labels "org.awg-forge.awg3-runtime" }}'
```

Команда должна вывести `true`.

Этот локальный экспериментальный образ не входит в managed release-upgrade path. Пересобери его той же командой, затем пересоздай сервис через `docker compose up -d --force-recreate`; не используй для этого локального образа `install.sh upgrade`.

Профиль рендерит унаследованные поля AWG 2.0 и `HeaderProtectionKey`, `ContentPaddingAddition`, `RekeyAfterTime`, `RekeyTimeout`, `RejectAfterTime`, `KeepaliveTimeout`, `MaxHandshakeAttempts`. `HeaderProtectionKey` обязателен и генерируется как base64-секрет на 32 байта; для AWG 3 также требуется `S1-S4 >= 12`. Остальные поля AWG 3 — необязательные upstream overrides, но AWG-Forge явно записывает userspace-дефолты v3.0.2: `ContentPaddingAddition=0`, `RekeyAfterTime=120`, `RekeyTimeout=5`, `RejectAfterTime=180`, `KeepaliveTimeout=10`, `MaxHandshakeAttempts=18`. `PersistentKeepalive` остаётся client-side значением, настроенным для туннеля. Все эти поля принимают беззнаковое значение `uint32` или возрастающий диапазон `min-max`, где это поддерживает upstream.

AmneziaVPN 5.0.0.5 стабильно поддерживает AWG 3. Для этого профиля используй отдельный **QR AmneziaVPN** из AWG-Forge. Он зеркалирует поля AWG во внешних metadata `containers[0].awg` и native structured `last_config`; `last_config.config` в точности совпадает с рендеренным client `.conf`. Прямой импорт `.conf` в текущем клиенте распознаёт metadata AWG 2, но отбрасывает эти поля AWG 3: конфигурация может выглядеть похожей, но handshake не состоится.

Во внешнем JSON QR остаётся `protocol_version = "2"`: в текущем исходном коде AmneziaVPN нет отдельного native metadata-значения `"3"`, а поля AWG 3 задаются через structured `last_config`. Поэтому текущий UI клиента может показывать у native AWG 3-профиля `version 2`; эта метка не отражает runtime-поля. Подтверждай AWG 3 успешным handshake, а не меткой в UI. Не меняй это metadata значение без подтверждения у клиента. Raw `.conf` QR и `vpn://` export для AWG 3 остаются выключены.

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
