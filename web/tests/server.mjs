import { execFileSync, spawn } from "node:child_process";
import { randomBytes } from "node:crypto";
import { mkdtempSync, realpathSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

const directory = realpathSync(mkdtempSync(join(tmpdir(), "awg-forge-ui-")));
const binary = join(directory, "awg-forge");
// Do not inherit deployment settings or point the tests at an existing data directory.
const env = {
  PATH: process.env.PATH,
  CONFIG_DIR: join(directory, "data"),
  WEBUI_HOST: "127.0.0.1",
  WEBUI_PORT: process.env.WEBUI_PORT || "51924",
  PASSWORD: "browser-test-only",
  SESSION_SECRET: randomBytes(32).toString("hex"),
  APPLY_CONFIG: "false",
  DATABASE_MODE: "sqlite",
  LOG_LEVEL: "error",
};
let server = null;
for (const signal of ["SIGINT", "SIGTERM"]) {
  process.on(signal, () => {
    if (server) server.kill(signal);
    else process.exit(1);
  });
}
process.on("exit", () => rmSync(directory, { recursive: true, force: true }));

execFileSync("go", [
  "build", "-ldflags=-X github.com/astronaut808/awg-forge/internal/buildinfo.AWG3Runtime=true",
  "-o", binary, "./cmd/awg-forge",
], { stdio: "inherit" });
execFileSync(binary, [
  "init", "--server-host", "127.0.0.1", "--external-interface", "eth0",
  "--profile", "awg_2_0", "--tunnel-name", "awg20", "--listen-port", "51820",
  "--ipv4-subnet", "10.20.0.0/24",
], { env, stdio: "inherit" });
execFileSync(binary, ["db", "migrate"], { env, stdio: "inherit" });
server = spawn(binary, ["serve"], { env, stdio: "inherit" });
server.on("error", (error) => {
  console.error(error.message);
  process.exitCode = 1;
});
server.on("exit", (code, signal) => {
  process.exitCode = code ?? (signal === "SIGTERM" || signal === "SIGINT" ? 0 : 1);
});
