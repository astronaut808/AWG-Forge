# Диагностика и troubleshooting

## Doctor

Запуск:

```bash
docker exec awg-forge awg-forge doctor
```

Doctor проверяет:

- root/capabilities;
- `/dev/net/tun`;
- `awg`, `awg-quick`, `amneziawg-go`;
- `iptables`, `ip`, `nf_tables`;
- IPv4 forwarding;
- external interface;
- права config directory;
- UDP listen ports;
- рендер server configs;
- runtime tunnel links;
- runtime `awg show` listen ports;
- NAT/FORWARD firewall rules;
- runtime peers;
- stale client configs;
- handshakes и transfer counters.

## Health В UI

Кнопка `Health` на туннеле делает короткий sample runtime counters и показывает состояние клиентов.

Возможные статусы:

- `traffic flowing`: handshake есть, rx/tx counters растут;
- `idle, handshake ok`: handshake есть, но трафик не двигался во время sample window;
- `client sends traffic, server sends 0 bytes back`: возможная проблема NAT, forwarding, route, DNS или upstream firewall.

## Проверка IPv4 Egress

После импорта клиентского конфига:

```bash
curl -4 https://ifconfig.co
```

Ответ должен показать внешний IP сервера.

## Нет Интернета Через VPN

Проверь внешний интерфейс:

```bash
ip route get 1.1.1.1
```

Если в выводе `dev ens3`, значит:

```env
EXTERNAL_INTERFACE=ens3
```

Дальше:

- запусти `docker exec awg-forge awg-forge doctor`;
- проверь IPv4 forwarding;
- проверь host firewall/UFW;
- в bridge mode проверь, опубликован ли UDP-порт туннеля;
- скачай свежий `.conf`, если менялись tunnel settings или protocol params.

## UI Недоступен

Проверь:

- SSH tunnel;
- `WEBUI_HOST=127.0.0.1`;
- `WEBUI_PORT=51821`;
- `docker compose logs -f`.

## TUN Недоступен

Проверь, что на host существует:

```bash
ls -l /dev/net/tun
```

Compose должен содержать:

```yaml
devices:
  - /dev/net/tun:/dev/net/tun
```

## iptables backend

Doctor ожидает `nf_tables` backend:

```bash
iptables -V
```

В выводе должно быть `nf_tables`.

## Port Already In Use

Если UDP port занят:

- выбери другой tunnel port;
- или останови процесс/интерфейс, который уже слушает этот порт.
