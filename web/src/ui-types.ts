import type { Client, Profile, Tunnel } from "./types";

export type Modal =
  | { kind: "create-tunnel"; profile: Profile }
  | { kind: "settings"; tunnel: Tunnel }
  | { kind: "protocol"; tunnel: Tunnel }
  | { kind: "create-client"; tunnelID?: string; origin?: "global" }
  | { kind: "client-settings"; tunnel: Tunnel; client: Client }
  | { kind: "client-config"; client: Client }
  | { kind: "delete-tunnel"; tunnel: Tunnel }
  | { kind: "maintenance" };

export type MaintenanceTab = "overview" | "doctor" | "warp" | "backup" | "support" | "logs" | "traffic";
export type QRImportMode = "amneziavpn" | "amneziawg";
export type ExpandedQR = { mode: QRImportMode; chunk: number };
export type TrafficLimitUnit = "mib" | "gib" | "tib";
export type TrafficLimitPeriod = "lifetime" | "rolling_30d";
export type LoadResult = "ok" | "unauthorized" | "failed";
export type PortSelectionMode = "automatic" | "manual";
export type RunActionOptions = { reload?: boolean; close?: boolean; errorMode?: "toast" | "inline" };
export type RunAction = (label: string, fn: () => Promise<unknown>, options?: RunActionOptions) => Promise<void>;
