import { t } from "../i18n";
import { relTime } from "./activity";
import { esc, type BadgeTone } from "./ui";

export interface EngineInfo {
  name: string;
  label: string;
  provider: string;
  models?: string[];
  is_default?: boolean;
  providers?: string[];
  auth_header?: boolean;
  // True when OCTOPUS_ALLOW_HOST_LLM_CREDENTIAL=1 lets a run with no profile use the daemon host's
  // credential; otherwise an engine without a default profile runs nothing.
  host_fallback?: boolean;
  // Model a named profile without a model runs, regardless of what the default profile pins.
  fallback_model?: string;
}

export interface LLMProfile {
  id: string;
  name: string;
  engine: string;
  model?: string;
  effort?: string;
  base_url?: string;
  org_id?: string;
  provider?: string;
  auth_header?: string;
  region?: string;
  project_id?: string;
  small_fast_model?: string;
  key_hint?: string;
  has_key?: boolean;
  is_default?: boolean;
  bound_agents?: string[] | null;
  last_used_at?: string;
  key_state?: "valid" | "invalid" | "unreachable" | "unchecked";
  key_checked_at?: string;
  key_reason?: KeyReason;
  key_status?: number;
  key_host?: string;
  refused_status?: number;
  refused_at?: string;
  federation?: FederationSettings;
}

export interface FederationSettings {
  federation_rule_id: string;
  organization_id: string;
  service_account_id: string;
  workspace_id?: string;
}

// Codes the daemon stores; the page translates them, so this list must match the daemon's reasons.
export type KeyReason =
  | "no_upstream"
  | "not_checkable"
  | "endpoint_refused"
  | "no_answer"
  | "rejected"
  | "unexpected";

export interface AgentLite {
  name: string;
  engine?: string;
}

export function agentsOnDefault(
  engineName: string,
  profiles: LLMProfile[],
  agents: AgentLite[],
  engines: EngineInfo[],
): string[] {
  const bound = new Set(profiles.flatMap((p) => p.bound_agents ?? []));
  const fallback = engines.find((e) => e.is_default)?.name ?? "";
  return agents
    .filter((a) => {
      const engine = [a.engine, fallback].find(Boolean) ?? "";
      return !bound.has(a.name) && engine === engineName;
    })
    .map((a) => a.name);
}

export function usedByText(names: string[], isDefault: boolean): string {
  if (names.length === 0)
    return isDefault
      ? t("lib.llm_profiles.used_by.none_default")
      : t("lib.llm_profiles.used_by.none_named");
  const shown = names.slice(0, 5).join(", ");
  return names.length > 5
    ? t("lib.llm_profiles.used_by.some_more", { names: shown, count: names.length - 5 })
    : t("lib.llm_profiles.used_by.some", { names: shown });
}

const EFFORT_LABELS: Record<string, string> = {
  low: t("lib.llm_profiles.effort.low"),
  medium: t("lib.llm_profiles.effort.medium"),
  high: t("lib.llm_profiles.effort.high"),
};

export function effortLabel(effort: string): string {
  return EFFORT_LABELS[effort] ?? effort;
}

export function noDefaultText(engine: EngineInfo, waiting: number): string {
  if (!engine.host_fallback) {
    return waiting
      ? t("lib.llm_profiles.no_default.blocked_waiting", { engine: engine.label, count: waiting })
      : t("lib.llm_profiles.no_default.blocked", { engine: engine.label });
  }
  return waiting
    ? t("lib.llm_profiles.no_default.host_waiting", { engine: engine.label, count: waiting })
    : t("lib.llm_profiles.no_default.host", { engine: engine.label });
}

export interface KeyBadge {
  label: string;
  tone: Extract<BadgeTone, "ok" | "danger" | "warn" | "muted">;
  title: string;
}

export function keyBadge(p: LLMProfile): KeyBadge | null {
  if (p.federation && !p.refused_status) return null;
  if (p.refused_status) {
    return { label: t("lib.llm_profiles.key.refused"), tone: "danger", title: refusedText(p) };
  }
  switch (p.key_state) {
    case "valid":
      return { label: t("lib.llm_profiles.key.valid"), tone: "ok", title: "" };
    case "invalid":
      return { label: t("lib.llm_profiles.key.invalid"), tone: "danger", title: keyNoteText(p) };
    case "unreachable":
      return { label: t("lib.llm_profiles.key.unreachable"), tone: "warn", title: keyNoteText(p) };
    case "unchecked":
      return { label: t("lib.llm_profiles.key.unchecked"), tone: "muted", title: keyNoteText(p) };
    case undefined:
      return null;
  }
}

export function keyNoteTone(p: LLMProfile): "danger" | "warn" {
  return p.refused_status || p.key_state === "invalid" ? "danger" : "warn";
}

export function canRecheck(p: LLMProfile): boolean {
  return !!p.has_key && p.key_reason !== "not_checkable";
}

export function keyTrouble(
  p: LLMProfile | undefined,
): { tone: "danger" | "warn"; text: string } | null {
  if (!p) return null;
  if (p.refused_status) return { tone: "danger", text: refusedText(p) };
  if (p.key_state === "invalid" || p.key_state === "unreachable")
    return { tone: keyNoteTone(p), text: keyNoteText(p) };
  return null;
}

export function keyRejected(p: LLMProfile | undefined): boolean {
  return keyTrouble(p)?.tone === "danger";
}

export function keyNoteText(p: LLMProfile): string {
  const status = p.key_status ?? 0;
  switch (p.key_reason) {
    case "no_upstream":
      return t("lib.llm_profiles.key.note.no_upstream");
    case "not_checkable":
      return t("lib.llm_profiles.key.note.not_checkable");
    case "endpoint_refused":
      return t("lib.llm_profiles.key.note.endpoint_refused");
    case "no_answer":
      return p.key_host
        ? t("lib.llm_profiles.key.note.no_answer", { host: p.key_host })
        : t("lib.llm_profiles.key.note.no_answer_unnamed");
    case "rejected":
      return t("lib.llm_profiles.key.note.rejected", { status });
    case "unexpected":
      return t("lib.llm_profiles.key.note.unexpected", { status });
    // A newer daemon may store a reason this page does not know yet: show nothing rather than the
    // code.
    case undefined:
    default:
      return "";
  }
}

export function refusedText(p: LLMProfile): string {
  if (!p.refused_status) return "";
  const at = p.refused_at;
  return at && !at.startsWith("0001")
    ? t("lib.llm_profiles.key.refused_when", { status: p.refused_status, when: relTime(at) })
    : t("lib.llm_profiles.key.refused_note", { status: p.refused_status });
}

export function profileModelText(model: string | undefined, fallback: string | undefined): string {
  if (model) return model;
  return fallback
    ? t("lib.llm_profiles.model.engine_default", { model: fallback })
    : t("lib.llm_profiles.model.cli_default");
}

// A timestamp starting with year 0001 is Go's zero time, not a real run.
export function profileMeta(p: LLMProfile, fallbackModel?: string): string[] {
  const meta: string[] = [
    p.model || fallbackModel
      ? t("lib.llm_profiles.meta.model", { model: esc(profileModelText(p.model, fallbackModel)) })
      : t("lib.llm_profiles.meta.model_default"),
  ];
  if (p.effort)
    meta.push(t("lib.llm_profiles.meta.effort", { effort: esc(effortLabel(p.effort)) }));
  if (isCloudProvider(p.provider)) {
    const where = [p.project_id, p.region]
      .filter(Boolean)
      .map((x) => esc(x ?? ""))
      .join(" / ");
    meta.push(`${esc(providerLabel(p.provider ?? ""))}${where ? ` ${where}` : ""}`);
    if (!p.has_key) meta.push(t("lib.llm_profiles.meta.workload_identity"));
  }
  if (p.federation)
    meta.push(
      t("lib.llm_profiles.meta.federation_html", { rule: esc(p.federation.federation_rule_id) }),
    );
  if (p.key_hint) meta.push(t("lib.llm_profiles.meta.key_html", { hint: esc(p.key_hint) }));
  if (p.base_url) meta.push(t("lib.llm_profiles.meta.base", { url: esc(p.base_url) }));
  if (p.auth_header === "bearer") meta.push(t("lib.llm_profiles.meta.bearer"));
  if (p.org_id) meta.push(t("lib.llm_profiles.meta.org", { org: esc(p.org_id) }));
  const last = p.last_used_at;
  meta.push(
    last && !last.startsWith("0001")
      ? t("lib.llm_profiles.meta.last_used", { when: esc(relTime(last)) })
      : t("lib.llm_profiles.meta.never_used"),
  );
  return meta;
}

export function isCloudProvider(p: string | undefined): boolean {
  return p === "bedrock" || p === "vertex";
}

const PROVIDER_LABELS: Record<string, string> = {
  anthropic: t("lib.llm_profiles.providers.anthropic"),
  openai: t("lib.llm_profiles.providers.openai"),
  "openai-compatible": t("lib.llm_profiles.providers.openai-compatible"),
  bedrock: t("lib.llm_profiles.providers.bedrock"),
  vertex: t("lib.llm_profiles.providers.vertex"),
};

export function providerLabel(p: string): string {
  return PROVIDER_LABELS[p] ?? p;
}

export function engineLabel(engines: EngineInfo[], name: string): string {
  return engines.find((e) => e.name === name)?.label ?? name;
}

export function profileModalTitle(
  label: string,
  editing: LLMProfile | null,
  asDefault: boolean,
  hasDefault: boolean,
): string {
  if (editing) return t("lib.llm_profiles.modal.edit", { name: editing.name });
  return asDefault || !hasDefault
    ? t("lib.llm_profiles.modal.default", { engine: label })
    : t("lib.llm_profiles.modal.new", { engine: label });
}

export function prefilledProfileName(
  engineName: string,
  profiles: LLMProfile[],
  editing: LLMProfile | null,
  asDefault: boolean,
  hasDefault: boolean,
): string {
  if (editing) return editing.name;
  const suggested = `default-${engineName}`;
  const free = !profiles.some((x) => x.name === suggested);
  return (asDefault || !hasDefault) && free ? suggested : "";
}

export function modelPlaceholder(models: string[]): string {
  return models.length
    ? t("lib.llm_profiles.placeholders.models", { models: models.join(", ") })
    : t("lib.llm_profiles.placeholders.model_empty");
}

export function keyPlaceholder(editing: LLMProfile | null, prefix: string): string {
  if (editing) {
    return editing.key_hint
      ? t("lib.llm_profiles.placeholders.keep_hint", { hint: editing.key_hint })
      : t("lib.llm_profiles.placeholders.keep_current");
  }
  return prefix
    ? t("lib.llm_profiles.placeholders.prefix", { prefix })
    : t("lib.llm_profiles.placeholders.paste");
}

export interface ProfileFormInput {
  name: string;
  key: string;
  editing: boolean;
  prefix: string;
  kindLabel?: string;
  provider?: string;
  baseURL?: string;
  region?: string;
  projectID?: string;
  awsAccessKeyID?: string;
  awsSecretAccessKey?: string;
  providerChanged?: boolean;
}

// The prefix check only catches typos and is not a security control. It is skipped with a custom
// base_url (LiteLLM, gateways), which issue keys in their own format.
export function validateProfileForm(f: ProfileFormInput): string | null {
  if (!f.name) return t("lib.llm_profiles.errors.name_required");
  if (isCloudProvider(f.provider)) {
    if (!f.region) return t("lib.llm_profiles.errors.region_required");
    if (f.provider === "vertex" && !f.projectID)
      return t("lib.llm_profiles.errors.project_required");
    if (Boolean(f.awsAccessKeyID) !== Boolean(f.awsSecretAccessKey)) {
      return t("lib.llm_profiles.errors.aws_pair");
    }
    return null;
  }
  if (f.provider === "openai-compatible" && !f.baseURL)
    return t("lib.llm_profiles.errors.base_url_required");
  if ((!f.editing || f.providerChanged) && !f.key) return t("lib.llm_profiles.errors.key_required");
  if (f.key && f.prefix && !f.baseURL && !f.key.startsWith(f.prefix)) {
    const kind = f.kindLabel ?? "";
    return kind === ""
      ? t("lib.llm_profiles.errors.prefix_generic", { prefix: f.prefix })
      : t("lib.llm_profiles.errors.prefix", { kind, prefix: f.prefix });
  }
  return null;
}

export function validateFederationForm(name: string, f: FederationSettings): string | null {
  if (!name) return t("lib.llm_profiles.errors.name_required");
  if (!f.federation_rule_id.startsWith("fdrl_"))
    return t("lib.llm_profiles.errors.federation_prefix", {
      field: t("lib.llm_profiles.labels.federation_rule"),
      prefix: "fdrl_",
    });
  if (!/^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i.test(f.organization_id))
    return t("lib.llm_profiles.errors.federation_org");
  if (!f.service_account_id.startsWith("svac_"))
    return t("lib.llm_profiles.errors.federation_prefix", {
      field: t("lib.llm_profiles.labels.federation_service_account"),
      prefix: "svac_",
    });
  if (f.workspace_id && !f.workspace_id.startsWith("wrkspc_"))
    return t("lib.llm_profiles.errors.federation_prefix", {
      field: t("lib.llm_profiles.labels.federation_workspace"),
      prefix: "wrkspc_",
    });
  return null;
}
