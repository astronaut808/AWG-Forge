# Быстрая установка

`install.sh` — интерактивный установщик для нового Linux/VPS сервера. Он создает runtime `.env`, подготавливает `data/`, инициализирует первый туннель в `state.json`, запускает Docker Compose и показывает дальнейшие шаги.

Установщик предлагает подготовить недостающие зависимости на Ubuntu 22.04/24.04/26.04, Debian 12/13, CentOS Stream 9/10 и RHEL 8/9/10 (systemd, x86_64). При необходимости он установит Docker Engine и Compose plugin из официального подписанного репозитория Docker, а также `curl`, CA-сертификаты, OpenSSL, `ip`/`ss`, `iptables`, `modprobe` и `awk`. Запускай его через `sudo`; установка пакетов и запуск Docker требуют подтверждения. Уже работающие Docker/Compose используются повторно; конфликтующие контейнерные runtime автоматически не удаляются. На других Linux-дистрибутивах зависимости нужно подготовить вручную. Образы ARM64 пока не публикуются.

Для работы нужен `/dev/net/tun`. Установщик попробует `modprobe tun` и остановится до создания файлов проекта, если ядро или провайдер VPS не предоставляют TUN. Userspace runtime AmneziaWG уже есть в образе: устанавливать AmneziaWG или DKMS-модуль на хост не нужно. Правила firewall провайдера и доступность публичных UDP/TCP-портов остаются ответственностью оператора.

На хостах с SELinux (обычно CentOS/RHEL) новая установка задаёт метку только приватному volume `data/` через `:Z`. SELinux остаётся включённым. Для существующего custom Compose подходящие метки volume нужно настроить вручную: установщик его не переписывает. RHEL должен иметь доступ к штатным репозиториям пакетов (подписка или соответствующее зеркало).

```bash
curl -fsSL https://raw.githubusercontent.com/astronaut808/awg-forge/master/install.sh -o install.sh
chmod +x install.sh
sudo ./install.sh
```

URL с `master` получает актуальный стабильный установщик. Нерелизные изменения накапливаются в `develop`; для их проверки используй явный test image. Для интерактивной установки рекомендуется сначала скачать файл. В некоторых окружениях `curl | sudo bash` prompt может выглядеть зависшим из-за особенностей TTY/sudo: тело скрипта и ответы пользователя идут через разные input streams.

Для проверки не-релизного образа можно передать `IMAGE`:

```bash
sudo IMAGE=ghcr.io/astronaut808/awg-forge:test ./install.sh
```

По умолчанию установка идет в:

```text
/opt/awg-forge
```

Можно указать свой путь:

```bash
curl -fsSL https://raw.githubusercontent.com/astronaut808/awg-forge/master/install.sh -o install.sh
chmod +x install.sh
sudo AWG_FORGE_HOME=/srv/awg-forge ./install.sh
```

Если репозиторий уже склонирован, можно запустить локальный файл:

```bash
./install.sh
```

## Что делает скрипт

- определяет дистрибутив, предлагает недостающие зависимости, при необходимости запускает Docker и проверяет Compose и `/dev/net/tun` до записи файлов проекта;
- при повторном запуске обнаруживает существующую установку и предлагает reconfigure или full reinstall;
- предлагает удалить найденные старые AWG-like runtime-интерфейсы, например `awg0`, `awg0-1`, `awg15` или `awg20`;
- определяет внешний интерфейс через `ip route get 1.1.1.1`;
- при чистой установке предлагает endpoint host первого туннеля из найденного source IP, но позволяет указать домен;
- при чистой установке сначала спрашивает protocol profile, затем предлагает автоматический выбор свободного UDP-порта из `30000-49999` или ручной ввод;
- по умолчанию выбирает AmneziaWG 2.0, если просто нажать Enter на вопросе профиля;
- генерирует `PASSWORD` и `SESSION_SECRET`;
- создает runtime `.env` с правами `0600`;
- включает SQLite и применяет его начальную схему до старта сервиса;
- создает `data/` с правами `0700`;
- до запуска сервиса выполняет одноразовый `docker run ... init`, который создает `data/state.json` с первым туннелем;
- создает `docker-compose.yml`, если его еще нет;
- использует host networking compose-файл;
- запускает `docker compose up -d`;
- запускает `docker exec awg-forge awg-forge doctor`;
- показывает пароль, путь к `.env` и SSH tunnel команду.

Если `.env` уже существует, скрипт сохранит backup вида:

```text
.env.backup-YYYYMMDD-HHMMSS
```

## Повторный запуск и full reinstall

Если в рабочей директории уже есть `.env`, `data/` или `docker-compose.yml`, установщик спросит, что делать:

```text
1) Reconfigure existing install, keep data and backup .env
2) Full reinstall, backup and remove old data/config first
3) Upgrade image, keep data and run required database migrations
4) Abort
```

`Reconfigure` оставляет `data/` на месте, делает backup старого `.env`, обновляет только выбранные runtime-значения и пересоздает контейнер. Существующие настройки SQLite, TLS и trusted proxy остаются без изменений. Существующие туннели остаются в `data/state.json` и не пересоздаются из `.env`.

`Full reinstall` сначала сохраняет текущие файлы в директорию вида:

```text
reinstall-backup-YYYYMMDD-HHMMSS/
```

Потом останавливает контейнер, удаляет managed firewall rules, AWG runtime-интерфейсы, `.env`, `data/` и `docker-compose.yml`, после чего запускает установку как с чистого состояния.

Важно: после full reinstall старые клиентские конфиги больше не подходят, потому что состояние, ключи и параметры туннеля создаются заново. Клиентам нужно выдать свежие `.conf`.

## Обновление

Для managed installation со стандартными `.env`, `docker-compose.yml` и `./data` используй:

```bash
sudo docker exec awg-forge awg-forge doctor
curl -fsSL https://raw.githubusercontent.com/astronaut808/awg-forge/master/install.sh -o install.sh
chmod +x install.sh
sudo AWG_FORGE_HOME=/opt/awg-forge ./install.sh upgrade
sudo docker exec awg-forge awg-forge doctor
```

Первая команда показывает состояние до обновления, последняя проверяет его после. Используй установщик из актуального release, чтобы его проверки совместимости и migrations соответствовали этой версии. Скрипт скачивает целевой image, останавливает текущий контейнер, сохраняет backup `.env` и `data/`, применяет SQLite migrations до старта нового контейнера, затем проверяет, что контейнер запущен, и выполняет `db status`. Он также выводит Doctor. Если SQLite выключен, скрипт спрашивает, нужно ли его включить; ответ по умолчанию — `No`. Если SQLite включен, но файл базы отсутствует, потребуется явное подтверждение создания новой пустой базы. При ошибке migration, запуска контейнера или `db status` восстанавливаются backup и предыдущий image.

Для другого каталога установки укажи его в `AWG_FORGE_HOME`: `sudo AWG_FORGE_HOME=/srv/awg-forge ./install.sh upgrade`. Если `./install.sh` запущен из каталога существующей managed-инсталляции, он также предлагает этот путь обновления в меню действий. Для custom Compose, `CONFIG_DIR` или database path вне `./data` нужен manual upgrade, чтобы operator сделал backup правильных volume.

## Старые tunnel-переменные в `.env`

Старые версии awg-forge хранили параметры первого туннеля в `.env`: `SERVER_HOST`, `LISTEN_PORT`, `IPV4_SUBNET`, `DNS`, `ALLOWED_IPS`, `PERSISTENT_KEEPALIVE`, `MTU`, `PROTOCOL_PROFILE`.

В актуальной версии после появления `state.json` файл `.env` используется только для runtime-настроек. Если Doctor предупреждает о legacy tunnel env variables, проверь настройки туннелей в Web UI и затем удали старые строки из `.env`.

## Безопасность

По умолчанию Web UI слушает только `127.0.0.1`, а доступ идет через SSH tunnel:

```bash
ssh -L 51821:127.0.0.1:51821 user@server
```

Если выбрать `WEBUI_HOST=0.0.0.0` или `::`, скрипт покажет предупреждение и потребует явное подтверждение. Такой режим стоит использовать только за firewall, VPN или reverse proxy.

При чистой установке скрипт также предлагает ACME-сертификат для домена или короткоживущий сертификат для публичного IP. Выбирай любой вариант только когда TCP/80 доступен из Интернета. Панель остаётся на выбранном Web UI-порту. Установщик не ждёт CA: для IP первая попытка выпуска начинается после готовности listener, а для домена выпуск запрашивается первым HTTPS-запросом к настроенному имени. Doctor и `tls status` показывают ожидающий статус и повторы; ограничения и восстановление описаны в [TLS-конфигурации](configuration.md#tls-для-web-ui).

Пароль показывается в конце установки и хранится в `/opt/awg-forge/.env` или в `.env` внутри `AWG_FORGE_HOME`:

```env
PASSWORD=...
```

## После установки

Открой UI, создай клиента, открой `Config` и используй один из предложенных способов импорта. AWG 3.x поддерживает `.conf`, QR для AmneziaWG, QR для AmneziaVPN и `vpn://`; используй AmneziaVPN 5.0.1.5+ и оставляй `.conf` как fallback. Затем проверь IPv4 egress:

```bash
curl -4 https://ifconfig.co
```

Полезные команды:

```bash
docker compose ps
docker compose logs -f
docker exec awg-forge awg-forge doctor
```

## Удаление

Если нужно удалить awg-forge, сначала запускай uninstall, пока `data/state.json` еще на месте. Так скрипт сможет удалить точные managed tunnel-интерфейсы, firewall rules (текущие tagged scoped rules и legacy untagged rules) и policy routes WARP, если они используются.

Перед удалением всегда запускай актуальный `uninstall.sh` из `master`: в нем есть cleanup-логика для текущих и старых установок.

```bash
curl -fsSL https://raw.githubusercontent.com/astronaut808/awg-forge/master/uninstall.sh | sudo bash
```

Удалить контейнер, runtime-интерфейсы, firewall rules и локальные файлы установки:

```bash
curl -fsSL https://raw.githubusercontent.com/astronaut808/awg-forge/master/uninstall.sh | sudo bash -s -- --purge
```

Проверить действия без остановки контейнера и изменения системы:

```bash
curl -fsSL https://raw.githubusercontent.com/astronaut808/awg-forge/master/uninstall.sh | sudo bash -s -- --dry-run --yes
```

По умолчанию скрипт удаляет только интерфейсы и firewall rules, которые может точно связать с туннелями из `data/state.json`. Если state уже потерян, неизвестные интерфейсы `awg*` остаются на хосте, чтобы случайно не удалить чужой AmneziaWG-туннель.

`--yes` подтверждает очистку runtime без вопросов и сохраняет `data/`, `.env` и `docker-compose.yml`. Добавляй `--purge`, только когда эти локальные файлы установки тоже нужно удалить.

После ручной проверки такие интерфейсы можно удалить явно:

```bash
curl -fsSL https://raw.githubusercontent.com/astronaut808/awg-forge/master/uninstall.sh | sudo bash -s -- --remove-orphans
```

## Явное подключение через installer к controller

Обычная установка остаётся standalone. В Maintenance → Controller после свежего MFA подготовьте точный control endpoint, скачайте и сохраните проверенный зашифрованный backup, затем включите listener. Для внешнего endpoint нужен отдельный флажок согласия. В Add node выберите новую или существующую установку и имя. Перед approve сравните код и имя с терминалом узла; Connected появляется только после authenticated presence этого enrollment. Закрытие flow, logout, смена аккаунта, reject и expiry очищают invitation и завершают polling.

После подключения выберите **Этот сервер** или узел для
[просмотра состояния](usage.md#просмотр-состояния-узлов). Доступность узла и
актуальность снимка показаны отдельно от состояния VPN; удалённых действий
и экспорта клиентских конфигураций в представлении узла нет.

Для этой develop-сборки совместимый installer ещё не опубликован. UI копирует **только публичные аргументы** и сообщает об отсутствии release. Несуществующих release URLs и fallback на `latest` нет. Для локальной проверки используйте скачанный/проверенный script с подтверждённым SHA-256 и явно собранный локальный image ID с совпадающими compiled version/commit. В шаблоне ниже только публичные placeholders; замените их проверенной artifact metadata и значениями из UI:

```bash
sudo bash ./install.sh join --workdir /opt/awg-node --mode fresh \
  --image sha256:IMAGE_ID_64_HEX --script-sha256 SCRIPT_SHA256_64_HEX \
  --artifact-version local-SOURCE_HASH_12_HEX --artifact-commit COMMIT_40_HEX \
  --controller-url 'https://controller.example:9443' \
  --ca-pin 'sha256:CA_SPKI_PIN_64_HEX' --invitation-id INVITATION_UUID --name 'node'
```

Script и локальный image должны поддерживать `installer-onboarding-v1`; несовпадение metadata, старый или отсутствующий локальный image отклоняются до остановки сервиса. Неявного pull нет. Опубликованный immutable GHCR digest допустим только с совпадающей compiled metadata. Получите invitation secret отдельно в authenticated UI и вставьте в скрытый prompt терминала. Не включайте его в команду, exported variables, heredoc или shell substitution. Для автоматизации разрешён явно переданный private pipe/FD на stdin с `--secret-fd 0`; stdin не используется неявно, в том числе при `curl | bash`.

Fresh join требует несуществующий workdir непосредственно внутри существующего каталога. Он создаёт защищённые local login credentials в `.env`, loopback Web UI для break-glass, DB-off и не создаёт туннель либо WARP/ACME setup. Используйте SSH tunnel; пароль смотрите в защищённом локальном `.env`, не копируя его в логи.

Для существующего root-run Compose service замените `--mode fresh` на `--mode existing --maintenance`; при необходимости укажите `--container NAME`. Обязательны точные Compose working-directory/service labels и проверенный image. Старую установку сначала обновите отдельно. Installer останавливает и запускает только этот container ID, использует его mounts/environment, сохраняя Compose, networks, logging, UI/TLS/session policy, history и локальные туннели. Неподдерживаемые container users, явные user namespaces, SELinux process/mount labels и multiline environment отклоняются до stop. Installer join/rebind требует Docker без SELinux labels и отклоняет SELinux-enabled daemon до создания fresh файлов; для установок с приватным volume `:Z` нужен отдельный offline enrollment/recovery workflow с учётом deployment. Не отключайте SELinux и не меняйте labels существующих данных ради обхода этой проверки. Действие прерывает VPN service; исходно остановленный сервис остаётся остановленным без явного `--start-service`.

Для managed node используйте отдельный `rebind` с `--maintenance`, новым invitation и точными локальными `--confirm-node-id`/`--confirm-controller-id`. Он вызывает offline recovery напрямую, без detach-first. Отзыв скопированных прежних credentials на старом controller выполняется отдельно.

Неудачный/прерванный fresh join сохраняет защищённый workdir для offline inspection: ошибка может возникнуть после identity commit. Не повторяйте consumed invitation и не удаляйте pending journal вслепую. До commit существующий сервис возобновляется с прежней identity, если завершение helper доказано. После commit ошибка startup/connectivity остаётся pending; сохраните новую identity и диагностируйте/повторите запуск сервиса. Подключение подтверждается только authenticated presence на controller. После setup недоступность controller не мешает локальному forwarding.
