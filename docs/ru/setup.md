# Установка и запуск

## Host Networking

Host networking — рекомендуемый production-режим для awg-forge. В этом режиме туннели, созданные в UI, могут использовать любые свободные UDP-порты без изменения Docker port mappings.

Интерактивный quick start:

```bash
curl -fsSL https://raw.githubusercontent.com/astronaut808/awg-forge/master/install.sh | sudo bash
```

Подробнее: [Быстрая установка](quick-install.md).

Ручная настройка:

```bash
cp .env.example .env
mkdir -p data
docker compose up -d
```

По умолчанию Web UI слушает `127.0.0.1:51821`. Для доступа используй SSH tunnel:

```bash
ssh -L 51821:127.0.0.1:51821 user@server
```

Затем открой:

```text
http://127.0.0.1:51821
```

При `network_mode: host` не добавляй `ports:` в `docker-compose.yml`.

## Bridge Networking

Bridge networking тоже может работать, но UDP-порты должны быть опубликованы до старта контейнера. Так как awg-forge позволяет создавать туннели в UI, нужно заранее опубликовать диапазон портов и создавать туннели только внутри него.

```bash
cp .env.example .env
mkdir -p data
docker compose -f docker-compose.bridge.yml up -d
```

Пример `docker-compose.bridge.yml` публикует:

- Web UI: `127.0.0.1:51821:51821/tcp`;
- UDP-порты туннелей: `51820-51840:51820-51840/udp`.

В bridge mode держи tunnel listen ports внутри `51820-51840`, если не меняешь compose-файл и не пересоздаешь контейнер.

Для bridge mode также выставь:

```env
PUBLISHED_UDP_PORTS=51820-51840
```

Так UI и `doctor` смогут предупреждать, если туннель создан на неопубликованном UDP-порту.

## Проверка запуска

```bash
docker compose ps
docker exec awg-forge awg-forge doctor
```

Если UI недоступен, проверь:

- SSH tunnel;
- `WEBUI_HOST`;
- `WEBUI_PORT`;
- `docker compose logs -f`.
