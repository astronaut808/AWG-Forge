# awg-forge

[English README](README.en.md)

awg-forge — self-hosted менеджер AmneziaWG для Docker. Проект дает небольшой Go backend, статический Web UI и CLI для запуска AmneziaWG-туннелей и управления клиентскими `.conf` файлами.

awg-forge не реализует собственный VPN-протокол. Он генерирует конфиги AmneziaWG и управляет существующими upstream-инструментами `awg`, `awg-quick` и `amneziawg-go`, которые входят в Docker-образ.

## Статус

Поддерживаемые профили:

- AmneziaWG Legacy / 1.0;
- AmneziaWG 1.5;
- AmneziaWG 2.0.

Поддерживаемый способ импорта клиента:

- скачивание `.conf`.

QR import не используется. Он был убран, потому что `.conf` импорт остается самым надежным способом для текущих клиентов AmneziaVPN.

## Возможности

- Web UI с вкладками профилей `1.0`, `1.5`, `2.0`.
- Несколько туннелей внутри каждого профиля.
- Создание, отключение, включение, удаление клиентов.
- Автоматическое скачивание `.conf` после успешного создания клиента.
- Настройки туннеля: порт, подсеть, DNS, allowed IPs, keepalive, MTU и enabled state.
- Генерация и валидация protocol params для Legacy / 1.0, 1.5 и 2.0.
- Безопасные ненулевые параметры обфускации для новых туннелей.
- IPv4 egress с согласованием NAT/firewall rules.
- Health view для клиентов: handshake и rx/tx counters.
- Doctor diagnostics для инструментов, runtime, firewall, ports, peers, handshakes и stale configs.
- Ручная проверка и repair managed firewall rules.
- Support bundle без секретов для безопасной передачи диагностики.
- Encrypted backup/restore с отдельным backup password.
- Откат state/configs при ошибке применения runtime-конфига.
- Проверка обновлений upstream AmneziaWG без автоматического изменения системы.
- Статический HTML/CSS/JavaScript frontend без Node/npm build pipeline.

## Быстрый Старт

Интерактивная установка на Linux/VPS:

```bash
curl -fsSL https://raw.githubusercontent.com/astronaut808/awg-forge/master/install.sh -o install.sh
chmod +x install.sh
sudo ./install.sh
```

Скрипт создаст рабочую директорию `/opt/awg-forge`, сгенерирует `.env`, пароль, `SESSION_SECRET`, определит внешний интерфейс, запустит Docker Compose и покажет SSH tunnel команду.

Ручной запуск:

```bash
git clone https://github.com/astronaut808/awg-forge.git
cd awg-forge
cp .env.example .env
mkdir -p data
docker compose up -d
```

По умолчанию Web UI слушает `127.0.0.1:51821`. Открой его через SSH tunnel:

```bash
ssh -L 51821:127.0.0.1:51821 user@server
```

Затем открой:

```text
http://127.0.0.1:51821
```

Host networking — рекомендуемый production-режим, потому что туннели, созданные в UI, могут использовать любые свободные UDP-порты без изменения Docker port mappings.

`SERVER_HOST` задает endpoint по умолчанию для клиентских конфигов. Для отдельных туннелей его можно переопределить в Web UI через `Tunnel settings` → `Server host`. Подробнее: [Конфигурация](docs/ru/configuration.md).

Удаление:

```bash
cd /opt/awg-forge
sudo ./uninstall.sh
```

Удаление без клонирования репозитория:

```bash
curl -fsSL https://raw.githubusercontent.com/astronaut808/awg-forge/master/uninstall.sh | sudo bash
```

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
- [План frontend](docs/ru/frontend-spec.md)
- [Техническая архитектура multi-profile / multi-tunnel](docs/ru/multi-profile-architecture.md)
- [Матрица протоколов](docs/ru/protocol-matrix.md)
- [Дизайн AWG 2.0](docs/ru/awg-2.0-design.md)
- [Исследование импорта и подписок AmneziaVPN](docs/ru/research/amnezia-import-subscriptions.md)
- [Changelog](CHANGELOG.md)

## Минимальная Проверка После Запуска

```bash
docker exec awg-forge awg-forge doctor
docker exec -e BACKUP_PASSWORD='long-random-backup-password' awg-forge awg-forge backup /tmp/awg-forge.afbackup
docker exec awg-forge awg-forge support-bundle
```

Создай клиента в UI, импортируй скачанный `.conf` в AmneziaVPN и проверь IPv4 egress:

```bash
curl -4 https://ifconfig.co
```

Ответ должен показать внешний IP сервера.

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

Подробнее: [Разработка](docs/ru/development.md).

## Лицензия

[MIT](LICENSE)
