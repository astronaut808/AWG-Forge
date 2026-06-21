# awg-forge

[English README](README.en.md)

Самостоятельно размещаемая панель управления AmneziaWG для Docker: Go backend, встроенный Web UI и CLI для туннелей, клиентов, `.conf`, диагностики, backup/restore и безопасного обслуживания.

awg-forge не реализует собственный VPN-протокол. Он генерирует конфиги AmneziaWG и управляет upstream-инструментами `awg`, `awg-quick` и `amneziawg-go`, которые входят в Docker-образ.

![Главный экран awg-forge](docs/assets/awg-forge-dashboard.jpg)

## Что Поддерживается

- AmneziaWG Legacy / 1.0, 1.5-oriented profile и 2.0.
- Несколько независимых туннелей с разными профилями, портами и подсетями.
- IPv4 egress через `Server WAN` или Cloudflare WARP на уровне отдельного туннеля.
- Клиенты: создание, скачивание `.conf`, `vpn://` import key, enable/disable, expiration, delete.
- Runtime diagnostics: Doctor, firewall repair, health, last seen, received/sent counters.
- Maintenance Center: WARP, backup, restore verify, support bundle, live audit logs, updates, system info.

Надежный production-импорт клиента — скачанный `.conf`. `vpn://` import key экспериментальный и зависит от клиента AmneziaVPN / DefaultVPN. QR import намеренно не используется.

## Быстрый Старт

Интерактивная установка на Linux/VPS (необходим Docker):

```bash
curl -fsSL https://raw.githubusercontent.com/astronaut808/awg-forge/master/install.sh -o install.sh
chmod +x install.sh
sudo ./install.sh
```

Скрипт проверит Docker до создания файлов, создаст `/opt/awg-forge`, сгенерирует `.env`, пароль и `SESSION_SECRET`, определит внешний интерфейс, запустит Docker Compose и покажет SSH tunnel команду.

По умолчанию Web UI слушает `127.0.0.1:51821`. Открывай через SSH tunnel:

```bash
ssh -L 51821:127.0.0.1:51821 user@server
```

Затем:

```text
http://127.0.0.1:51821
```

## Ручной Запуск

```bash
git clone https://github.com/astronaut808/awg-forge.git
cd awg-forge
cp .env.example .env
mkdir -p data
docker compose up -d
```

Рекомендуемый production-режим — Docker host networking. Так туннели, созданные в UI, могут использовать разные UDP-порты без изменения Docker port mappings.

## Важные Настройки

- `SERVER_HOST` — endpoint по умолчанию для клиентских конфигов.
- `EXTERNAL_INTERFACE` — внешний интерфейс сервера для WAN egress.
- `WEBUI_HOST=127.0.0.1` — безопасный дефолт для доступа через SSH tunnel.
- `APPLY_CONFIG=true` — применять runtime-туннели и firewall rules.
- `SESSION_COOKIE_SECURE=auto|true|false` — политика Secure cookie для Web UI.

`SERVER_HOST` можно переопределить для конкретного туннеля в `Tunnel settings` -> `Server host`.

WARP можно выбрать при создании туннеля или включить позже в `Tunnel settings` -> `Egress` -> `Cloudflare WARP`. Если WARP еще не настроен, awg-forge зарегистрирует общий `warp0` автоматически. Подробнее: [Конфигурация](docs/ru/configuration.md).

## Проверка После Запуска

1. Создай клиента в UI.
2. Импортируй скачанный `.conf` в AmneziaVPN.
3. Проверь IPv4 egress:

```bash
curl -4 https://ifconfig.co
```

Doctor:

```bash
docker exec awg-forge awg-forge doctor
```

## Обслуживание

Удаление установленного экземпляра:

```bash
cd /opt/awg-forge
sudo ./uninstall.sh
```

Без клонирования репозитория:

```bash
curl -fsSL https://raw.githubusercontent.com/astronaut808/awg-forge/master/uninstall.sh | sudo bash
```

Dry-run перед удалением:

```bash
cd /opt/awg-forge
sudo ./uninstall.sh --dry-run --yes
```

Backup/restore, firewall repair, support bundle, logs и update checks доступны в `Maintenance Center` и CLI.

## Документация

- [README EN](README.en.md)
- [Contributing](CONTRIBUTING.md)
- [Security policy](SECURITY.md)
- [Документация на русском](docs/ru/README.md)
- [Быстрая установка](docs/ru/quick-install.md)
- [Установка и запуск](docs/ru/setup.md)
- [Конфигурация](docs/ru/configuration.md)
- [Web UI и CLI](docs/ru/usage.md)
- [Диагностика и troubleshooting](docs/ru/diagnostics.md)
- [Обновления AmneziaWG](docs/ru/updates.md)
- [Разработка](docs/ru/development.md)
- [Безопасность](docs/ru/security.md)
- [Changelog](CHANGELOG.md)

## Разработка

```bash
make ci
```

Локальный запуск без применения runtime-туннелей:

```bash
CONFIG_DIR=/private/tmp/awg-forge-dev \
WEBUI_HOST=127.0.0.1 \
WEBUI_PORT=51821 \
PASSWORD=test \
APPLY_CONFIG=false \
SERVER_HOST=127.0.0.1 \
go run ./cmd/awg-forge serve
```

Runtime и Docker image не требуют Node/npm. Web UI собирается из `web/` через Vite/Preact/TypeScript и встраивается в Go-бинарь как статические файлы.

## Лицензия

[MIT](LICENSE)
