# AWG 2.0 design

Этот документ фиксирует design для `awg_2_0`. Профиль 2.0 живет отдельно от Legacy / 1.0 и 1.5, чтобы не ломать рабочие туннели.

## Product decision

`awg_2_0` — отдельный tunnel/server profile.

Не конвертировать Legacy / 1.0 или 1.5 tunnels in-place. Для 2.0 нужны новые configs/keys и совместимые версии клиентов.

## Compatibility

Минимальный клиент:

- AmneziaVPN `4.8.12.9` или новее для официальной поддержки 2.0.

Текущее поведение продукта:

- вкладка `1.0` остается для Legacy tunnels;
- вкладка `1.5` остается для 1.5-oriented tunnels;
- вкладка `2.0` включена;
- `.conf` импорт проверен на desktop и iOS;
- native Amnezia import payloads не показываются в продукте.

## Required params

```text
Jc
Jmin
Jmax
S1
S2
S3
S4
H1
H2
H3
H4
I1
I2
I3
I4
I5
```

## Validation

Numeric:

- `Jc`: `0..10`;
- `Jmin`: `64..1024`;
- `Jmax`: `64..1024`;
- `Jmin <= Jmax`;
- `S1`: `0..64`;
- `S2`: `0..64`;
- `S3`: `0..64`;
- `S4`: `0..32`.

Headers:

- single unsigned 32-bit integer: `1234`;
- или range: `1234-5678`;
- start <= end;
- values inside `0..4294967295`;
- `H1-H4` ranges не должны пересекаться.

CPS fields:

- `<b 0xHEX>`;
- `<r N>`;
- `<rd N>`;
- `<rc N>`;
- `<t>`.

Malformed tags rejected.

MTU:

- protocol code не придумывает MTU;
- используется ровно tunnel MTU;
- если tunnel MTU explicit, UI может предупреждать при `Jmax >= MTU`.

## Defaults

- `Jc`: random `4..10`;
- `Jmin`: random `64..256`;
- `Jmax`: random `768..1024`;
- `S1`: random `15..64`;
- `S2`: random `15..64`;
- `S3`: random `15..64`;
- `S4`: random `8..32`;
- `H1-H4`: four non-overlapping ranges;
- `I1-I5`: current verified CPS chain.

## Rendering

Server interface включает:

```ini
[Interface]
PrivateKey = <server-private-key>
Address = <server-address>/<prefix>
ListenPort = <port>
MTU = <optional tunnel MTU>
Jc = <value>
Jmin = <value>
Jmax = <value>
S1 = <value>
S2 = <value>
S3 = <value>
S4 = <value>
H1 = <range-or-value>
H2 = <range-or-value>
H3 = <range-or-value>
H4 = <range-or-value>
I1 = <optional CPS>
I2 = <optional CPS>
I3 = <optional CPS>
I4 = <optional CPS>
I5 = <optional CPS>
```

Client interface включает те же protocol params и tunnel MTU, если MTU задан явно.

Peer sections остаются обычными AWG/WireGuard peer sections.

## Native import

Поддерживаемый путь сейчас — `.conf`.

Future native import возможен только после проверки schema на реальных iOS, Android и desktop clients.

## Tests

Покрытие:

- golden server config для 2.0;
- golden client config для 2.0;
- `H1-H4` overlap rejection;
- single-value/range parsing;
- invalid range rejection;
- `S4 > 32` rejection;
- missing `S3/S4` rejection;
- CPS syntax tests.

## Validation status

Проверено:

- 2.0 server/client configs рендерятся;
- Docker/server-side `awg show` показывает `s3/s4`, ranged `h1-h4`, `i1-i5`, handshake и traffic;
- `.conf` import работает на desktop;
- `.conf` import работает на iOS после обновления до compatible AmneziaVPN build.

Still pending:

- native import validation;
- wider compatibility matrix для iOS, Android, desktop и client versions.
