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

## Support Bundle

Support bundle нужен, чтобы передать диагностику без приватных ключей и полных конфигов.

В UI нажми `Support`, чтобы скачать `.zip`.

В Docker:

```bash
docker exec awg-forge awg-forge support-bundle
```

С заданным именем файла:

```bash
docker exec awg-forge awg-forge support-bundle /tmp/awg-forge-support.zip
docker cp awg-forge:/tmp/awg-forge-support.zip .
```

Bundle включает:

- redacted config/state summary;
- Doctor results;
- runtime output `ip`, `iptables`, `awg show`;
- inventory config directory без содержимого `.conf`.

Bundle не должен включать:

- private keys;
- preshared keys;
- password;
- session secret;
- rendered server/client configs;
- raw protocol parameter values.

## Encrypted Backup / Restore

Backup отличается от support bundle: он содержит секретный материал, включая `state.json`, private keys, preshared keys и rendered `.conf`.

Backup всегда шифруется отдельным паролем:

```bash
docker exec -e BACKUP_PASSWORD='long-random-backup-password' awg-forge awg-forge backup /tmp/awg-forge.afbackup
docker cp awg-forge:/tmp/awg-forge.afbackup .
```

Restore требует тот же пароль:

```bash
docker cp awg-forge.afbackup awg-forge:/tmp/awg-forge.afbackup
docker exec -e BACKUP_PASSWORD='long-random-backup-password' awg-forge awg-forge restore /tmp/awg-forge.afbackup
```

Перед заменой текущего config directory restore сохраняет encrypted pre-restore backup в `backups/` внутри восстановленного config directory.

Restore проверяет:

- пароль и целостность шифротекста;
- `metadata.json`;
- schema version;
- checksums файлов;
- валидность `state.json`;
- возможность render server configs.

Restore не применяет runtime автоматически. После restore перезапусти контейнер или явно перезапусти туннели.

## Firewall Check / Repair

`doctor` показывает missing или duplicate managed firewall rules. Для ручной проверки:

```bash
docker exec awg-forge awg-forge firewall check
```

Для восстановления managed rules:

```bash
docker exec awg-forge awg-forge firewall repair
```

Repair делает только ожидаемые awg-forge rules для enabled tunnels:

- `nat POSTROUTING MASQUERADE` для tunnel subnet;
- `INPUT udp --dport <port> ACCEPT`;
- `FORWARD -i <interface> ACCEPT`;
- `FORWARD -o <interface> ACCEPT`.

Repair удаляет дубли только этих managed rules и добавляет отсутствующие. Чужие firewall rules не трогает. Disabled tunnels не получают новые rules.

Если `APPLY_CONFIG=false`, `firewall check/repair` ничего не меняет и показывает предупреждение.

В UI эта операция доступна через `Doctor` -> `Repair firewall`. Если `APPLY_CONFIG=false`, кнопка визуально недоступна и показывает причину; если `APPLY_CONFIG=true`, действие требует подтверждения.

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
