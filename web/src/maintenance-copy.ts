import type { Locale } from "./i18n";

const copy = {
  en: {
    loadingAudit: "Loading audit events…", auditUnavailable: "Audit events could not be loaded.", retry: "Retry", staleAudit: "Showing the last successful audit refresh.", lastUpdated: "Last updated", loadingTraffic: "Loading traffic history…", trafficUnavailable: "Traffic history could not be loaded.", staleTraffic: "Showing the last successful traffic refresh.", doctorHelp: "Runs read-only checks for service, configuration, network, and firewall health.", warpHelp: "Registering WARP creates or replaces this server’s WARP identity and may require tunnels to be applied again.", warpDeleteBlocked: "Delete is unavailable while tunnels use WARP. Move or disable those tunnels first.", severity: { ok: "OK", warn: "Warning", fail: "Failed", bad: "Critical", neutral: "Info", info: "Info", error: "Error", debug: "Debug" }, events: { "tunnel.created": "Tunnel created", "tunnel.updated": "Tunnel updated", "tunnel.deleted": "Tunnel deleted", "client.created": "Client created", "client.updated": "Client updated", "client.deleted": "Client deleted", "warp.registered": "WARP registered", "warp.deleted": "WARP deleted", "backup.created": "Backup created", "firewall.repaired": "Firewall repaired" },
  },
  ru: {
    loadingAudit: "Загрузка событий аудита…", auditUnavailable: "Не удалось загрузить события аудита.", retry: "Повторить", staleAudit: "Показаны данные последнего успешного обновления аудита.", lastUpdated: "Последнее обновление", loadingTraffic: "Загрузка истории трафика…", trafficUnavailable: "Не удалось загрузить историю трафика.", staleTraffic: "Показаны данные последнего успешного обновления трафика.", doctorHelp: "Запускает проверки состояния сервиса, конфигурации, сети и firewall без изменения настроек.", warpHelp: "Регистрация WARP создаёт или заменяет WARP-идентификатор сервера и может потребовать повторного применения туннелей.", warpDeleteBlocked: "Удаление недоступно, пока туннели используют WARP. Сначала перенесите или отключите эти туннели.", severity: { ok: "ОК", warn: "Предупреждение", fail: "Ошибка", bad: "Критично", neutral: "Информация", info: "Информация", error: "Ошибка", debug: "Отладка" }, events: { "tunnel.created": "Туннель создан", "tunnel.updated": "Туннель изменён", "tunnel.deleted": "Туннель удалён", "client.created": "Клиент создан", "client.updated": "Клиент изменён", "client.deleted": "Клиент удалён", "warp.registered": "WARP зарегистрирован", "warp.deleted": "WARP удалён", "backup.created": "Резервная копия создана", "firewall.repaired": "Firewall восстановлен" },
  },
} as const;

export function maintenanceCopy(locale: Locale) {
  return copy[locale === "ru" ? "ru" : "en"];
}

export function maintenanceDate(value: string | Date, locale: Locale): string {
  const date = value instanceof Date ? value : new Date(value);
  if (Number.isNaN(date.getTime())) return "—";
  return new Intl.DateTimeFormat(locale === "ru" ? "ru-RU" : "en-US", { dateStyle: "medium", timeStyle: "medium" }).format(date);
}
