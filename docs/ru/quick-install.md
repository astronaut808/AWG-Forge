# Быстрая установка

`install.sh` — интерактивный установщик для нового Linux/VPS сервера. Он создает `.env`, подготавливает `data/`, запускает Docker Compose и показывает дальнейшие шаги.

```bash
curl -fsSL https://raw.githubusercontent.com/astronaut808/awg-forge/master/install.sh -o install.sh
chmod +x install.sh
sudo ./install.sh
```

Для интерактивной установки рекомендуется сначала скачать файл. В некоторых окружениях `curl | sudo bash` prompt может выглядеть зависшим из-за особенностей TTY/sudo: тело скрипта и ответы пользователя идут через разные input streams.

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
- предлагает `SERVER_HOST` из найденного source IP, но позволяет указать домен;
- спрашивает UDP-порт туннеля, Web UI host/port, subnet, DNS, MTU и protocol profile;
- генерирует `PASSWORD` и `SESSION_SECRET`;
- создает `.env` с правами `0600`;
- создает `data/` с правами `0700`;
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
3) Abort
```

`Reconfigure` оставляет `data/` на месте, делает backup старого `.env` и пересоздает контейнер с новыми переменными.

`Full reinstall` сначала сохраняет текущие файлы в директорию вида:

```text
reinstall-backup-YYYYMMDD-HHMMSS/
```

Потом останавливает контейнер, удаляет managed firewall rules, AWG runtime-интерфейсы, `.env`, `data/` и `docker-compose.yml`, после чего запускает установку как с чистого состояния.

Важно: после full reinstall старые клиентские конфиги больше не подходят, потому что состояние, ключи и параметры туннеля создаются заново. Клиентам нужно выдать свежие `.conf`.

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

Открой UI, создай клиента, импортируй `.conf` в AmneziaVPN и проверь IPv4 egress:

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

Для установки через `curl` без клонирования репозитория:

```bash
curl -fsSL https://raw.githubusercontent.com/astronaut808/awg-forge/master/uninstall.sh | sudo bash
```
