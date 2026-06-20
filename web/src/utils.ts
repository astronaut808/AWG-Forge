export function formatBytes(value: number | undefined): string {
  const bytes = Number(value || 0);
  if (bytes >= 1024 * 1024 * 1024) return `${(bytes / 1024 / 1024 / 1024).toFixed(2)} GiB`;
  if (bytes >= 1024 * 1024) return `${(bytes / 1024 / 1024).toFixed(2)} MiB`;
  if (bytes >= 1024) return `${(bytes / 1024).toFixed(2)} KiB`;
  return `${bytes} B`;
}

export function filenameFromDisposition(value: string | null): string {
  const match = String(value || "").match(/filename="([^"]+)"/);
  return match ? match[1] : "";
}

export function downloadResponse(res: Response, fallbackName: string): Promise<void> {
  return res.blob().then((blob) => {
    const url = URL.createObjectURL(blob);
    const link = document.createElement("a");
    link.href = url;
    link.download = filenameFromDisposition(res.headers.get("Content-Disposition")) || fallbackName;
    link.rel = "noopener";
    document.body.appendChild(link);
    link.click();
    link.remove();
    globalThis.setTimeout(() => URL.revokeObjectURL(url), 0);
  });
}

export function downloadConfig(clientID: string): void {
  const link = document.createElement("a");
  link.href = `/clients/config/${encodeURIComponent(clientID)}`;
  link.download = "";
  link.rel = "noopener";
  document.body.appendChild(link);
  link.click();
  link.remove();
}

export function dateOnly(value: string): string {
  if (!value) return "";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "";
  return new Intl.DateTimeFormat(undefined, { day: "2-digit", month: "2-digit", year: "numeric" }).format(date);
}

export function relativeTime(value: string): string {
  if (!value) return "never";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "unknown";
  const seconds = Math.max(0, Math.floor((Date.now() - date.getTime()) / 1000));
  if (seconds < 60) return `${seconds} seconds ago`;
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes} minutes ago`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours} hours ago`;
  return dateOnly(value);
}

export function activeLabel(lastSeenAt: string): "active now" | "seen recently" | "offline" | "never seen" {
  if (!lastSeenAt) return "never seen";
  const date = new Date(lastSeenAt);
  if (Number.isNaN(date.getTime())) return "offline";
  const seconds = Math.max(0, Math.floor((Date.now() - date.getTime()) / 1000));
  if (seconds <= 120) return "active now";
  if (seconds <= 24 * 60 * 60) return "seen recently";
  return "offline";
}

export function expirationValue(value: string): string {
  if (value === "") return "";
  const match = value.match(/^(\d+)([hd])?$/);
  if (!match) return "";
  const amount = Number(match[1]);
  if (!Number.isFinite(amount) || amount <= 0) return "";
  const unit = match[2] || "d";
  const multiplier = unit === "h" ? 60 * 60 * 1000 : 24 * 60 * 60 * 1000;
  return new Date(Date.now() + amount * multiplier).toISOString();
}

export function dateTimeLocalValue(value?: string): string {
  const date = value ? new Date(value) : new Date();
  if (Number.isNaN(date.getTime())) return "";
  const pad = (part: number) => String(part).padStart(2, "0");
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}T${pad(date.getHours())}:${pad(date.getMinutes())}`;
}

export function dateTimeLocalToISO(value: string): string {
  if (!value) return "";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "";
  return date.toISOString();
}

export function profileTitle(profileID: string): string {
  if (profileID === "awg_1_5") return "AmneziaWG 1.5";
  if (profileID === "awg_2_0") return "AmneziaWG 2.0";
  return "AmneziaWG Legacy / 1.0";
}

export function profileDescription(profileID: string): string {
  if (profileID === "awg_2_0") return "Create AWG 2.0 tunnels. Use .conf import for production clients.";
  if (profileID === "awg_1_5") return "Create AWG 1.5 tunnels with I1-I5 profile parameters.";
  return "Create and manage AmneziaWG Legacy / 1.0 tunnels.";
}

export function classNames(...parts: Array<string | false | null | undefined>): string {
  return parts.filter(Boolean).join(" ");
}
