import type {
  AppState,
  AuditEvent,
  DoctorResult,
  FirewallReport,
  RestoreReport,
  TrafficSummary,
  UpdatesReport,
  WarpSummary,
} from "./types";

export class APIError extends Error {
  status: number;

  constructor(status: number, message: string) {
    super(message);
    this.status = status;
  }
}

type RequestOptions = {
  method?: string;
  body?: unknown;
  idempotencyKey?: string;
};

export function newIdempotencyKey(): string {
  if (globalThis.crypto?.randomUUID) return globalThis.crypto.randomUUID();
  return `${Date.now()}-${Math.random().toString(16).slice(2)}`;
}

async function request<T>(url: string, options: RequestOptions = {}): Promise<T> {
  const init: RequestInit = {
    method: options.method || "GET",
    headers: {},
  };
  const headers = init.headers as Record<string, string>;
  if (options.idempotencyKey) headers["Idempotency-Key"] = options.idempotencyKey;
  if (options.body !== undefined) {
    headers["Content-Type"] = "application/json";
    init.body = JSON.stringify(options.body);
  }

  const res = await fetch(url, init);
  if (!res.ok) throw new APIError(res.status, await errorText(res));
  if (res.status === 204) return undefined as T;
  return (await res.json()) as T;
}

async function errorText(res: Response): Promise<string> {
  try {
    const data = await res.json();
    return String(data.error || `Request failed: ${res.status}`);
  } catch {
    return `Request failed: ${res.status}`;
  }
}

export function login(password: string): Promise<{ ok: true }> {
  return request("/api/login", { method: "POST", body: { password } });
}

export function logout(): Promise<{ ok: true }> {
  return request("/api/logout", { method: "POST", body: {} });
}

export function state(): Promise<AppState> {
  return request("/api/state");
}

export function createTunnel(body: { profile: string; name: string; port: number; subnet: string; egress_mode: string }) {
  return request("/api/tunnels", { method: "POST", body, idempotencyKey: newIdempotencyKey() });
}

export function updateTunnel(id: string, body: Record<string, unknown>) {
  return request(`/api/tunnels/${encodeURIComponent(id)}/settings`, {
    method: "PATCH",
    body,
    idempotencyKey: newIdempotencyKey(),
  });
}

export function deleteTunnel(id: string) {
  return request(`/api/tunnels/${encodeURIComponent(id)}/delete`, {
    method: "DELETE",
    idempotencyKey: newIdempotencyKey(),
  });
}

export function restartTunnel(id: string) {
  return request(`/api/tunnels/${encodeURIComponent(id)}/restart`, {
    method: "POST",
    body: {},
    idempotencyKey: newIdempotencyKey(),
  });
}

export function updateProtocol(id: string, profile: string, params: Record<string, string>) {
  return request(`/api/tunnels/${encodeURIComponent(id)}/protocol`, {
    method: "PATCH",
    body: { profile, params },
    idempotencyKey: newIdempotencyKey(),
  });
}

export function regenerateProtocol(id: string, profile: string) {
  return request(`/api/tunnels/${encodeURIComponent(id)}/regenerate`, {
    method: "POST",
    body: { profile },
    idempotencyKey: newIdempotencyKey(),
  });
}

export function createClient(tunnelID: string, name: string, expiresAt: string, trafficLimitBytes: number | null) {
  return request<{ client: { id: string } }>("/api/clients", {
    method: "POST",
    body: { tunnel_id: tunnelID, name, expires_at: expiresAt, traffic_limit_bytes: trafficLimitBytes },
    idempotencyKey: newIdempotencyKey(),
  });
}

export function updateClient(id: string, body: { name: string; notes: string; expires_at: string }) {
  return request(`/api/clients/${encodeURIComponent(id)}/settings`, {
    method: "PATCH",
    body,
    idempotencyKey: newIdempotencyKey(),
  });
}

export function updateClientTrafficLimit(id: string, limitBytes: number | null) {
  return request(`/api/clients/${encodeURIComponent(id)}/traffic-limit`, {
    method: "PATCH",
    body: { limit_bytes: limitBytes },
    idempotencyKey: newIdempotencyKey(),
  });
}

export function setClientEnabled(id: string, enabled: boolean) {
  return request(`/api/clients/${encodeURIComponent(id)}/${enabled ? "enable" : "disable"}`, {
    method: "POST",
    body: {},
    idempotencyKey: newIdempotencyKey(),
  });
}

export function deleteClient(id: string) {
  return request(`/api/clients/${encodeURIComponent(id)}/delete`, {
    method: "DELETE",
    idempotencyKey: newIdempotencyKey(),
  });
}

export function clientImportKey(id: string) {
  return request<{ import_key: string; warning: string }>(`/api/clients/${encodeURIComponent(id)}/import-key`, {
    method: "POST",
    body: {},
    idempotencyKey: newIdempotencyKey(),
  });
}

export function clientQRCodeURL(id: string): string {
  return `/api/clients/${encodeURIComponent(id)}/qr`;
}

export function clientAmneziaVPNQRCodeURL(id: string, chunk = 0): string {
  return `/api/clients/${encodeURIComponent(id)}/amnezia-vpn-qr?chunk=${chunk}`;
}

export function clientAmneziaVPNQRSeries(id: string): Promise<{ chunks: number }> {
  return request(`/api/clients/${encodeURIComponent(id)}/amnezia-vpn-qr-series`);
}

export function doctor(): Promise<{ results: DoctorResult[] }> {
  return request("/api/doctor");
}

export function firewallRepair(): Promise<{ firewall: FirewallReport }> {
  return request("/api/firewall/repair", { method: "POST", body: {}, idempotencyKey: newIdempotencyKey() });
}

export function updates(): Promise<{ updates: UpdatesReport }> {
  return request("/api/updates");
}

export function auditLog(): Promise<{ events: AuditEvent[] }> {
  return request("/api/audit-log?tail=100");
}

export function trafficSummary(): Promise<TrafficSummary> {
  return request("/api/traffic-summary");
}

export function registerWarp(): Promise<{ warp: WarpSummary }> {
  return request("/api/warp/register", { method: "POST", body: {}, idempotencyKey: newIdempotencyKey() });
}

export function restartWarp(): Promise<{ ok: true }> {
  return request("/api/warp/restart", { method: "POST", body: {}, idempotencyKey: newIdempotencyKey() });
}

export function deleteWarp(): Promise<{ ok: true }> {
  return request("/api/warp", { method: "DELETE", idempotencyKey: newIdempotencyKey() });
}

export function importWarp(config: string): Promise<{ warp: WarpSummary }> {
  return request("/api/warp/import", {
    method: "POST",
    body: { config },
    idempotencyKey: newIdempotencyKey(),
  });
}

export async function backup(password: string): Promise<Response> {
  const res = await fetch("/api/backup", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ password }),
  });
  if (!res.ok) throw new APIError(res.status, await errorText(res));
  return res;
}

export async function restoreVerify(file: File, password: string): Promise<{ report: RestoreReport }> {
  const form = new FormData();
  form.set("backup", file);
  form.set("password", password);
  const res = await fetch("/api/restore/verify", { method: "POST", body: form });
  if (!res.ok) throw new APIError(res.status, await errorText(res));
  return (await res.json()) as { report: RestoreReport };
}
