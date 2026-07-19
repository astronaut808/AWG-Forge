# Быстрая установка

`install.sh` — интерактивный установщик для нового Linux/VPS сервера. Он создает runtime `.env`, подготавливает `data/`, инициализирует первый туннель в `state.json`, запускает Docker Compose и показывает дальнейшие шаги.

Перед запуском установи [Docker Engine с официальной документации](https://docs.docker.com/engine/install/). Если Docker или Docker Compose отсутствуют, скрипт завершится до создания `/opt/awg-forge` и любых файлов проекта.

```bash
curl -fsSL https://raw.githubusercontent.com/astronaut808/awg-forge/master/install.sh -o install.sh
chmod +x install.sh
sudo ./install.sh
```

Для интерактивной установки рекомендуется сначала скачать файл. В некоторых окружениях `curl | sudo bash` prompt может выглядеть зависшим из-за особенностей TTY/sudo: тело скрипта и ответы пользователя идут через разные input streams.

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

- проверяет Linux, Docker, Docker Compose и `/dev/net/tun`;
- при повторном запуске обнаруживает существующую установку и предлагает reconfigure или full reinstall;
- предлагает удалить найденные старые AWG-like runtime-интерфейсы, например `awg0`, `awg0-1`, `awg15` или `awg20`;
- определяет внешний интерфейс через `ip route get 1.1.1.1`;
- при чистой установке предлагает endpoint host первого туннеля из найденного source IP, но позволяет указать домен;
- при чистой установке сначала спрашивает protocol profile, затем UDP-порт туннеля, Web UI host/port, subnet, DNS и MTU;
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
sudo ./install.sh upgrade
sudo docker exec awg-forge awg-forge doctor
```

Первая команда показывает состояние до обновления, последняя проверяет его после. Перед каждым upgrade скачивай актуальный `install.sh`: в нём находятся проверки совместимости и migrations для текущей версии. Скрипт скачивает целевой image, останавливает текущий контейнер, сохраняет backup `.env` и `data/`, применяет SQLite migrations до старта нового контейнера, затем проверяет, что контейнер запущен, и выполняет `db status`. Он также выводит Doctor. Если SQLite выключен, скрипт спрашивает, нужно ли его включить; ответ по умолчанию — `No`. Если SQLite включен, но файл базы отсутствует, потребуется явное подтверждение создания новой пустой базы. При ошибке migration, запуска контейнера или `db status` восстанавливаются backup и предыдущий image.

Для другого каталога установки укажи его в `AWG_FORGE_HOME`: `sudo AWG_FORGE_HOME=/srv/awg-forge ./install.sh upgrade`. Если `./install.sh` находит существующую managed-инсталляцию, он также предлагает этот путь обновления в меню действий. Для custom Compose, `CONFIG_DIR` или database path вне `./data` нужен manual upgrade, чтобы operator сделал backup правильных volume.

## Старые tunnel-переменные в `.env`

Старые версии awg-forge хранили параметры первого туннеля в `.env`: `SERVER_HOST`, `LISTEN_PORT`, `IPV4_SUBNET`, `DNS`, `ALLOWED_IPS`, `PERSISTENT_KEEPALIVE`, `MTU`, `PROTOCOL_PROFILE`.

В актуальной версии после появления `state.json` файл `.env` используется только для runtime-настроек. Если Doctor предупреждает о legacy tunnel env variables, проверь настройки туннелей в Web UI и затем удали старые строки из `.env`.

## Безопасность

По умолчанию Web UI слушает только `127.0.0.1`, а доступ идет через SSH tunnel:

```bash
ssh -L 51821:127.0.0.1:51821 user@server
```

Если выбрать `WEBUI_HOST=0.0.0.0` или `::`, скрипт покажет предупреждение и потребует явное подтверждение. Такой режим стоит использовать только за firewall, VPN или reverse proxy.

Пароль показывается в конце установки и хранится в `/opt/awg-forge/.env` или в `.env` внутри `AWG_FORGE_HOME`:

```env
PASSWORD=...
```

## После установки

Открой UI, создай клиента, открой `Config`, импортируй через AmneziaVPN QR или скачанный `.conf` и проверь IPv4 egress:

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

Если нужно удалить awg-forge, сначала запускай uninstall, пока `data/state.json` еще на месте. Так скрипт сможет удалить точные managed firewall rules для каждого туннеля.

```bash
cd /opt/awg-forge
sudo ./uninstall.sh
```

Удалить контейнер, runtime-интерфейсы, firewall rules и локальные файлы установки:

```bash
cd /opt/awg-forge
sudo ./uninstall.sh --purge
```

Проверить действия без остановки контейнера и изменения системы:

```bash
sudo ./uninstall.sh --dry-run --yes
```

По умолчанию скрипт удаляет только интерфейсы и firewall rules, которые может точно связать с туннелями из `data/state.json`. Если state уже потерян, неизвестные интерфейсы `awg*` остаются на хосте, чтобы случайно не удалить чужой AmneziaWG-туннель.

После ручной проверки такие интерфейсы можно удалить явно:

```bash
sudo ./uninstall.sh --remove-orphans
```

Для установки через `curl` без клонирования репозитория:

```bash
curl -fsSL https://raw.githubusercontent.com/astronaut808/awg-forge/master/uninstall.sh | sudo bash
```
