import type { Profile } from "./types";
import type { TrafficLimitPeriod, TrafficLimitUnit } from "./ui-types";
import { dateTimeLocalToISO, expirationValue } from "./utils";

export function field(form: HTMLFormElement, name: string): string {
  return String(new FormData(form).get(name) || "");
}

export function defaultCreateProfile(profiles: Profile[]): Profile | undefined {
  const preferred = profiles.find((profile) => profile.id === "awg_2_0" && profile.available);
  if (preferred) return preferred;
  return profiles.find((profile) => profile.available);
}

export function versionLabel(version: string): string {
  const trimmed = version.trim();
  if (!trimmed || trimmed === "dev") return "dev";
  return trimmed.startsWith("v") ? trimmed : `v${trimmed}`;
}

export function mtuValue(form: HTMLFormElement): number {
  const mode = field(form, "mtu_mode");
  if (mode === "custom") return Number(field(form, "mtu_custom"));
  return Number(mode || 0);
}

export function expirationFromForm(form: HTMLFormElement, current = ""): string {
  const mode = field(form, "expires_mode");
  if (mode === "keep") return current;
  if (mode === "custom") return dateTimeLocalToISO(field(form, "expires_custom"));
  return expirationValue(mode);
}

export function trafficLimitInitial(value: number | null | undefined): { value: string; unit: TrafficLimitUnit } {
  if (!value) return { value: "", unit: "gib" };
  const units: Array<{ unit: TrafficLimitUnit; bytes: number }> = [
    { unit: "tib", bytes: 1024 * 1024 * 1024 * 1024 },
    { unit: "gib", bytes: 1024 * 1024 * 1024 },
    { unit: "mib", bytes: 1024 * 1024 },
  ];
  const selected = units.find((item) => value >= item.bytes && value % item.bytes === 0) || units.find((item) => value >= item.bytes) || units[2];
  return { value: String(Number((value / selected.bytes).toFixed(3))), unit: selected.unit };
}

export function trafficLimitFromForm(form: HTMLFormElement, invalidMessage: string): { bytes: number | null; period: TrafficLimitPeriod } {
  if (field(form, "traffic_limit_mode") !== "limit") return { bytes: null, period: "lifetime" };
  const raw = field(form, "traffic_limit_value").replace(",", ".").trim();
  const value = Number(raw);
  if (!raw || !Number.isFinite(value) || value <= 0) throw new Error(invalidMessage);
  const multiplier = trafficLimitUnitBytes(field(form, "traffic_limit_unit"));
  if (multiplier <= 0) throw new Error(invalidMessage);
  const bytes = Math.round(value * multiplier);
  if (bytes <= 0) throw new Error(invalidMessage);
  const period = field(form, "traffic_limit_period") as TrafficLimitPeriod;
  if (period !== "lifetime" && period !== "rolling_30d") throw new Error(invalidMessage);
  return { bytes, period };
}

export function trafficLimitUnitBytes(unit: string): number {
  switch (unit) {
    case "mib":
      return 1024 * 1024;
    case "tib":
      return 1024 * 1024 * 1024 * 1024;
    case "gib":
      return 1024 * 1024 * 1024;
  }
  return 0;
}
