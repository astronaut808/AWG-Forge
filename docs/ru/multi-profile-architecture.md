# Multi-profile / Multi-tunnel архитектура

awg-forge проектировался как manager нескольких независимых AmneziaWG-серверов, а не как один туннель с переключаемым протоколом.

## Основное решение

Профили `1.0`, `1.5` и `2.0` должны жить параллельно.

Пример:

- клиент Legacy / 1.0 подключается к туннелю `awg0`;
- клиент 1.5 подключается к туннелю `awg15`;
- клиент 2.0 подключается к туннелю `awg20`.

Это важно, потому что разные версии AmneziaWG могут требовать разные конфиги, keys, protocol params и совместимые client versions.

## State model

State хранит массив `tunnels`.

Каждый tunnel содержит:

- `id`;
- `name`;
- `interface_name`;
- `enabled`;
- server private/public keys;
- listen port;
- server address;
- IPv4 subnet;
- DNS;
- allowed IPs;
- keepalive;
- MTU;
- protocol profile ID;
- protocol params;
- clients;
- config revision;
- last apply error.

Client всегда принадлежит ровно одному tunnel.

## Lifecycle клиента

Создание клиента:

1. Выбран tunnel/profile.
2. Введено имя клиента.
3. IP выделяется из subnet конкретного tunnel.
4. Генерируются client keys и preshared key.
5. Рендерится только нужный tunnel.
6. При `APPLY_CONFIG=true` применяется только нужный tunnel.
7. UI обновляется и предлагает `.conf` download.

Удаление клиента освобождает IP внутри того же tunnel.

Disabled client остается в state, но не попадает в server peers.

## ProtocolProfile

Каждый протокол реализуется как профиль:

- `ID`;
- `DisplayName`;
- `Version`;
- `GenerateDefaults`;
- `Validate`;
- render server interface;
- render server peer;
- render client interface;
- render client peer.

Так новые версии добавляются как новый profile, а не как набор if-else по всему коду.

## Firewall и NAT

Каждый tunnel имеет свои NAT/FORWARD rules.

Текущая модель использует idempotent iptables rules и reconciliation при apply/sync. Это защищает от stale firewall rules после изменения subnet, port или interface.

Будущее улучшение: отдельный управляемый ruleset, например nftables-модель, где awg-forge еще точнее отличает свои rules от чужих.

## Doctor

Doctor должен быть tunnel-aware.

Global checks:

- root/capabilities;
- `/dev/net/tun`;
- tools;
- iptables backend;
- IPv4 forwarding;
- external interface;
- route.

Per tunnel checks:

- interface name;
- listen port;
- subnet validity;
- render success;
- runtime link;
- `awg show` listen port;
- NAT/FORWARD rules;
- peers;
- stale configs;
- handshakes;
- transfer counters.

## AWG 2.0

AWG 2.0 реализован как отдельный profile и отдельный tunnel.

Он не должен превращать существующий Legacy/1.5 tunnel в 2.0 in-place. Для 2.0 создается новый tunnel и новые client configs.
