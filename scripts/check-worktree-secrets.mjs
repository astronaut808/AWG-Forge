import { execFileSync, spawnSync } from "node:child_process";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";

// Scan current tracked and nonignored untracked files, including uncommitted
// changes. Git history is scanned separately; local data and installs stay out.
const root = fs.realpathSync(execFileSync("git", ["rev-parse", "--show-toplevel"], { encoding: "utf8" }).trim());
const files = execFileSync("git", ["ls-files", "--cached", "--others", "--exclude-standard", "-z"], { cwd: root }).toString("utf8").split("\0");
const work = fs.mkdtempSync(path.join(os.tmpdir(), "awg-worktree-secrets-"));
fs.chmodSync(work, 0o700);
let status = 2;
try {
  let count = 0;
  for (const name of new Set(files.filter(Boolean))) {
    const source = path.resolve(root, name);
    if (!source.startsWith(root + path.sep)) throw new Error("Secret scan path leaves the repository");
    let stat;
    try {
      stat = fs.lstatSync(source);
    } catch (error) {
      if (error.code === "ENOENT") continue; // Deleted tracked file.
      throw error;
    }
    if (stat.isSymbolicLink() || !fs.realpathSync(source).startsWith(root + path.sep)) {
      throw new Error("Secret scan refuses symlinks outside the source snapshot");
    }
    if (!stat.isFile()) continue;
    const target = path.join(work, name);
    fs.mkdirSync(path.dirname(target), { recursive: true, mode: 0o700 });
    fs.copyFileSync(source, target);
    fs.chmodSync(target, 0o600);
    count++;
  }
  console.log(`Scanning ${count} current source files for secrets`);
  const result = spawnSync("gitleaks", ["dir", "--redact", "--no-banner", "--timeout", "120", work], { cwd: root, stdio: "inherit" });
  if (result.error) throw result.error;
  status = result.status ?? 2;
} finally {
  fs.rmSync(work, { recursive: true, force: true });
}
process.exitCode = status;
