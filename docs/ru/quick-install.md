# Быстрая установка

`install.sh` — интерактивный установщик для нового Linux/VPS сервера. Он создает `.env`, подготавливает `data/`, запускает Docker Compose и показывает дальнейшие шаги.

```bash
curl -fsSL https://raw.githubusercontent.com/astronaut808/awg-forge/master/install.sh | sudo bash
```

По умолчанию установка идет в:

```text
/opt/awg-forge
```

Можно указать свой путь:

```bash
curl -fsSL https://raw.githubusercontent.com/astronaut808/awg-forge/master/install.sh | sudo AWG_FORGE_HOME=/srv/awg-forge bash
```

Если репозиторий уже склонирован, можно запустить локальный файл:

```bash
./install.sh
```

## Что делает скрипт

- проверяет Linux, Docker, Docker Compose и `/dev/net/tun`;
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
