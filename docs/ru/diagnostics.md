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
- IPv4 egress route и совпадение с `EXTERNAL_INTERFACE`;
- `rp_filter` для host/default/external/tunnel interfaces;
- права config directory;
- UDP listen ports;
- UDP listener через `ss`;
- рендер server configs;
- runtime config `/etc/amnezia/amneziawg/<interface>.conf`;
- `awg-quick strip` для runtime config;
- runtime tunnel links;
- runtime `awg show` listen ports;
- NAT/FORWARD firewall rules;
- runtime peers;
- stale client configs;
- handshakes и transfer counters.

## Support Bundle

Support bundle нужен, чтобы передать диагностику без приватных ключей и полных конфигов.

В UI открой `Maintenance` -> `Support`, чтобы скачать `.zip`.

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
- import keys, `vpn://` links, QR payloads или packed AmneziaVPN QR strings;
- raw protocol parameter values.

Bundle также включает `audit-log.redacted.jsonl`: последние audit events с уже отредактированными secret-looking fields.

## Audit Log

Audit log помогает понять последовательность событий: кто-то создал клиента, поменял tunnel settings, скачал новый config, сделал firewall repair, запустил backup или получил apply error.

Команды:

```bash
docker exec awg-forge awg-forge logs
docker exec awg-forge awg-forge logs --tail 200
docker exec awg-forge awg-forge logs --level warn
docker exec awg-forge awg-forge logs --event tunnel.settings.updated
docker exec awg-forge awg-forge logs --json
```

Audit log хранится в `CONFIG_DIR/audit.log`, по умолчанию `/etc/awg-forge/audit.log`, с правами `0600` и ротацией.

Если нужно расследовать “подключение есть, но интернета нет”, полезно смотреть:

- `tunnel.settings.updated`;
- `tunnel.protocol.updated`;
- `client.config.downloaded`;
- `tunnel.apply.failed`;
- `firewall.repaired`;
- `doctor.completed`.

## Encrypted Backup / Restore

Backup отличается от support bundle: он содержит секретный материал, включая `state.json`, private keys, preshared keys и rendered `.conf`.

Backup всегда шифруется отдельным паролем:

```bash
docker exec -e BACKUP_PASSWORD='long-random-backup-password' awg-forge awg-forge backup /tmp/awg-forge.afbackup
docker cp awg-forge:/tmp/awg-forge.afbackup ./awg-forge-backup-YYYYMMDD-HHMMSS.afbackup
```

Restore требует тот же пароль:

```bash
docker cp ./<backup-file>.afbackup awg-forge:/tmp/backup.afbackup
docker exec -e BACKUP_PASSWORD='long-random-backup-password' awg-forge awg-forge restore verify /tmp/backup.afbackup
docker exec -e BACKUP_PASSWORD='long-random-backup-password' awg-forge awg-forge restore /tmp/backup.afbackup
```

`docker exec` видит только filesystem контейнера. Если backup лежит на хосте, сначала скопируй его внутрь контейнера через `docker cp`, как в примере выше. Альтернативно можно положить файл в mounted volume:

```bash
cp ./<backup-file>.afbackup ./data/backup.afbackup
docker exec -e BACKUP_PASSWORD='long-random-backup-password' awg-forge awg-forge restore verify /etc/awg-forge/backup.afbackup
docker exec -e BACKUP_PASSWORD='long-random-backup-password' awg-forge awg-forge restore /etc/awg-forge/backup.afbackup
```

`restore verify` расшифровывает и валидирует backup, рендерит server и client configs в памяти и выводит summary без секретов. Он не пишет в config directory, не создает pre-restore backup, не перезапускает tunnels и не меняет runtime state.

В UI открой `Maintenance` -> `Restore`, загрузи `.afbackup` и запусти такую же проверку в dry-run режиме. Настоящий restore остается CLI-only.

Перед заменой текущего config directory restore сохраняет encrypted pre-restore backup в `backups/` внутри восстановленного config directory.

Restore проверяет:

- пароль и целостность шифротекста;
- `metadata.json`;
- schema version;
- checksums файлов;
- валидность `state.json`;
- возможность render server configs.

Restore не применяет runtime автоматически. После restore явно перезапусти туннели, восстанови managed firewall rules и проверь состояние:

```bash
docker exec awg-forge awg-forge tunnel restart
docker exec awg-forge awg-forge firewall repair
docker exec awg-forge awg-forge doctor
```

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

В UI эта операция доступна через `Maintenance` -> `Firewall` -> `Repair firewall`. Если `APPLY_CONFIG=false`, кнопка визуально недоступна и показывает причину; если `APPLY_CONFIG=true`, действие требует подтверждения.

## Статусы Клиентов В UI

Список клиентов показывает базовый runtime status без отдельного окна диагностики:

- `active now`: клиент недавно сделал handshake;
- `seen recently`: клиент подключался ранее, но сейчас может быть неактивен;
- `never seen`: handshake еще не было;
- `last seen`, `received` и `sent`: время последнего handshake и runtime counters со стороны сервера.

Для глубокой диагностики используй `Maintenance` -> `Doctor`, `Support bundle` и CLI-команды ниже.

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
- выдай свежий client config через `Config`, если менялись tunnel settings или protocol params.

Если `doctor` показывает:

```text
runtime <tunnel>/awg: <interface> link exists, but awg cannot access it: Protocol not supported
```

это значит, что Linux interface существует, но AmneziaWG runtime не может прочитать его как AWG interface. Обычно это stale/broken runtime link после неудачного apply, смены версии tools или ручных экспериментов. Перезапусти туннель из UI или CLI:

```bash
docker exec awg-forge awg-forge tunnel restart
docker exec awg-forge awg-forge doctor
```

Если restart не помог, удали stale link вручную на host/container network namespace и примени туннель заново. Для host networking это обычно:

```bash
docker exec awg-forge ip link delete <interface>
docker exec awg-forge awg-forge tunnel restart
```

Если `doctor` показывает `external route` mismatch, значит NAT может уходить не через тот interface. Проверь `ip route get 1.1.1.1` и обнови `EXTERNAL_INTERFACE`.

Если `rp_filter` в strict mode (`1`), reverse path filtering может отбрасывать VPN-трафик при нестандартных маршрутах или дополнительных firewall/router rules. В простом host-networking setup это редко основная причина, но такой WARN полезен при сложной сети.

Если в строке клиента видно, что `received` растет, а `sent` остается `0 B`, и counters в:

```bash
docker exec awg-forge iptables -L FORWARD -v -n
docker exec awg-forge iptables -t nat -L POSTROUTING -v -n
```

не растут для нужного tunnel subnet/interface, значит трафик не дошел до forwarding/NAT слоя. Проверь `awg show <interface>`, stale link, свежий client config и правильный protocol profile.

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
