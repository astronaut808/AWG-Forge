# Обновления AmneziaWG

Docker-образ awg-forge содержит pinned версии upstream AmneziaWG tools:

- `amneziawg-go`;
- `amneziawg-tools`.

Pinned refs лежат здесь:

```bash
cat build/amneziawg.refs
```

## Важное решение

awg-forge не обновляет AmneziaWG tools внутри running container автоматически.

Схема обновлений ручная:

1. Проверить, появились ли новые upstream commits.
2. Обновить pinned refs в репозитории awg-forge.
3. Пересобрать Docker image.
4. Проверить реальные туннели и клиенты.
5. Выпустить новый awg-forge release/image.

Такой подход дает воспроизводимые сборки и снижает риск внезапно сломать рабочие VPN-туннели.

## Проверка Обновлений Локально

```bash
make updates-local
```

## Проверка Обновлений В Docker

```bash
make updates-docker
# или
docker exec awg-forge awg-forge updates
```

## Обновить pinned refs для будущего PR

```bash
make update-amneziawg-refs
```

После этого нужно собрать образ и проверить работу:

```bash
make docker-build
```

## Web UI

Кнопка `Updates` делает read-only проверку upstream refs.

Она не меняет running system, не скачивает новые binaries и не перезапускает туннели.
