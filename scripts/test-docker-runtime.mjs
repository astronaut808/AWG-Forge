#!/usr/bin/env node
/**
 * Proves encrypted IPv4 traffic through each supported AmneziaWG profile.
 * Everything is deliberately confined to a disposable Docker bridge network.
 */
import { mkdtemp, chmod, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { randomBytes } from "node:crypto";
import { spawn } from "node:child_process";

const image = process.env.IMAGE ?? "awg-forge:local";
const token = `${process.pid}-${randomBytes(12).toString("hex")}`;
const network = `awg-runtime-${token}`;
const volume = `${network}-data`;
const server = `${network}-server`;
const clients = new Set();
const configurations = new Map();
const activeCommands = new Set();
let stopping = false;
let workdir;

const profiles = [
  { id: "awg_legacy_1_0", tunnel: "awg0", port: "51820", subnet: "10.8.0.0/24", serverIP: "10.8.0.1" },
  { id: "awg_1_5", tunnel: "awg15", port: "51825", subnet: "10.15.0.0/24", serverIP: "10.15.0.1" },
  { id: "awg_2_0", tunnel: "awg20", port: "51830", subnet: "10.20.0.0/24", serverIP: "10.20.0.1" },
  { id: "awg_3", tunnel: "awg3", port: "51840", subnet: "10.30.0.0/24", serverIP: "10.30.0.1" },
];

/** Run Docker without relaying potentially sensitive stdout/stderr. */
async function docker(stage, args, { timeout = 60_000, cleanup = false } = {}) {
  if (stopping && !cleanup) throw new Error("runtime smoke interrupted");
  return await new Promise((resolve, reject) => {
    const child = spawn("docker", args, { stdio: ["ignore", "pipe", "pipe"] });
    activeCommands.add(child);
    let stdout = "";
    child.stdout.on("data", (chunk) => { stdout += chunk; });
    child.stderr.resume();
    let forceKill;
    let timedOut = false;
    const timer = setTimeout(() => {
      timedOut = true;
      child.kill("SIGTERM");
      forceKill = setTimeout(() => child.kill("SIGKILL"), 2_000);
    }, timeout);
    child.on("error", () => {
      clearTimeout(timer);
      activeCommands.delete(child);
      reject(new Error(`${stage}: unable to start docker`));
    });
    child.on("close", (code) => {
      clearTimeout(timer);
      clearTimeout(forceKill);
      activeCommands.delete(child);
      if (timedOut) reject(new Error(`${stage}: timeout after ${Math.ceil(timeout / 1000)}s`));
      else if (code === 0) resolve(stdout);
      else reject(new Error(`${stage}: docker exited ${code ?? "without status"}`));
    });
  });
}

async function cleanup() {
  try {
    await Promise.allSettled([
      ...[...clients].map((name) => docker("cleanup client", ["rm", "-f", name], { timeout: 15_000, cleanup: true })),
      docker("cleanup server", ["rm", "-f", server], { timeout: 15_000, cleanup: true }),
    ]);
    await docker("cleanup network", ["network", "rm", network], { timeout: 15_000, cleanup: true }).catch(() => {});
    await docker("cleanup volume", ["volume", "rm", volume], { timeout: 15_000, cleanup: true }).catch(() => {});
    const remaining = (await docker("verify container cleanup", ["ps", "-aq", "--filter", `name=^/${network}-(server|client-[0-9]+)$`], { cleanup: true })).trim();
    const remainingNetwork = (await docker("verify network cleanup", ["network", "ls", "--format", "{{.Name}}", "--filter", `name=^${network}$`], { cleanup: true })).trim();
    const remainingVolume = (await docker("verify volume cleanup", ["volume", "ls", "--format", "{{.Name}}", "--filter", `name=^${volume}$`], { cleanup: true })).trim();
    if (remaining || remainingNetwork || remainingVolume) throw new Error("owned Docker resources remain after cleanup");
  } finally {
    // Private exports must be removed even if Docker cannot verify its resources.
    if (workdir) await rm(workdir, { recursive: true, force: true });
  }
}

function envArgs() {
  return [
    "-e", "WEBUI_HOST=0.0.0.0",
    "-e", "WEBUI_PORT=51821",
    "-e", "PASSWORD=public-ci-fixture",
    "-e", "SESSION_SECRET=public-ci-fixture-session-secret",
    "-e", "LOG_LEVEL=error",
    "-e", "DATABASE_MODE=off",
    "-e", "APPLY_CONFIG=true",
  ];
}

function capabilityArgs() {
  return ["--platform", "linux/amd64", "--cap-add", "NET_ADMIN", "--device", "/dev/net/tun", "--sysctl", "net.ipv4.ip_forward=1"];
}

async function addClient(profile, index) {
  const clientName = `runtime-${index}`;
  const clientID = (await docker(`add client ${profile.id}`, ["exec", server, "awg-forge", "client", "add", clientName, profile.tunnel])).trim();
  if (!/^[a-zA-Z0-9_-]+$/.test(clientID)) throw new Error(`add client ${profile.id}: invalid client identifier`);

  const remoteConfig = `/tmp/runtime-${clientID}.conf`;
  const localConfig = join(workdir, `${profile.tunnel}.conf`);
  await docker(`export config ${profile.id}`, ["exec", server, "sh", "-ec", "umask 077; awg-forge client config \"$1\" > \"$2\"", "sh", clientID, remoteConfig]);
  await docker(`copy config ${profile.id}`, ["cp", `${server}:${remoteConfig}`, localConfig]);
  await chmod(localConfig, 0o600);
  await docker(`remove exported config ${profile.id}`, ["exec", server, "rm", "-f", remoteConfig]);
  return localConfig;
}

async function proveProfile(profile, index) {
  let configFile = configurations.get(profile.id);
  if (!configFile) {
    configFile = await addClient(profile, index);
    configurations.set(profile.id, configFile);
  }
  const client = `${network}-client-${index}`;
  clients.add(client);
  const iface = `awgc${index}`;
  const script = [
    "set -euo pipefail",
    `iface=${iface}`,
    "grep -qx 'nameserver 127.0.0.11' /etc/resolv.conf",
    "getent ahostsv4 server.test >/dev/null",
    "address=$(awk -F ' = ' '$1 == \"Address\" { print $2; exit }' /run/client.conf)",
    "test -n \"$address\"",
    "amneziawg-go \"$iface\" >/tmp/amneziawg-go.log 2>&1 &",
    "runtime_pid=$!",
    "trap 'kill \"$runtime_pid\" 2>/dev/null || true; wait \"$runtime_pid\" 2>/dev/null || true' EXIT",
    "for _ in $(seq 1 30); do test -d \"/sys/class/net/$iface\" && break; sleep 1; done",
    "test -d \"/sys/class/net/$iface\"",
    "awg-quick strip /run/client.conf | awg setconf \"$iface\" /dev/stdin",
    "ip link set up dev \"$iface\"",
    "ip address add \"$address\" dev \"$iface\"",
    `ip route replace ${profile.serverIP}/32 dev "$iface"`,
    "peer_count=$(awg show \"$iface\" peers | awk 'NF { count++ } END { print count + 0 }')",
    "test \"$peer_count\" -eq 1",
    `exec 3<>/dev/tcp/${profile.serverIP}/51821`,
    "printf 'GET / HTTP/1.1\\r\\nHost: server.test\\r\\nConnection: close\\r\\n\\r\\n' >&3",
    "IFS= read -r status <&3",
    "test \"${status%$'\\r'}\" = 'HTTP/1.1 200 OK'",
  ].join("\n");
  await docker(`traffic ${profile.id}`, [
    "run", "--rm", "--name", client,
    "--network", network,
    ...capabilityArgs(),
    "-v", `${configFile}:/run/client.conf:ro`,
    "--entrypoint", "/bin/bash", image, "-ec", script,
  ], { timeout: 90_000 });
  await docker(`handshake ${profile.id}`, [
    "exec", server, "sh", "-ec",
    `awg show ${profile.tunnel} latest-handshakes | awk '$2 > 0 { found = 1 } END { exit !found }'`,
  ]);
  await docker(`firewall ${profile.id}`, ["exec", server, "awg-forge", "firewall", "check"]);
}

async function main() {
  workdir = await mkdtemp(join(tmpdir(), "awg-runtime-smoke-"));
  await chmod(workdir, 0o700);

  await docker("create network", ["network", "create", "--driver", "bridge", network]);
  // Keep root-owned server state in Docker, so host cleanup also works for
  // non-root Linux runners. Only caller-owned client exports touch the host.
  await docker("create volume", ["volume", "create", volume]);
  await docker("start server", [
    "run", "-d", "--name", server, "--network", network, "--network-alias", "server.test",
    ...capabilityArgs(), "-v", `${volume}:/etc/awg-forge`, ...envArgs(),
    "--entrypoint", "/bin/bash", image, "-ec",
    `if [[ ! -f /etc/awg-forge/state.json ]]; then awg-forge init --server-host server.test --external-interface eth0 --profile ${profiles[0].id} --tunnel-name ${profiles[0].tunnel} --listen-port ${profiles[0].port} --ipv4-subnet ${profiles[0].subnet} >/dev/null; fi; exec awg-forge serve`,
  ]);
  await waitForServer();

  for (const profile of profiles.slice(1)) {
    await docker(`create tunnel ${profile.id}`, ["exec", server, "awg-forge", "tunnel", "create", profile.id, profile.tunnel, profile.port, profile.subnet]);
  }
  for (const [index, profile] of profiles.entries()) await proveProfile(profile, index);
  await docker("restart server", ["restart", server]);
  await waitForServer();
  for (const [index, profile] of profiles.entries()) await proveProfile(profile, index);
  process.stdout.write("OK docker runtime: encrypted IPv4 traffic and handshakes for 4 profiles before and after restart\n");
}

async function waitForServer() {
  await docker("wait for server", ["exec", server, "/bin/bash", "-ec",
    "for _ in $(seq 1 30); do if (exec 3<>/dev/tcp/127.0.0.1/51821) 2>/dev/null; then exit 0; fi; sleep 1; done; exit 1",
  ], { timeout: 40_000 });
}

let cleaning;
function cleanupOnce() {
  cleaning ??= cleanup();
  return cleaning;
}
for (const signal of ["SIGINT", "SIGTERM"]) {
  process.once(signal, () => {
    stopping = true;
    process.exitCode = signal === "SIGINT" ? 130 : 143;
    for (const child of activeCommands) child.kill("SIGTERM");
  });
}

try {
  await main();
} catch (error) {
  process.stderr.write(`docker runtime smoke: ${error.message}\n`);
  process.exitCode ||= 1;
} finally {
  try {
    await cleanupOnce();
  } catch {
    process.stderr.write(`docker runtime smoke: cleanup failed; inspect owned network ${network}, volume ${volume}, server ${server}, clients ${[...clients].join(", ")} and private directory ${workdir ?? "not created"}\n`);
    process.exitCode = 1;
  }
}
