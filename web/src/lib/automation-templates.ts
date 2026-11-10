// The Go test parses these specs: each schedule says `tz: UTC` (withTimezone swaps it on pick),
// and translated phrases must stay YAML-safe (automation-templates.test.ts).

import { t } from "../i18n";

export interface AutomationTemplate {
  title: string;
  blurb: string;
  spec: string;
}

export interface TemplateSource extends AutomationTemplate {
  texts: Record<string, string>;
}

export const TEMPLATE_SOURCES: TemplateSource[] = [
  {
    title: t("lib.automation_templates.blank.title"),
    blurb: t("lib.automation_templates.blank.blurb"),
    // i18n-ignore a YAML recipe the editor parses, not a message
    spec: `name: my automation
description: what it does and why
trigger:
  manual: true
policy:
  concurrency: forbid
  timeout: 10m
steps:
  - id: ticket
    task:
      title: "Something to look at"
      body: |
        Written by an automation.
      fingerprint: "my-automation"
      on_duplicate: comment
`,
    texts: {
      "my automation": "lib.automation_templates.blank.spec.name",
      "what it does and why": "lib.automation_templates.blank.spec.description",
      "Something to look at": "lib.automation_templates.blank.spec.task_title",
      "Written by an automation.": "lib.automation_templates.blank.spec.task_body",
    },
  },
  {
    title: t("lib.automation_templates.error_guard.title"),
    blurb: t("lib.automation_templates.error_guard.blurb"),
    // i18n-ignore a YAML recipe the editor parses, not a message
    spec: `name: error guard
description: watch prod error rate and open an incident ticket when it spikes
trigger:
  schedule: "*/15 * * * *"
  tz: UTC
policy:
  concurrency: forbid
  starting_deadline: 2m
  max_agent_runs_per_day: 8
  timeout: 5m
steps:
  - id: errs
    query:
      source: metric
      # The environment's ID, not its name. Pick it with the Environment picker above the editor.
      # Save refuses an ID that does not exist.
      environment: ENVIRONMENT_ID
      metric: error_rate
      window: 15m
      agg: avg

  - id: gate
    condition: "steps.errs.has_data && steps.errs.value > 5"

  - id: ticket
    task:
      title: "Error rate at {{ steps.errs.value }}%"
      body: |
        The 15-minute average error rate is {{ steps.errs.value }}%.

        Investigate, and open a PR if it needs a code change.
      type: bug
      priority: high
      fingerprint: "error-rate"
      on_duplicate: comment
`,
    texts: {
      "error guard": "lib.automation_templates.error_guard.spec.name",
      "watch prod error rate and open an incident ticket when it spikes":
        "lib.automation_templates.error_guard.spec.description",
      "The environment's ID, not its name. Pick it with the Environment picker above the editor.":
        "lib.automation_templates.error_guard.spec.env_comment",
      "Save refuses an ID that does not exist.":
        "lib.automation_templates.error_guard.spec.env_comment_save",
      "Error rate at {{ steps.errs.value }}%":
        "lib.automation_templates.error_guard.spec.task_title",
      "The 15-minute average error rate is {{ steps.errs.value }}%.":
        "lib.automation_templates.error_guard.spec.task_body",
      "Investigate, and open a PR if it needs a code change.":
        "lib.automation_templates.error_guard.spec.task_body_ask",
    },
  },
  {
    title: t("lib.automation_templates.pr_sweep.title"),
    blurb: t("lib.automation_templates.pr_sweep.blurb"),
    // i18n-ignore a YAML recipe the editor parses, not a message
    spec: `name: pr review sweep
description: nudge the agent on PRs that have been sitting
trigger:
  schedule: "0 * * * *"
  tz: UTC
policy:
  concurrency: forbid
  max_agent_runs_per_day: 20
  timeout: 10m
steps:
  - id: stale
    query:
      source: tasks
      with_pr: true
      open: true
      stale_for: 24h
      limit: 20

  - id: gate
    condition: "steps.stale.count > 0"

  - id: each
    fanout:
      over: "steps.stale.json.tasks"
      max_items: 5
      as: task
    steps:
      # No agent: the mention wakes the one on the task's current stage.
      - id: nudge
        mention:
          task_id: "{{ task.id }}"
          body: |
            Your PR has had no activity for a day.
            Check it for reviews or comments to address, and reply here with what you changed.
`,
    texts: {
      "pr review sweep": "lib.automation_templates.pr_sweep.spec.name",
      "nudge the agent on PRs that have been sitting":
        "lib.automation_templates.pr_sweep.spec.description",
      "No agent: the mention wakes the one on the task's current stage.":
        "lib.automation_templates.pr_sweep.spec.agent_comment",
      "Your PR has had no activity for a day.": "lib.automation_templates.pr_sweep.spec.body",
      "Check it for reviews or comments to address, and reply here with what you changed.":
        "lib.automation_templates.pr_sweep.spec.body_ask",
    },
  },
  {
    title: t("lib.automation_templates.review_comments.title"),
    blurb: t("lib.automation_templates.review_comments.blurb"),
    // i18n-ignore a YAML recipe the editor parses, not a message
    spec: `name: answer review comments
description: a human review comment wakes the agent that owns the PR
trigger:
  event: github.pull_request_review_comment
  filter: >
    trigger.payload.action == "created"
    && !trigger.payload.sender_is_bot
    && exists(trigger.payload.task_id)
policy:
  debounce: 5m
  max_agent_runs_per_day: 30
  timeout: 5m
steps:
  # No agent: the mention wakes the one on the task's current stage.
  # The debounce gathers a burst into one run; the comments are all in trigger.payload.events.
  - id: reply
    mention:
      task_id: "{{ trigger.payload.task_id }}"
      body: |
        New review comment on {{ trigger.payload.pr_url }} from {{ trigger.payload.comment_by }}:

        {{ trigger.payload.comment }}

        Address it and push.
`,
    texts: {
      "answer review comments": "lib.automation_templates.review_comments.spec.name",
      "a human review comment wakes the agent that owns the PR":
        "lib.automation_templates.review_comments.spec.description",
      "No agent: the mention wakes the one on the task's current stage.":
        "lib.automation_templates.pr_sweep.spec.agent_comment",
      "The debounce gathers a burst into one run; the comments are all in trigger.payload.events.":
        "lib.automation_templates.review_comments.spec.debounce_comment",
      "New review comment on {{ trigger.payload.pr_url }} from {{ trigger.payload.comment_by }}:":
        "lib.automation_templates.review_comments.spec.body",
      "Address it and push.": "lib.automation_templates.review_comments.spec.body_ask",
    },
  },
  {
    title: t("lib.automation_templates.script_decides.title"),
    blurb: t("lib.automation_templates.script_decides.blurb"),
    // i18n-ignore a YAML recipe the editor parses, not a message
    spec: `name: script decides
description: cheap deterministic check first, model second
trigger:
  schedule: "0 9 * * 1-5"
  tz: UTC
policy:
  concurrency: forbid
  max_agent_runs_per_day: 3
  timeout: 15m
steps:
  - id: check
    script:
      run: |
        # Anything that prints is available as steps.check.stdout.
        echo "needs-work"
      timeout: 2m

  - id: gate
    condition: 'contains(steps.check.stdout, "needs-work")'

  - id: work
    task:
      title: "Morning check flagged something"
      body: |
        The check printed:

        \`\`\`
        {{ steps.check.stdout }}
        \`\`\`
      # One card per day. The date comes from the firing, so a run that starts at 23:59 does not land on tomorrow's card.
      fingerprint: "morning-check:{{ trigger.fired_date }}"
      on_duplicate: skip
`,
    texts: {
      "script decides": "lib.automation_templates.script_decides.spec.name",
      "cheap deterministic check first, model second":
        "lib.automation_templates.script_decides.spec.description",
      "Anything that prints is available as steps.check.stdout.":
        "lib.automation_templates.script_decides.spec.script_comment",
      "Morning check flagged something": "lib.automation_templates.script_decides.spec.task_title",
      "The check printed:": "lib.automation_templates.script_decides.spec.task_body",
      "One card per day. The date comes from the firing, so a run that starts at 23:59 does not land on tomorrow's card.":
        "lib.automation_templates.script_decides.spec.fingerprint_comment",
    },
  },
];

// Longest phrases first, so one that contains another is replaced whole.
export function localizeSpec(spec: string, texts: Record<string, string>): string {
  return Object.keys(texts)
    .sort((a, b) => b.length - a.length)
    .reduce((out, phrase) => out.split(phrase).join(t(texts[phrase])), spec);
}

export const TEMPLATES: AutomationTemplate[] = TEMPLATE_SOURCES.map((src) => ({
  title: src.title,
  blurb: src.blurb,
  spec: localizeSpec(src.spec, src.texts),
}));
