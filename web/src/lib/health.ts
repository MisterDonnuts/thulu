import { locale, t } from "../i18n";
import { formatSeconds } from "./time";
import { statGroupHTML, statTileHTML, type StatTone } from "./ui";

export type HealthTone = "ok" | "warn" | "err";

export interface HealthCheck {
  status: string;
  latency?: string;
  error?: string;
  name?: string;
}

export interface HealthResponse {
  status: string;
  uptime?: string;
  uptime_seconds?: number;
  checks?: HealthCheck[] | Record<string, HealthCheck>;
  system?: Record<string, unknown>;
}

export interface HealthTile {
  name: string;
  value: string;
  detail: string;
  tone: HealthTone;
}

export interface HealthView {
  tiles: HealthTile[];
  system: HealthTile[];
  hasSystem: boolean;
}

export function fmtBytes(b: number, loc: string = locale): string {
  const one = (n: number) =>
    n.toLocaleString(loc, { minimumFractionDigits: 1, maximumFractionDigits: 1 });
  if (b >= 1e9) return `${one(b / 1e9)} GB`;
  if (b >= 1e6) return `${one(b / 1e6)} MB`;
  if (b >= 1e3) return `${one(b / 1e3)} KB`;
  return `${b} B`;
}

function firstSet(...values: Array<string | undefined>): string {
  return values.find(Boolean) ?? "";
}

export function healthTone(status: string): HealthTone {
  if (status === "ok") return "ok";
  if (status === "warn") return "warn";
  return "err";
}

export function statusLabel(status: string): string {
  switch (status) {
    case "ok":
    case "healthy":
      return t("lib.health.status.ok");
    case "warn":
      return t("lib.health.status.warn");
    case "degraded":
      return t("lib.health.status.degraded");
    case "fail":
      return t("lib.health.status.fail");
    default:
      return status;
  }
}

// Stores the daemon registers a db check for (internal/daemon); one literal key each so the i18n
// catalog guard sees them in use.
function storeLabel(name: string): string {
  switch (name) {
    case "usage":
      return t("lib.health.stores.usage");
    case "projects":
      return t("lib.health.stores.projects");
    case "tasks":
      return t("lib.health.stores.tasks");
    case "settings":
      return t("lib.health.stores.settings");
    case "sandbox":
      return t("lib.health.stores.sandbox");
    case "secrets":
      return t("lib.health.stores.secrets");
    case "credentials":
      return t("lib.health.stores.credentials");
    case "chat":
      return t("lib.health.stores.chat");
    case "chatruntime":
      return t("lib.health.stores.chatruntime");
    case "auth":
      return t("lib.health.stores.auth");
    case "audit":
      return t("lib.health.stores.audit");
    case "webhooks":
      return t("lib.health.stores.webhooks");
    case "stages":
      return t("lib.health.stores.stages");
    case "automations":
      return t("lib.health.stores.automations");
    case "evidence":
      return t("lib.health.stores.evidence");
    default:
      return name;
  }
}

export function checkLabel(name: string): string {
  if (name === "docker") return t("lib.health.checks.docker");
  if (name === "disk") return t("lib.health.checks.disk");
  if (name.startsWith("db:")) return t("lib.health.checks.db", { name: storeLabel(name.slice(3)) });
  return name;
}

export function fmtGiB(n: number): string {
  return `${n.toLocaleString(locale, { minimumFractionDigits: 1, maximumFractionDigits: 1 })} GiB`;
}

function systemEntry(key: string, v: unknown): { name: string; value: string } {
  switch (key) {
    case "go_version":
      return { name: t("lib.health.system.go_version"), value: String(v) };
    case "goroutines":
      return { name: t("lib.health.system.goroutines"), value: String(v) };
    case "disk_free_gb":
      return {
        name: t("lib.health.system.disk_free"),
        value: typeof v === "number" ? fmtGiB(v) : String(v),
      };
  }
  const value =
    typeof v === "number" && key.toLowerCase().includes("mem") ? fmtBytes(v) : String(v);
  return { name: key, value };
}

export function healthBadge(status: string): { label: string; tone: "ok" | "danger" } {
  return {
    label: statusLabel(status),
    tone: status === "ok" || status === "healthy" ? "ok" : "danger",
  };
}

// Older daemons send `checks` as an array with the name inside each entry; both shapes must be
// read.
export function healthView(h: HealthResponse): HealthView {
  const tiles: HealthTile[] = [];

  const up = typeof h.uptime_seconds === "number" ? formatSeconds(h.uptime_seconds) : h.uptime;
  if (up) tiles.push({ name: t("lib.health.tiles.uptime"), value: up, detail: "", tone: "ok" });

  const freeGiB = h.system?.disk_free_gb;
  const diskDetail =
    typeof freeGiB === "number" ? t("lib.health.disk_free_detail", { size: fmtGiB(freeGiB) }) : "";
  const detailOf = (name: string, c: HealthCheck): string => {
    if (name !== "disk" || !diskDetail) return firstSet(c.error, c.latency);
    return c.error ? `${c.error} (${diskDetail})` : diskDetail;
  };

  const checks = h.checks;
  if (checks && typeof checks === "object" && !Array.isArray(checks)) {
    for (const [name, c] of Object.entries(checks)) {
      tiles.push({
        name: checkLabel(name),
        value: statusLabel(c.status),
        detail: detailOf(name, c),
        tone: healthTone(c.status),
      });
    }
  } else if (Array.isArray(checks)) {
    for (const c of checks) {
      const name = firstSet(c.name, "?");
      tiles.push({
        name: checkLabel(name),
        value: statusLabel(c.status),
        detail: detailOf(name, c),
        tone: healthTone(c.status),
      });
    }
  }

  const diskShown = diskDetail !== "" && tiles.some((tile) => tile.name === checkLabel("disk"));
  const hasSystem = Boolean(h.system) && typeof h.system === "object";
  const system: HealthTile[] = [];
  if (hasSystem) {
    for (const [k, v] of Object.entries(h.system as Record<string, unknown>)) {
      if (k === "disk_free_gb" && diskShown) continue;
      const { name, value } = systemEntry(k, v);
      system.push({ name, value, detail: "", tone: "ok" });
    }
  }

  return { tiles, system, hasSystem };
}

export interface InfoResponse {
  edition?: string;
  version?: string;
  licensee?: string;
  features?: string[];
  license_expires_at?: string;
}

export function isEE(info: InfoResponse): boolean {
  return (info.edition ?? "").toLowerCase() === "ee";
}

export function daysUntil(iso: string, now: number = Date.now()): number | null {
  const ms = new Date(iso).getTime();
  if (Number.isNaN(ms)) return null;
  return Math.ceil((ms - now) / 86_400_000);
}

export interface ExpiryState {
  days: number | null;
  expired: boolean;
  warn: boolean;
}

export function expiryState(iso: string, now: number = Date.now()): ExpiryState {
  const days = daysUntil(iso, now);
  if (days === null) return { days: null, expired: false, warn: false };
  return { days, expired: days < 0, warn: days <= 30 };
}

export function healthStatTone(tone: HealthTone): StatTone {
  return tone === "err" ? "danger" : tone;
}

export function healthTilesHTML(tiles: HealthTile[], label: string): string {
  return statGroupHTML(
    tiles.map((tile) =>
      statTileHTML({
        label: tile.name,
        value: tile.value,
        sub: tile.detail || undefined,
        tone: healthStatTone(tile.tone),
        size: "sm",
      }),
    ),
    { label },
  );
}
