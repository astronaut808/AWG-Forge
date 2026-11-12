import * as api from "./api";
import type { Messages } from "./i18n";
import type { Level } from "./types";

export function errorMessage(err: unknown, fallback = "request failed"): string {
  return err instanceof Error ? err.message : fallback;
}

export function actionErrorMessage(err: unknown, m: Messages): string {
  if (err instanceof api.APIError && err.status === 409 && err.code === "traffic_limit_exceeded") {
    return m.forms.trafficLimitEnableBlocked;
  }
  return errorMessage(err, m.common.requestFailed);
}

export function backupErrorMessage(err: unknown, m: Messages): string {
  if (!(err instanceof api.APIError)) return m.common.requestFailed;
  if (err.code === "backup_verification_failed") return m.backup.verifyFailed;
  if (err.code === "backup_create_failed") return m.backup.createFailed;
  if (err.status === 400 || err.status === 413) return m.backup.uploadFailed;
  return m.common.requestFailed;
}

export function toneFromLevel(level: Level): string {
  if (level === "ok") return "ok";
  if (level === "warn") return "warn";
  if (level === "neutral") return "neutral";
  if (level === "bad" || level === "fail") return "bad";
  return "muted";
}
