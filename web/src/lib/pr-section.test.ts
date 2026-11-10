// @vitest-environment jsdom
import { describe, expect, it } from "vitest";
import {
  PR_NO_CREDENTIAL,
  PR_POLL_ACTIVE_MS,
  PR_POLL_IDLE_MS,
  prMergeReady,
  prPollDelay,
  prSectionHTML,
  prSectionSignature,
  prTone,
  type CIState,
  type PRSectionInput,
  type PullRequestStatus,
} from "./pr-section";

const URL7 = "https://github.com/acme/widgets/pull/7";

function q<T extends Element = Element>(root: ParentNode, sel: string): T {
  const el = root.querySelector<T>(sel);
  if (!el) throw new Error(`no ${sel}`);
  return el;
}

function pr(over: Partial<PullRequestStatus> = {}): PullRequestStatus {
  return {
    project_id: "p1",
    project_name: "widgets",
    url: URL7,
    number: 7,
    title: "Fix login",
    state: "open",
    head: "octopus/t1",
    base: "main",
    ci: { source: "checks", total: 8, passed: 5, failed: 0, running: 3, state: "running" },
    review: "pending",
    mergeable: "blocked",
    changed_files: 5,
    additions: 120,
    deletions: 30,
    commits: 3,
    updated_at: "2026-10-06T10:00:00Z",
    ...over,
  };
}

const link = { project_id: "p1", project_name: "widgets", url: URL7 };

function render(input: Partial<PRSectionInput> = {}, open: string[] = []): HTMLElement {
  const box = document.createElement("section");
  box.innerHTML = prSectionHTML(
    { links: [link], rows: [pr()], failed: false, ...input },
    { openKeys: new Set(open) },
  );
  return box;
}

describe("prSectionHTML", () => {
  it("renders nothing without pull requests", () => {
    expect(prSectionHTML({ links: [], failed: false }, { openKeys: new Set() })).toBe("");
  });

  it("folds CI, review and number into the summary", () => {
    const box = render();
    const row = q(box, ".pr-row");
    expect(row.classList.contains("pr-row-running")).toBe(true);
    expect(row.classList.contains("row-group-boxed")).toBe(true);
    expect(row.classList.contains("row-group-boxed-edge")).toBe(true);
    expect(row.getAttribute("data-key")).toBe("p1");
    const head = q(row, "button.row[data-pr-toggle]");
    expect(head.getAttribute("aria-expanded")).toBe("false");
    expect(head.getAttribute("aria-controls")).toBe("pr-detail-p1");
    expect(q<HTMLElement>(row, "#pr-detail-p1.row-detail").hidden).toBe(true);
    expect(q(head, ".row-lead .pr-num").textContent).toBe("#7");
    expect(q(head, ".row-title").textContent).toBe("Fix login");
    expect(q(head, ".row-aside .pr-indicators .pr-ci")).not.toBeNull();
    const ci = q(box, ".pr-ci");
    expect(ci.textContent).toBe("5/8");
    expect(ci.classList.contains("pr-ci-running")).toBe(true);
    expect(ci.classList.contains("badge-info")).toBe(true);
    expect(ci.getAttribute("aria-label")).toContain("5");
    expect(q(ci, ".badge-spin use").getAttribute("href")).toBe("#i-refresh");
    expect(q(box, ".pr-review-pending use").getAttribute("href")).toBe("#i-eye");
    expect(box.querySelector(".pr-pill-ready")).toBeNull();
    expect(q(box, ".sec-head .sec-title").textContent).toBe("Pull requests1");
    expect(q(box, ".sec-head .count").textContent).toBe("1");
  });

  it("opens on the list row's sprite caret, not a text arrow", () => {
    const head = q(render(), "[data-pr-toggle]");
    expect(q(head, ".row-caret use").getAttribute("href")).toBe("#i-chevron-right");
    expect(head.textContent).not.toContain("▸");
  });

  it("shows no CI chip when there is no CI", () => {
    const box = render({
      rows: [
        pr({ ci: { source: "checks", total: 0, passed: 0, failed: 0, running: 0, state: "none" } }),
      ],
    });
    expect(box.querySelector(".pr-ci")).toBeNull();
  });

  it("leaves out the CI chip, not the block, for a state it does not know", () => {
    const box = render({
      rows: [
        pr({
          ci: {
            source: "checks",
            total: 3,
            passed: 1,
            failed: 2,
            running: 0,
            state: "failing" as string as CIState,
          },
        }),
      ],
    });
    expect(box.querySelector(".pr-ci")).toBeNull();
    expect(q(box, ".pr-num").textContent).toBe("#7");
    expect(q(box, ".pr-detail .fact-list").textContent).toContain(
      "1 passed · 2 failed · 0 running · 3 total",
    );
  });

  it("marks a pull request ready to merge", () => {
    const ready = pr({
      review: "approved",
      mergeable: "ready",
      ci: { source: "checks", total: 2, passed: 2, failed: 0, running: 0, state: "passed" },
    });
    const box = render({ rows: [ready] });
    expect(box.querySelector(".pr-row-ready")).not.toBeNull();
    expect(box.querySelector(".pr-pill-ready")).not.toBeNull();
    expect(q(box, ".pr-ci-passed use").getAttribute("href")).toBe("#i-check");
    expect(q(box, ".pr-review-approved use").getAttribute("href")).toBe("#i-check");
  });

  it("draws every indicator with the shared badge", () => {
    const ready = pr({
      state: "merged",
      review: "approved",
      mergeable: "ready",
      ci: { source: "checks", total: 2, passed: 2, failed: 0, running: 0, state: "passed" },
    });
    const box = render({ rows: [ready] });
    expect(q(box, ".pr-num").className).toBe("badge badge-mono pr-num");
    expect(q(box, ".pr-pill-merged").classList.contains("badge-accent")).toBe(true);
    expect(q(box, ".pr-ci-passed").classList.contains("badge-ok")).toBe(true);
    expect(box.querySelector(".pr-ci-passed .badge-spin")).toBeNull();
  });

  it("shows merged, closed and draft as a pill", () => {
    for (const state of ["merged", "closed", "draft"] as const) {
      const box = render({ rows: [pr({ state })] });
      expect(box.querySelector(`.pr-pill-${state}`), state).not.toBeNull();
      expect(box.querySelector(`.pr-row-${state}`), state).not.toBeNull();
    }
    expect(render().querySelector(".pr-pill-open")).toBeNull();
  });

  it("unfolds into the facts and the GitHub link", () => {
    const box = render({}, ["p1"]);
    expect(q(box, "[data-pr-toggle]").getAttribute("aria-expanded")).toBe("true");
    expect(q<HTMLElement>(box, ".pr-detail").hidden).toBe(false);
    const list = q(box, ".pr-detail > dl.fact-list");
    expect([...list.querySelectorAll("dt")].map((d) => d.textContent)).not.toContain("");
    const facts = list.textContent;
    expect(facts).toContain("5 files changed");
    expect(facts).toContain("+120");
    expect(facts).toContain("−30");
    expect(facts).toContain("octopus/t1");
    expect(facts).toContain("main");
    const a = q<HTMLAnchorElement>(box, ".pr-gh-link");
    expect(a.getAttribute("href")).toBe(URL7);
    expect(a.getAttribute("rel")).toBe("noopener noreferrer");
    expect(box.querySelector(".pr-hint")).toBeNull();
  });

  it("says when CI counts only GitHub Actions", () => {
    const box = render({
      rows: [
        pr({
          ci: { source: "actions", total: 1, passed: 1, failed: 0, running: 0, state: "passed" },
        }),
      ],
    });
    expect(box.querySelector(".pr-hint")).not.toBeNull();
  });

  it("escapes what GitHub and agents wrote", () => {
    const box = render({
      rows: [pr({ title: '<img src=x onerror="alert(1)">', head: "<b>x</b>" })],
    });
    expect(box.querySelector("img")).toBeNull();
    expect(box.querySelector("b")).toBeNull();
    expect(q(box, ".row-title").textContent).toBe('<img src=x onerror="alert(1)">');
  });

  it("never links a URL that is not http(s)", () => {
    const bad = "javascript:alert(1)";
    const box = render({ links: [{ ...link, url: bad }], rows: [pr({ url: bad })] });
    expect(box.querySelector("a")).toBeNull();
  });

  it("shows a skeleton while the first read is on its way", () => {
    const box = render({ rows: undefined });
    expect(box.querySelector(".pr-row-loading")).not.toBeNull();
    expect(box.querySelectorAll(".pr-row-loading .skeleton")).toHaveLength(3);
    expect(box.querySelector(".pr-row-loading .skeleton-chip.pr-skel-chip")).not.toBeNull();
    expect(q(box, ".pr-row-loading").classList.contains("row-group-boxed")).toBe(true);
    expect(q(box, ".pr-row-loading").getAttribute("aria-busy")).toBe("true");
    expect(box.querySelector(".pr-note")).toBeNull();
  });

  it("falls back to the plain link when the read failed", () => {
    const box = render({ rows: undefined, failed: true });
    expect(box.querySelector(".pr-note")).not.toBeNull();
    const a = q<HTMLAnchorElement>(box, ".pr-plain-link");
    expect(a.getAttribute("href")).toBe(URL7);
    expect(a.textContent).toContain("widgets");
  });

  it("keeps the last rows when a later read failed", () => {
    const box = render({ failed: true });
    expect(box.querySelector(".pr-note")).not.toBeNull();
    expect(box.querySelector("[data-pr-toggle]")).not.toBeNull();
  });

  it("turns a row GitHub refused into a link with the reason", () => {
    const box = render({
      rows: [
        {
          ...link,
          error: "api.pull_requests.errors.load_failed",
          error_message: "could not load the pull request: Not Found",
        },
      ],
    });
    expect(q(box, ".pr-row-error .pr-plain-link").getAttribute("href")).toBe(URL7);
    expect(q(box, ".pr-why").textContent).toContain("Not Found");
  });

  it("shows a pull request recorded after the last read as a plain link", () => {
    const other = {
      project_id: "p2",
      project_name: "api",
      url: "https://github.com/acme/api/pull/3",
    };
    const box = render({ links: [link, other] });
    expect(box.querySelectorAll("[data-pr-toggle]")).toHaveLength(1);
    expect(box.querySelector('.pr-row-plain[data-key="p2"]')).not.toBeNull();
  });

  it("asks for a GitHub credential when the project has none", () => {
    const box = render({ rows: [{ ...link, error: PR_NO_CREDENTIAL }] });
    const row = q(box, ".pr-row-cred");
    expect(q(row, ".pr-plain-link").textContent).toContain("PR #7");
    expect(q(row, ".pr-cred").textContent).toContain("No GitHub credential for this project.");
    expect(q(row, ".pr-cred").classList.contains("callout-warn")).toBe(true);
    expect(q(row, '.pr-cred a[href="/credentials/"]').textContent).toBe("Add one");
    expect(box.querySelector(".pr-why")).toBeNull();
  });

  it("opens with a section head, like the task's other sections", () => {
    const box = document.createElement("section");
    box.innerHTML = prSectionHTML(
      { links: [link], rows: [pr()], failed: false },
      { openKeys: new Set() },
    );
    expect(box.firstElementChild?.classList.contains("sec-head")).toBe(true);
    expect(box.querySelector(".pr-list .pr-row [data-pr-toggle]")).not.toBeNull();
  });
});

describe("prMergeReady / prTone", () => {
  it("needs approval, a mergeable state and CI passed or absent", () => {
    const base = pr({
      review: "approved",
      mergeable: "ready",
      ci: { source: "checks", total: 1, passed: 1, failed: 0, running: 0, state: "passed" },
    });
    expect(prMergeReady(base)).toBe(true);
    expect(
      prMergeReady({
        ...base,
        ci: { source: "checks", total: 0, passed: 0, failed: 0, running: 0, state: "none" },
      }),
    ).toBe(true);
    expect(prMergeReady({ ...base, review: "pending" })).toBe(false);
    expect(prMergeReady({ ...base, mergeable: "behind" })).toBe(false);
    expect(prMergeReady({ ...base, state: "draft" })).toBe(false);
  });

  it("puts failures first", () => {
    expect(prTone(pr({ review: "changes_requested" }))).toBe("failed");
    expect(prTone(pr({ mergeable: "conflicts" }))).toBe("failed");
    expect(
      prTone(
        pr({
          ci: { source: "checks", total: 1, passed: 0, failed: 1, running: 0, state: "failed" },
        }),
      ),
    ).toBe("failed");
    expect(prTone(pr({ state: "merged", review: "changes_requested" }))).toBe("merged");
    expect(prTone({ ...link, error: "x" })).toBe("error");
  });
});

describe("prPollDelay", () => {
  it("polls faster while CI runs or a review is awaited", () => {
    expect(prPollDelay(undefined)).toBe(PR_POLL_IDLE_MS);
    expect(prPollDelay([pr()])).toBe(PR_POLL_ACTIVE_MS);
    const settled = pr({
      review: "approved",
      ci: { source: "checks", total: 1, passed: 1, failed: 0, running: 0, state: "passed" },
    });
    expect(prPollDelay([settled])).toBe(PR_POLL_IDLE_MS);
    expect(prPollDelay([settled, pr({ review: "pending", ci: settled.ci })])).toBe(
      PR_POLL_ACTIVE_MS,
    );
    expect(prPollDelay([pr({ state: "merged" })])).toBe(PR_POLL_IDLE_MS);
  });
});

describe("prSectionSignature", () => {
  it("changes with what the block shows", () => {
    const a = prSectionSignature({ links: [link], rows: [pr()], failed: false });
    expect(prSectionSignature({ links: [link], rows: [pr()], failed: false })).toBe(a);
    expect(
      prSectionSignature({ links: [link], rows: [pr({ title: "Other" })], failed: false }),
    ).not.toBe(a);
    expect(prSectionSignature({ links: [link], rows: [pr()], failed: true })).not.toBe(a);
    expect(prSectionSignature({ links: [link], failed: false })).not.toBe(a);
  });
});
