# AWG-Forge

[English README](README.en.md)

Панель управления AmneziaWG на своём сервере: Web UI, CLI и VPN runtime в одном Docker-образе.

![Главный экран AWG-Forge](docs/assets/awg-forge-dashboard.jpg)

## Почему AWG-Forge

- Установка на VPS через интерактивный скрипт.
- Несколько независимых туннелей с отдельными портами, подсетями и профилями.
- Управление клиентами, сроками доступа, конфигурациями и QR-кодами через Web UI.
- IPv4-трафик напрямую через сервер или через Cloudflare WARP — для каждого туннеля.
- Диагностика, резервные копии и восстановление в панели и CLI.
- Панель по умолчанию доступна только локально, через SSH-туннель.

AmneziaWG 2.0 используется по умолчанию. AWG 3.x — экспериментальный профиль;
совместимость и ограничения описаны в [матрице профилей](docs/ru/protocol-matrix.md).

## Быстрая установка

На Linux/VPS (x86_64):

```bash
curl -fsSL https://raw.githubusercontent.com/astronaut808/awg-forge/master/install.sh -o install.sh
chmod +x install.sh
sudo ./install.sh
```

Установщик подготовит зависимости, создаст первый туннель и покажет пароль и команду
для доступа к панели. [Требования и дальнейшие шаги](docs/ru/quick-install.md).

## Документация

- [Установка и ручной запуск](docs/ru/setup.md)
- [Обновление](docs/ru/quick-install.md#обновление) · [Удаление](docs/ru/quick-install.md#удаление)
- [Конфигурация, Web UI, CLI и диагностика](docs/ru/README.md)
- [Разработка](docs/ru/development.md) · [Участие в проекте](CONTRIBUTING.md)
- [История изменений](CHANGELOG.md) · [Политика безопасности](SECURITY.md)

## Поддержать проект

- USDT (TRC20): `TBQcgJ9UoGEBXBwPMcf97t3uJiTCRnVmji`
- GRAM (ex TON): `UQCrUmIsUBgIJJJNKpvOO5dxpUH5r7xCz9-AJ2IHUTIckJhS`

## Независимость проекта

AWG-Forge — независимый open-source проект, не аффилированный с Amnezia.org
и не разрабатываемый или поддерживаемый командой Amnezia. Название AmneziaWG
обозначает совместимость с протоколом и инструментами.

## Лицензия

[MIT](LICENSE)
