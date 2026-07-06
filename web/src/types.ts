export type Level = "ok" | "warn" | "fail" | "bad" | "neutral";

export type Profile = {
  id: string;
  tab: string;
  label: string;
  available: boolean;
  suggested_name: string;
  suggested_port: number;
  suggested_subnet: string;
};

export type ProtocolParam = {
  key: string;
  value: string;
};

export type FirewallSummary = {
  level: Level;
  label: string;
  message?: string;
};

export type ClientRuntime = {
  present: boolean;
  latest_handshake: number;
  last_seen_at: string;
  rx_bytes: number;
  tx_bytes: number;
};

export type TrafficSummaryRow = {
  tunnel_id: string;
  client_id: string;
  rx_total: number;
  tx_total: number;
  rx_today: number;
  tx_today: number;
  rx_7d: number;
  tx_7d: number;
  rx_30d: number;
  tx_30d: number;
  limit_bytes: number | null;
};

export type TrafficSummary = {
  enabled: boolean;
  rows: TrafficSummaryRow[];
};

export type ClientTraffic = {
  enabled: boolean;
  rx_total: number;
  tx_total: number;
  limit_bytes: number | null;
  exceeded: boolean;
};

export type Client = {
  id: string;
  tunnel_id: string;
  name: string;
  notes: string;
  enabled: boolean;
  active: boolean;
  expired: boolean;
  address: string;
  revision: number;
  needs_new_config: boolean;
  ever_connected: boolean;
  last_seen_at: string;
  expires_at: string;
  runtime: ClientRuntime;
  traffic: ClientTraffic;
  created_at: string;
  updated_at: string;
};

export type TunnelStatus = {
  up: boolean;
  apply_enabled: boolean;
  last_render: string;
  last_apply: string;
  last_error: string;
  firewall: FirewallSummary;
  stale_clients: number;
};

export type Tunnel = {
  id: string;
  name: string;
  interface: string;
  egress_mode: "wan" | "warp" | string;
  enabled: boolean;
  listen_port: number;
  server_host: string;
  address: string;
  subnet: string;
  dns: string;
  allowed_ips: string;
  keepalive: number;
  mtu: number;
  profile: string;
  revision: number;
  params: ProtocolParam[];
  clients: Client[];
  status: TunnelStatus;
};

export type WarpSummary = {
  configured: boolean;
  registered: boolean;
  client_id: string;
  license_set: boolean;
  interface_name: string;
  endpoint: string;
  address_v4: string;
  enabled_tunnel_count: number;
  last_apply_error?: string;
};

export type BuildInfo = {
  version: string;
  commit: string;
  amneziawg_go_ref: string;
  amneziawg_tools_ref: string;
  amneziawg_go_repo: string;
  amneziawg_tools_repo: string;
  amneziawg_update_mode: string;
};

export type DatabaseSummary = {
  mode: string;
  enabled: boolean;
};

export type AppState = {
  authenticated: boolean;
  apply_enabled: boolean;
  server_host: string;
  warp: WarpSummary;
  database: DatabaseSummary;
  build: BuildInfo;
  published_udp_ports: number[];
  profiles: Profile[];
  tunnels: Tunnel[];
};

export type DoctorResult = {
  level: Level;
  area: string;
  message: string;
};

export type FirewallRuleResult = {
  tunnel: string;
  name: string;
  status: string;
  message: string;
  rule: string;
};

export type FirewallReport = {
  apply_enabled: boolean;
  results: FirewallRuleResult[];
};

export type UpdateComponent = {
  name: string;
  current_ref: string;
  latest_ref: string;
  status: string;
  url: string;
};

export type UpdatesReport = {
  components: UpdateComponent[];
};

export type AuditEvent = {
  time: string;
  level: string;
  event: string;
  message: string;
  error?: string;
  fields?: Record<string, unknown>;
};

export type RestoreReport = {
  format: string;
  schema: number;
  created_at: string;
  build: Record<string, string>;
  files: number;
  total_bytes: number;
  tunnels: Array<{
    id: string;
    name: string;
    profile: string;
    interface: string;
    subnet: string;
    listen_port: number;
    clients: number;
  }>;
  client_count: number;
  server_host: string;
};
