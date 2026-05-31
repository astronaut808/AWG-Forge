// deno-lint-ignore-file no-unused-vars -- classic scripts share UI symbols across files loaded by index.html.
async function runMaintenanceDoctor() {
  maintenanceState.loading.doctor = true;
  openMaintenance("doctor");
  const res = await api("/api/doctor");
  maintenanceState.loading.doctor = false;
  if (!res.ok) {
    openMaintenance("doctor");
    return;
  }
  const payload = await res.json();
  maintenanceState.doctor = payload.results || [];
  maintenanceState.lastRun.doctor = new Date().toLocaleString();
  openMaintenance("doctor");
}

async function runMaintenanceUpdates() {
  maintenanceState.loading.updates = true;
  openMaintenance("updates");
  const res = await api("/api/updates");
  maintenanceState.loading.updates = false;
  if (!res.ok) {
    openMaintenance("updates");
    return;
  }
  const payload = await res.json();
  maintenanceState.updates = payload.updates || {};
  maintenanceState.lastRun.updates = new Date().toLocaleString();
  openMaintenance("updates");
}

async function submitMaintenanceBackup(event) {
  event.preventDefault();
  if (!beginSubmit(event.currentTarget)) return;

  const form = new FormData(event.currentTarget);
  if (form.get("saved") !== "on") {
    showToast("Confirm that you saved the backup password");
    resetSubmit(event.currentTarget);
    return;
  }
  const res = await fetch("/api/backup", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ password: form.get("password") }),
  });
  if (!res.ok) {
    showToast(await errorText(res));
    resetSubmit(event.currentTarget);
    return;
  }
  await downloadBlobResponse(res, "awg-forge-backup.afbackup");
  showToast("Backup downloaded");
  resetSubmit(event.currentTarget);
}

async function submitMaintenanceRestoreVerify(event) {
  event.preventDefault();
  if (!beginSubmit(event.currentTarget)) return;

  const form = new FormData(event.currentTarget);
  const res = await fetch("/api/restore/verify", {
    method: "POST",
    body: form,
  });
  if (!res.ok) {
    showToast(await errorText(res));
    resetSubmit(event.currentTarget);
    return;
  }
  const payload = await res.json();
  maintenanceState.restoreReport = payload.report || null;
  showToast("Backup verified");
  resetSubmit(event.currentTarget);
  openMaintenance("restore");
}

async function repairFirewall(options = {}) {
  if (!state?.apply_enabled) {
    showToast("Firewall repair is unavailable: APPLY_CONFIG=false");
    return;
  }
  if (!confirm("Repair managed firewall rules for enabled tunnels?")) return;
  const res = await api("/api/firewall/repair", { method: "POST", body: {} });
  if (!res.ok) return;
  const payload = await res.json();
  const firewall = payload.firewall || {};
  if (firewall.apply_enabled === false) {
    showToast("Firewall repair skipped: APPLY_CONFIG=false");
  } else {
    showToast("Firewall rules repaired");
  }
  await loadState();
  if (options.after === "maintenance") {
    openMaintenance("firewall");
  } else {
    await openDoctor();
  }
}
