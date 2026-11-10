import { t } from "../i18n";
import { api } from "./api";
import type { Board, Stage } from "./stages";
import { emptyRowHTML, esc } from "./ui";

export interface AgentType {
  name: string;
  type: string;
  valid_stages?: string[];
}

export function boardOptions(boards: Board[], selected: string): string {
  return (
    `<option value="">${esc(t("lib.pickers.instance_default"))}</option>` +
    boards
      .map(
        (b) =>
          `<option value="${esc(b.id)}"${b.id === selected ? " selected" : ""}>${esc(b.label)}</option>`,
      )
      .join("")
  );
}

// Must match how the daemon reads valid_stages when launching a run: empty means every stage.
export function agentCovers(agent: { valid_stages?: string[] | null }, stageID: string): boolean {
  const valid = agent.valid_stages ?? [];
  return valid.length === 0 || valid.includes(stageID);
}

export function agentColumns<S extends Pick<Stage, "initial" | "terminal"> & { virtual?: boolean }>(
  stages: readonly S[],
): S[] {
  return stages.filter((s) => !s.initial && !s.terminal && !s.virtual);
}

export interface StageAgentRowsOpts {
  catalog?: Stage[];
  boardHref?: string;
}

// Template entries for columns not shown (another board) keep a row so saving the table never drops
// them.
export function stageAgentRows(
  stages: Stage[],
  agents: AgentType[],
  template: Record<string, string>,
  opts: StageAgentRowsOpts = {},
): string {
  const shown = agentColumns(stages);
  const ids = new Set(shown.map((s) => s.id));
  const catalog = opts.catalog ?? stages;
  const extra = Object.keys(template)
    .filter((id) => template[id] && !ids.has(id))
    .map((id) => catalog.find((s) => s.id === id) ?? ({ id, label: id } as Stage));
  const rows = [...shown, ...extra];
  if (rows.length === 0) {
    const link = opts.boardHref
      ? ` <a href="${esc(opts.boardHref)}">${esc(t("lib.pickers.no_agent_columns_link"))}</a>`
      : "";
    return emptyRowHTML(undefined, {
      cols: 2,
      cls: "tpl-empty",
      html: `${esc(t("lib.pickers.no_agent_columns"))}${link}`,
    });
  }
  return rows
    .map((s) => {
      const cur = template[s.id] || "";
      const options = agents
        .filter((a) => agentCovers(a, s.id))
        .map((a) => {
          const v = a.type || a.name;
          return `<option value="${esc(v)}"${cur === v ? " selected" : ""}>${esc(v)}</option>`;
        })
        .join("");
      return `<tr><td>${esc(s.label)}</td><td><select data-stage="${esc(s.id)}" aria-label="${esc(s.label)}"><option value="">—</option>${options}</select></td></tr>`;
    })
    .join("");
}

export interface StageSelect {
  value: string;
  dataset: { stage?: string };
}

// A stage left on "—" is omitted: the API reads an absent key as "no default" and an empty string
// as a default named "".
export function readStageTemplate(selects: Iterable<StageSelect>): Record<string, string> {
  const out: Record<string, string> = {};
  for (const s of selects) {
    if (s.value && s.dataset.stage) out[s.dataset.stage] = s.value;
  }
  return out;
}

export function stageTemplateTable(o: {
  table: HTMLElement;
  boardSelect: HTMLSelectElement;
  catalog: () => Stage[];
  agents: () => AgentType[];
  boards: () => Board[];
  signal?: AbortSignal;
}): { render(template: Record<string, string>): Promise<void> } {
  const cache = new Map<string, Promise<Stage[]>>();
  const columnsOf = (id: string) => {
    let p = cache.get(id);
    if (!p) {
      p = api.get<Stage[]>(`/boards/${encodeURIComponent(id)}/stages`).catch(() => {
        cache.delete(id);
        return o.catalog();
      });
      cache.set(id, p);
    }
    return p;
  };
  let seq = 0;
  async function render(template: Record<string, string>) {
    const mine = ++seq;
    const id = o.boardSelect.value || (o.boards().find((b) => b.default)?.id ?? "");
    const cols = id ? await columnsOf(id) : o.catalog();
    if (mine !== seq) return;
    o.table.innerHTML = stageAgentRows(cols, o.agents(), template, {
      catalog: o.catalog(),
      boardHref: id ? `/settings/?board=${encodeURIComponent(id)}#boards` : "/settings/#boards",
    });
  }
  o.boardSelect.addEventListener(
    "change",
    () =>
      void render(readStageTemplate(o.table.querySelectorAll<HTMLSelectElement>("[data-stage]"))),
    { signal: o.signal },
  );
  return { render };
}
