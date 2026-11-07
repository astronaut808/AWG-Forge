# Диагностика и troubleshooting

## Doctor

Запуск:

```bash
docker exec awg-forge awg-forge doctor
```

Вывод Doctor сгруппирован по диагностическим категориям: system, security, database, network, firewall, tunnels, clients и WARP.

Doctor проверяет:

- root/capabilities;
- `/dev/net/tun`;
- `awg`, `awg-quick`, `amneziawg-go`;
- `iptables`, `ip`, `nf_tables`;
- session cookie security policy;
- режим TLS Web UI, валидность manual или cached ACME certificate и trusted proxy configuration;
- optional database mode, schema и journal mode;
- превышенные client traffic limits, если включен SQLite;
- IPv4 forwarding;
- external interface;
- IPv4 egress route и совпадение с `EXTERNAL_INTERFACE`;
- `rp_filter` для host/default/external/tunnel interfaces;
- права config directory;
- WARP config, runtime link и policy rules для туннелей с WARP;
- UDP listen ports;
- UDP listener через `ss`;
- диапазоны Docker published UDP ports;
- рендер server configs;
- runtime config `/etc/amnezia/amneziawg/<interface>.conf`;
- `awg-quick strip` для runtime config;
- runtime tunnel links;
- runtime `awg show` listen ports;
- NAT/FORWARD firewall rules;
- runtime peers;
- stale client configs;
- handshakes и transfer counters.

## Диагностика AmneziaWG и WARP

Проверки ниже разделяют три наблюдаемых состояния: входящий UDP-пакет не виден, после прихода пакета нет handshake, после handshake нет egress.

WARP egress применяется на сервере после того, как трафик дошёл до VPS. Он не меняет AmneziaWG endpoint, настроенный на клиенте. Обычный проксируемый DNS record Cloudflare не передаёт произвольный UDP; для AmneziaWG endpoint используй DNS-only запись.

Не добавляй private keys и полные client configs в диагностические отчёты.

### UDP не приходит на сервер

На сервере захвати только порт туннеля во время одной попытки подключения:

```bash
sudo tcpdump -ni "${EXTERNAL_INTERFACE:?Set EXTERNAL_INTERFACE first}" "udp dst port ${LISTEN_PORT:?Set LISTEN_PORT first}"
```

Если пакеты не видны на выбранном интерфейсе:

- сравни DNS-ответ endpoint с ожидаемым IP VPS;
- убедись, что DNS-запись endpoint не проксируется Cloudflare;
- проверь security group провайдера, если она настроена;
- проверь порт и endpoint в заново скачанном client `.conf`;
- повтори проверку из другой сети и зафиксируй результат.

### UDP приходит, но handshake нет

После того как пакеты видны на внешнем интерфейсе, проверь, что сервис их принимает:

```bash
docker exec awg-forge ss -lunp
docker inspect awg-forge --format '{{.HostConfig.NetworkMode}}'
docker port awg-forge
```

`docker port` применим только для bridge networking. Затем выполни:

```bash
docker exec awg-forge awg-forge doctor
```

Doctor показывает runtime peers, handshakes и transfer counters без раскрытия protocol secrets. Не публикуй raw output `awg show` без редактирования: для AWG 3.x он содержит `HeaderProtectionKey`, а `awg show <interface> dump` также может содержать private key интерфейса и preshared keys. Источник данных AWG-Forge — выбранный profile, stale-config status и необходимость заново скачать client config после client-facing изменений туннеля. Для AmneziaWG 2.0 используй fallback через `.conf`, если у целевого клиента есть compatibility limitations.

Зафиксируй сеть, время, версию клиента и результат packet capture до изменения настроек.

### Handshake есть, но интернет не работает

Следуй разделу [Нет Интернета Через VPN](#нет-интернета-через-vpn) и сравни counters туннеля с counters managed `FORWARD` и `POSTROUTING`.

Если выбран WARP egress, Doctor также проверяет runtime WARP и policy rules.

### Проблема есть только у части сетей

Проверь один и тот же client, свежий config, endpoint и временной интервал в рабочей и нерабочей сети. Зафиксируй наблюдаемое состояние:

- DNS resolution;
- UDP до VPS;
- завершение handshake;
- последующий сбой egress; или
- ухудшение после длительной передачи трафика.

### WARP включен, но endpoint недоступен

WARP начинает работать после того, как зашифрованный туннель клиента дошёл до VPS. Сначала проверь путь от клиента до VPS; диагностика WARP имеет смысл после возможного handshake.

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
- database status metadata, если настроена optional operational database;
- runtime output `ip` и `iptables`; сведения о peers, handshakes и transfer берутся из redacted Doctor report;
- inventory config directory без содержимого `.conf`.

Bundle не должен включать:

- private keys;
- preshared keys;
- password;
- session secret;
- rendered server/client configs;
- import keys, `vpn://` links, QR payloads или packed AmneziaVPN QR strings;
- database table rows;
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

Текущие сбои сервиса отдельно проверяй по runtime JSON-логам:

```bash
docker compose logs --tail 200 awg-forge
docker compose logs --no-log-prefix awg-forge | jq 'select(.level == "ERROR" or .level == "WARN")'
```

Не публикуй raw container logs. Секреты в них редактируются, но могут оставаться operational metadata, например tunnel identifiers и interface names.

Если нужно расследовать “подключение есть, но интернета нет”, полезно смотреть:

- `tunnel.settings.updated`;
- `tunnel.protocol.updated`;
- `client.config.downloaded`;
- `tunnel.apply.failed`;
- `firewall.repaired`;
- `doctor.completed`.

## Encrypted Backup / Restore

Резервная копия содержит закрытые ключи и конфигурации клиентов. Храни архив и пароль в защищённом месте. В Bash пароль можно ввести скрыто, без записи в историю команд.

```bash
IFS= read -rsp 'Backup password: ' BACKUP_PASSWORD
printf '\n'
export BACKUP_PASSWORD
```

```bash
docker exec -e BACKUP_PASSWORD awg-forge awg-forge backup /tmp/awg-forge.afbackup &&
  docker cp awg-forge:/tmp/awg-forge.afbackup "./awg-forge-backup-$(date -u +%Y%m%d-%H%M%S).afbackup"
docker exec awg-forge rm /tmp/awg-forge.afbackup
```

Для проверки существующего архива укажи его абсолютный путь в `backup_file`. Команды Compose выполняй из каталога установки.

```bash
backup_file=/absolute/path/to/backup.afbackup
test -f "$backup_file" && docker compose run --rm --no-deps -T -e BACKUP_PASSWORD \
  --volume "$backup_file:/tmp/backup.afbackup:ro" \
  awg-forge restore verify /tmp/backup.afbackup
```

После успешной проверки останови сервис и восстанови копию во время обслуживания. Перед восстановлением сохрани отдельную копию `data/`. Отдельная копия сохраняет историю SQLite и журнал событий. При восстановлении содержимое каталога конфигурации заменяется.

```bash
test -f "$backup_file" &&
  docker compose stop awg-forge &&
  docker compose run --rm --no-deps -T -e BACKUP_PASSWORD \
    --volume "$backup_file:/tmp/backup.afbackup:ro" \
    awg-forge restore /tmp/backup.afbackup &&
  docker compose up -d awg-forge
unset BACKUP_PASSWORD
```

При локальном запуске перед восстановлением останови работающий процесс. Используй ту же переменную `BACKUP_PASSWORD` с командами `awg-forge backup`, `awg-forge restore verify` и `awg-forge restore`.

В панели открой «Обслуживание» → «Резервные копии», чтобы проверить файл `.afbackup` без применения. Проверка выводит сводку без секретов и оставляет настройки туннелей без изменений.

Перед заменой текущего config directory restore сохраняет encrypted pre-restore backup в `backups/` внутри восстановленного config directory.

При сбое staging восстановление возвращает уже перенесённые записи на исходные места. Если откат не удался, оставшиеся исходные файлы сохраняются в `.restore-pending/old/`, а повтор блокируется. Оставь сервис остановленным и сохрани весь каталог конфигурации перед ручным восстановлением. Каталоги восстановления старых версий `.restore-old-*` также блокируют новый restore. Удаляй каталоги восстановления только после проверки восстановленного состояния. Закрытые каталоги имеют права `0700`, восстановленные файлы — `0600`.

Restore проверяет:

- пароль и целостность шифротекста;
- `metadata.json`;
- schema version;
- checksums файлов;
- валидность `state.json`;
- возможность render server configs.

Restore не применяет runtime автоматически. При запуске сервис загрузит восстановленные настройки туннелей и TLS. При `APPLY_CONFIG=true` запуск применяет включенные туннели, а затем согласует runtime WARP. `awg-forge tunnel restart` перезапускает только первый туннель и не перезагружает настройки работающего сервиса или runtime WARP. После запуска восстанови managed firewall rules и проверь состояние:

```bash
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
- stateful forwarding из tunnel subnet через выбранный WAN или WARP egress;
- обратный forwarding из этого egress в tunnel subnet только для соединений `ESTABLISHED,RELATED`.

У каждого текущего managed rule есть comment `awg-forge-<tunnel-id>-...`. Repair удаляет дубли только этих tagged rules и добавляет отсутствующие; чужие firewall rules не трогает. Disabled tunnels не получают новые rules.

После обновления при apply AWG-Forge сначала устанавливает ограниченные rules, затем удаляет точные старые широкие правила `FORWARD -i/-o <interface> ACCEPT` этого туннеля. Миграция распознаёт собственные legacy-директивы runtime-конфигурации и соответствующие остаточные rules на хосте; чужие firewall rules не удаляются. Маршрутизация в приватные сети, доступные через выбранный egress, остаётся задачей operator firewall и routing policy.

Если `APPLY_CONFIG=false`, `firewall check/repair` ничего не меняет и показывает предупреждение.

В UI запусти `Maintenance` -> `Doctor`. Если Doctor показывает проблему с managed firewall rules и `APPLY_CONFIG=true`, в результатах появится `Восстановить firewall rules`.

## Статусы клиентов в UI

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

## Нет интернета через VPN

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
- экспортируй новый конфиг через «Действия» → «Подключить», если изменились настройки туннеля или параметры протокола.

Если `doctor` показывает:

```text
runtime <tunnel>/awg: <interface> link exists, but awg cannot access it: Protocol not supported
```

это значит, что Linux interface существует, но AmneziaWG runtime не может прочитать его как AWG interface. Обычно это stale/broken runtime link после неудачного apply, смены версии tools или ручных экспериментов. Перезапусти туннель из UI или CLI:

```bash
docker exec awg-forge awg-forge tunnel restart
docker exec awg-forge awg-forge doctor
```

Если перезапуск не помог, уточни имя интерфейса туннеля по выводу Doctor и проверь его через `ip link show`. При сетевом режиме хоста команды в контейнере могут удалить интерфейсы хоста. Удаляй только проверенный интерфейс туннеля. Затем перезапусти этот туннель на его странице в панели.

Если `doctor` показывает `external route` mismatch, значит NAT может уходить не через тот interface. Проверь `ip route get 1.1.1.1` и обнови `EXTERNAL_INTERFACE`.

Если `rp_filter` в strict mode (`1`), reverse path filtering может отбрасывать VPN-трафик при нестандартных маршрутах или дополнительных firewall/router rules. В простом host-networking setup это редко основная причина, но такой WARN полезен при сложной сети.

Если в строке клиента видно, что `received` растет, а `sent` остается `0 B`, и counters в:

```bash
docker exec awg-forge iptables -L FORWARD -v -n
docker exec awg-forge iptables -t nat -L POSTROUTING -v -n
```

не растут для нужного tunnel subnet/interface, значит трафик не дошел до forwarding/NAT слоя. Проверь результаты Doctor для peers/handshakes, stale link, свежий client config и правильный protocol profile.

## UI недоступен

Проверь:

- SSH tunnel;
- `WEBUI_HOST=127.0.0.1`;
- `WEBUI_PORT=51821`;
- `docker compose logs -f`.

## TUN недоступен

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

## Порт уже используется

Если UDP port занят:

- выбери другой tunnel port;
- или останови процесс/интерфейс, который уже слушает этот порт.
