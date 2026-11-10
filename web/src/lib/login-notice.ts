// Reason codes come from internal/auth/login_reasons.go; keep both in sync.

import { t } from "../i18n";

export const PENDING_ACCESS = "pending_access";
export const PENDING_ACCESS_FIRST = "pending_access_first";
export const NOT_INVITED = "not_invited";

export interface LoginNotice {
  pending: boolean;
  first: boolean;
  notInvited: boolean;
  reason: string;
  error: string;
  email: string;
  provider: string;
  request: string;
  invited: string[];
  invite: string;
  workspace: string;
}

export function loginNotice(search: string): LoginNotice {
  const q = new URLSearchParams(search);
  const reason = q.get("reason") ?? "";
  return {
    pending: reason === PENDING_ACCESS || reason === PENDING_ACCESS_FIRST,
    first: reason === PENDING_ACCESS_FIRST,
    notInvited: reason === NOT_INVITED,
    reason,
    error: q.get("error") ?? "",
    email: q.get("email") ?? "",
    provider: q.get("provider") ?? "",
    request: q.get("request") ?? "",
    invited: q.getAll("invited"),
    invite: q.get("invite") ?? "",
    workspace: q.get("workspace") ?? "",
  };
}

export function noticeText(n: LoginNotice): string {
  switch (n.reason) {
    case "state_expired":
      return t("login.reasons.state_expired");
    case "session_missing":
      return t("login.reasons.session_missing");
    case "email_unverified":
      return n.provider
        ? t("login.reasons.email_unverified", { provider: n.provider })
        : t("login.reasons.email_unverified_generic");
    default:
      return n.error;
  }
}

export function firstOwnerCommand(base: string, workspace: string, email: string): string {
  const body = JSON.stringify({ email, role: "owner" });
  // i18n-ignore a shell variable, not a message
  const ws = workspace ? encodeURIComponent(workspace) : "$OCTOPUS_OPERATOR_WORKSPACE";
  return [
    // i18n-ignore a shell command, not a message
    `curl -X POST "${base}/auth/workspaces/${ws}/members" \\`,
    // i18n-ignore a shell command, not a message
    `  -H "Authorization: Bearer $OCTOPUS_API_KEY" -H "Content-Type: application/json" \\`,
    `  -d '${body.replace(/'/g, "'\\''")}'`,
  ].join("\n");
}
