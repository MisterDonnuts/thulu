import { api, ApiError } from "./api";
import { t } from "../i18n";

export interface Membership {
  id: string;
  name: string;
  slug: string;
  owner_id: string;
  created_at: string;
  role: string;
  member_count: number;
  current: boolean;
}

export type WorkspaceAction = "switch" | "rename" | "transfer" | "delete";

// Delete only from inside the workspace: DELETE /workspaces/{id} acts as the session's role.
export function workspaceActions(m: Membership, owner: boolean): WorkspaceAction[] {
  const acts: WorkspaceAction[] = [];
  if (!m.current) acts.push("switch");
  if (canInvite(m.role)) acts.push("rename");
  if (owner) {
    acts.push("transfer");
    if (m.current) acts.push("delete");
  }
  return acts;
}

export function workspaceMeta(m: Membership): string {
  const parts = [
    m.slug,
    roleLabel(m.role),
    t("settings.account.workspaces.members", { count: m.member_count }),
  ];
  if (m.current) parts.push(t("settings.account.workspaces.current"));
  return parts.join(" · ");
}

export interface BlockingWorkspace {
  id: string;
  name: string;
  slug: string;
  member_count: number;
}

export interface Member {
  user_id: string;
  email?: string;
  name?: string;
  role: string;
}

export function listMemberships(): Promise<Membership[]> {
  return api.get<Membership[]>("/auth/workspaces");
}

export function listMembers(workspaceID: string): Promise<Member[]> {
  return api.get<Member[]>(`/auth/workspaces/${encodeURIComponent(workspaceID)}/members`);
}

export async function switchWorkspace(workspaceID: string): Promise<void> {
  await api.post("/auth/switch-workspace", { workspace_id: workspaceID });
}

export async function transferOwnership(workspaceID: string, userID: string): Promise<void> {
  await api.post(`/auth/workspaces/${encodeURIComponent(workspaceID)}/transfer`, {
    user_id: userID,
  });
}

// Must match auth.maxWorkspaceNameLen in the daemon.
export const WORKSPACE_NAME_MAX = 80;

export function workspaceNameProblem(name: string): string {
  const n = name.trim();
  if (!n) return t("settings.account.rename.empty");
  if ([...n].length > WORKSPACE_NAME_MAX) {
    return t("settings.account.rename.too_long", { max: WORKSPACE_NAME_MAX });
  }
  return "";
}

export async function renameWorkspace(workspaceID: string, name: string): Promise<void> {
  await api.patch(`/auth/workspaces/${encodeURIComponent(workspaceID)}`, { name: name.trim() });
}

export async function deleteWorkspace(workspaceID: string, slug: string): Promise<void> {
  await api.del(`/workspaces/${encodeURIComponent(workspaceID)}`, { confirm: slug });
}

export async function deleteAccount(email: string): Promise<void> {
  await api.del("/auth/me", { confirm: email });
}

export function createWorkspace(name: string): Promise<Membership> {
  return api.post<Membership>("/auth/workspaces", { name: name.trim() });
}

export function switchTarget(pathname: string): string {
  if (pathname === "/project" || pathname.startsWith("/project/")) return "/projects/";
  return pathname || "/";
}

export function workspaceInitial(name: string): string {
  const first = Array.from(name.trim())[0];
  return first ? first.toLocaleUpperCase() : "?";
}

export function workspaceColor(id: string): string {
  let h = 0;
  for (const ch of id) h = (h * 31 + ch.charCodeAt(0)) >>> 0;
  return `var(--viz-${(h % 8) + 1})`;
}

export function createLockText(me: {
  create_workspace_reason?: string;
  create_workspace_max?: number;
}): string {
  if (me.create_workspace_reason === "cap") {
    return t("layout.workspaces.create_cap", { count: me.create_workspace_max ?? 0 });
  }
  if (me.create_workspace_reason === "reserved") return t("layout.workspaces.create_reserved");
  return t("layout.workspaces.create_unavailable");
}

export function canInvite(role: string | undefined): boolean {
  return role === "owner" || role === "admin";
}

export interface Invitation {
  id: string;
  workspace_id: string;
  email: string;
  role: string;
  invited_by: string;
  created_at: string;
  awaiting_access: boolean;
}

export type AddMemberResult =
  { status: "ok"; user_id: string } | { status: "invited"; invitation: Invitation };

export function addMember(
  workspaceID: string,
  email: string,
  role: string,
): Promise<AddMemberResult> {
  return api.post<AddMemberResult>(`/auth/workspaces/${encodeURIComponent(workspaceID)}/members`, {
    email,
    role,
  });
}

export function listInvitations(workspaceID: string): Promise<Invitation[]> {
  return api.get<Invitation[]>(`/auth/workspaces/${encodeURIComponent(workspaceID)}/invitations`);
}

export async function setInvitationRole(
  workspaceID: string,
  id: string,
  role: string,
): Promise<void> {
  await api.patch(
    `/auth/workspaces/${encodeURIComponent(workspaceID)}/invitations/${encodeURIComponent(id)}`,
    { role },
  );
}

export async function cancelInvitation(workspaceID: string, id: string): Promise<void> {
  await api.del(
    `/auth/workspaces/${encodeURIComponent(workspaceID)}/invitations/${encodeURIComponent(id)}`,
  );
}

// Mirrors the daemon's comparison: surrounding spaces and letter case do not count.
export function emailConfirmed(typed: string, email: string): boolean {
  const want = email.trim().toLowerCase();
  return want !== "" && typed.trim().toLowerCase() === want;
}

// The daemon wants the slug exactly; only surrounding spaces are forgiven here.
export function slugConfirmed(typed: string, slug: string): boolean {
  return slug !== "" && typed.trim() === slug;
}

export function ownedBy(list: Membership[], userID: string): Membership[] {
  return list.filter((m) => m.owner_id === userID);
}

export function transferCandidates(members: Member[], ownerID: string): Member[] {
  return members.filter((m) => m.user_id !== ownerID);
}

export function blockingWorkspaces(err: unknown): BlockingWorkspace[] {
  if (!(err instanceof ApiError) || err.status !== 409) return [];
  const body = err.body as { workspaces?: unknown } | undefined;
  return Array.isArray(body?.workspaces) ? (body.workspaces as BlockingWorkspace[]) : [];
}

export function memberLabel(m: Member): string {
  const name = m.name?.trim() ?? "";
  const email = m.email?.trim() ?? "";
  if (name && email && name !== email) return `${name} (${email})`;
  if (email) return email;
  return name ? name : m.user_id;
}

export function identityProviderNames(
  keys: string[],
  providers: Array<{ key: string; name: string }>,
): string {
  const names = new Map(
    providers.filter((p) => p.name.trim() !== "").map((p) => [p.key, p.name.trim()]),
  );
  return keys
    .map((k) => names.get(k) ?? k)
    .filter(Boolean)
    .join(", ");
}

export function roleLabel(role: string): string {
  switch (role) {
    case "owner":
      return t("settings.roles.owner");
    case "admin":
      return t("settings.roles.admin");
    case "developer":
      return t("settings.roles.developer");
    case "viewer":
      return t("settings.roles.viewer");
    default:
      return role;
  }
}
