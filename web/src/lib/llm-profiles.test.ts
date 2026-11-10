import { describe, expect, it } from "vitest";

import type { AgentLite, EngineInfo, KeyReason, LLMProfile } from "./llm-profiles";
import {
  agentsOnDefault,
  canRecheck,
  effortLabel,
  engineLabel,
  isCloudProvider,
  keyBadge,
  keyNoteText,
  keyNoteTone,
  keyPlaceholder,
  keyRejected,
  keyTrouble,
  modelPlaceholder,
  noDefaultText,
  prefilledProfileName,
  profileMeta,
  profileModalTitle,
  profileModelText,
  providerLabel,
  refusedText,
  usedByText,
  validateFederationForm,
  validateProfileForm,
} from "./llm-profiles";

const engines: EngineInfo[] = [
  { name: "claude", label: "Claude", provider: "anthropic", is_default: true },
  { name: "codex", label: "Codex", provider: "openai" },
];

function profile(p: Partial<LLMProfile>): LLMProfile {
  return { id: "p1", name: "p1", engine: "claude", ...p };
}

describe("agentsOnDefault", () => {
  const agents: AgentLite[] = [
    { name: "dev", engine: "claude" },
    { name: "review", engine: "codex" },
    { name: "plan" },
  ];

  it("counts the agents that named no engine against the fallback one", () => {
    expect(agentsOnDefault("claude", [], agents, engines)).toEqual(["dev", "plan"]);
    expect(agentsOnDefault("codex", [], agents, engines)).toEqual(["review"]);
  });

  it("drops agents bound to a profile of their own, whichever profile it is", () => {
    const bound = [profile({ id: "x", engine: "codex", bound_agents: ["dev"] })];
    expect(agentsOnDefault("claude", bound, agents, engines)).toEqual(["plan"]);
  });

  it("puts nobody on the fallback when no engine claims to be one", () => {
    const orphans = engines.map((e) => ({ ...e, is_default: false }));
    expect(agentsOnDefault("claude", [], agents, orphans)).toEqual(["dev"]);
  });
});

describe("usedByText", () => {
  it("distinguishes an unused profile from an unused engine", () => {
    expect(usedByText([], true)).toBe("No agent runs on this engine");
    expect(usedByText([], false)).toBe("No agent uses it");
  });

  it("lists up to five names and counts the rest", () => {
    expect(usedByText(["a", "b"], false)).toBe("Used by a, b");
    expect(usedByText(["a", "b", "c", "d", "e", "f", "g"], false)).toBe(
      "Used by a, b, c, d, e and 2 more",
    );
  });
});

describe("profileModelText", () => {
  it("prefers the profile's model, then the engine's fallback, then the CLI's", () => {
    expect(profileModelText("opus", "sonnet")).toBe("opus");
    expect(profileModelText("", "sonnet")).toBe("sonnet (engine default)");
    expect(profileModelText(undefined, undefined)).toBe("CLI default model");
  });
});

describe("profileMeta", () => {
  it("says which parts are unset rather than dropping them", () => {
    expect(profileMeta(profile({}))).toEqual(["model: CLI default", "never used"]);
  });

  it("names the engine's fallback model for a profile without one", () => {
    expect(profileMeta(profile({}), "sonnet")[0]).toBe("model sonnet (engine default)");
    expect(profileMeta(profile({ model: "opus" }), "sonnet")[0]).toBe("model opus");
    expect(profileMeta(profile({}), "<b>")[0]).toBe("model &lt;b&gt; (engine default)");
  });

  it("reads a zero-value timestamp as never used", () => {
    expect(profileMeta(profile({ last_used_at: "0001-01-01T00:00:00Z" })).at(-1)).toBe(
      "never used",
    );
  });

  it("adds only the optional fields that are set", () => {
    const meta = profileMeta(profile({ model: "opus", effort: "high", org_id: "org-7" }));
    expect(meta.slice(0, 3)).toEqual(["model opus", "effort high", "org org-7"]);
  });

  it("names the cloud, where it runs, and whether it runs on workload identity", () => {
    const meta = profileMeta(
      profile({ provider: "vertex", region: "us-east5", project_id: "proj", has_key: false }),
    );
    expect(meta.slice(1, 3)).toEqual(["Google Vertex AI proj / us-east5", "workload identity"]);
    const keyed = profileMeta(
      profile({ provider: "bedrock", region: "eu-west-1", has_key: true, key_hint: "…1234" }),
    );
    expect(keyed).not.toContain("workload identity");
  });

  it("says when a custom endpoint gets a bearer token", () => {
    const meta = profileMeta(profile({ base_url: "https://llm.corp", auth_header: "bearer" }));
    expect(meta).toContain("bearer auth");
  });

  it("escapes what came from the server", () => {
    expect(profileMeta(profile({ model: "<script>" }))[0]).toBe("model &lt;script&gt;");
    expect(profileMeta(profile({ key_hint: "sk-…1a" }))[1]).toBe(
      'key <code class="code-inline">sk-…1a</code>',
    );
  });
});

describe("providers", () => {
  it("treats only bedrock and vertex as clouds", () => {
    expect(isCloudProvider("bedrock")).toBe(true);
    expect(isCloudProvider("vertex")).toBe(true);
    expect(isCloudProvider("anthropic")).toBe(false);
    expect(isCloudProvider(undefined)).toBe(false);
  });

  it("labels the known providers and passes others through", () => {
    expect(providerLabel("bedrock")).toBe("Amazon Bedrock");
    expect(providerLabel("openai-compatible")).toBe(
      "OpenAI-compatible API (DeepSeek, OpenRouter, vLLM)",
    );
    expect(providerLabel("mystery")).toBe("mystery");
  });
});

describe("engineLabel", () => {
  it("falls back to the engine's own name", () => {
    expect(engineLabel(engines, "claude")).toBe("Claude");
    expect(engineLabel(engines, "gemini")).toBe("gemini");
  });
});

describe("profileModalTitle", () => {
  it("names the profile being edited", () => {
    expect(profileModalTitle("Claude", profile({ name: "team" }), false, true)).toBe("Edit team");
  });

  it("offers a default when the engine has none, asked for or not", () => {
    expect(profileModalTitle("Claude", null, false, false)).toBe("Default profile for Claude");
    expect(profileModalTitle("Claude", null, true, true)).toBe("Default profile for Claude");
  });

  it("is a plain new profile once a default exists", () => {
    expect(profileModalTitle("Claude", null, false, true)).toBe("New Claude profile");
  });
});

describe("prefilledProfileName", () => {
  it("suggests default-<engine> for the first profile", () => {
    expect(prefilledProfileName("claude", [], null, false, false)).toBe("default-claude");
  });

  it("leaves the box empty when the name is taken", () => {
    const taken = [profile({ name: "default-claude" })];
    expect(prefilledProfileName("claude", taken, null, false, false)).toBe("");
  });

  it("leaves the box empty for an extra profile", () => {
    expect(prefilledProfileName("claude", [], null, false, true)).toBe("");
  });

  it("keeps the name of the profile being edited", () => {
    expect(prefilledProfileName("claude", [], profile({ name: "team" }), true, true)).toBe("team");
  });
});

describe("placeholders", () => {
  it("lists the engine's models when it published any", () => {
    expect(modelPlaceholder([])).toBe("Empty = CLI default");
    expect(modelPlaceholder(["opus", "sonnet"])).toBe("opus, sonnet, … · empty = CLI default");
  });

  it("shows the credential prefix when creating and the stored hint when editing", () => {
    expect(keyPlaceholder(null, "sk-ant-")).toBe("sk-ant-…");
    expect(keyPlaceholder(null, "")).toBe("Paste credential");
    expect(keyPlaceholder(profile({ key_hint: "sk-…1a" }), "sk-")).toBe(
      "Leave empty to keep sk-…1a",
    );
    expect(keyPlaceholder(profile({}), "sk-")).toBe("Leave empty to keep the current key");
  });
});

describe("validateProfileForm", () => {
  const base = { name: "team", key: "sk-ant-123", editing: false, prefix: "sk-ant-" };

  it("passes a complete new profile", () => {
    expect(validateProfileForm(base)).toBeNull();
  });

  it("asks for a name first", () => {
    expect(validateProfileForm({ ...base, name: "", key: "" })).toBe("Give the profile a name.");
  });

  it("requires a key when creating but not when editing", () => {
    expect(validateProfileForm({ ...base, key: "" })).toBe("Paste the key this profile runs with.");
    expect(validateProfileForm({ ...base, key: "", editing: true })).toBeNull();
  });

  it("catches a key pasted into the wrong credential kind", () => {
    expect(validateProfileForm({ ...base, key: "sk-proj-9", kindLabel: "Anthropic key" })).toBe(
      "Anthropic key starts with sk-ant-",
    );
  });

  it("names the credential generically when the kind has no label", () => {
    expect(validateProfileForm({ ...base, key: "nope" })).toBe(
      "This credential starts with sk-ant-",
    );
  });

  it("skips the prefix check for a kind with no fixed shape", () => {
    expect(validateProfileForm({ ...base, key: "anything", prefix: "" })).toBeNull();
  });

  it("skips the prefix check for a custom endpoint, whose keys have their own shape", () => {
    expect(
      validateProfileForm({ ...base, key: "lt-virtual-1", baseURL: "https://litellm.corp" }),
    ).toBeNull();
  });

  it("asks for a new key when an existing profile changes provider", () => {
    expect(validateProfileForm({ ...base, key: "", editing: true, providerChanged: true })).toBe(
      "Paste the key this profile runs with.",
    );
  });

  it("asks an OpenAI-compatible profile for its endpoint", () => {
    const compat = { ...base, key: "sk-deepseek", prefix: "", provider: "openai-compatible" };
    expect(validateProfileForm(compat)).toBe(
      "An OpenAI-compatible provider has no default address: fill in the base URL.",
    );
    expect(validateProfileForm({ ...compat, baseURL: "https://api.deepseek.com" })).toBeNull();
  });

  it("lets a cloud profile run without a key, but not without a region", () => {
    const cloud = { ...base, key: "", provider: "bedrock", region: "us-east-1" };
    expect(validateProfileForm(cloud)).toBeNull();
    expect(validateProfileForm({ ...cloud, region: "" })).toBe("Set the region the models run in.");
    expect(validateProfileForm({ ...cloud, provider: "vertex" })).toBe(
      "Set the Google Cloud project.",
    );
    expect(validateProfileForm({ ...cloud, awsAccessKeyID: "AKIA1" })).toBe(
      "The access key ID and the secret access key go together.",
    );
  });
});

describe("usedByText with one agent", () => {
  it("reads the same as with several: the sentence does not agree a verb with a count", () => {
    expect(usedByText(["a"], false)).toBe("Used by a");
  });
});

describe("effortLabel", () => {
  it("words the stored level and keeps an unknown one", () => {
    expect(effortLabel("high")).toBe("high");
    expect(effortLabel("max")).toBe("max");
  });
});

describe("noDefaultText", () => {
  const claude: EngineInfo = { name: "claude", label: "Claude", provider: "anthropic" };
  it("says runs will not start when the host fallback is off", () => {
    expect(noDefaultText(claude, 0)).toBe(
      "No default profile: runs on Claude will not start until you add a key.",
    );
    expect(noDefaultText(claude, 2)).toContain("2 agents wait");
  });
  it("names the host credential only when the operator allows it", () => {
    expect(noDefaultText({ ...claude, host_fallback: true }, 0)).toBe(
      "No default profile: runs on Claude use the daemon host's own credential.",
    );
  });
});

describe("keyBadge", () => {
  it("shows nothing for a profile saved before the check", () => {
    expect(keyBadge(profile({}))).toBeNull();
  });
  it("maps each state to a tone", () => {
    expect(keyBadge(profile({ key_state: "valid" }))).toMatchObject({ label: "Valid", tone: "ok" });
    expect(
      keyBadge(profile({ key_state: "invalid", key_reason: "rejected", key_status: 401 })),
    ).toMatchObject({
      label: "Invalid",
      tone: "danger",
      title: "The provider rejected the key (HTTP 401).",
    });
    expect(keyBadge(profile({ key_state: "unreachable" }))?.tone).toBe("warn");
    expect(keyBadge(profile({ key_state: "unchecked" }))?.tone).toBe("muted");
  });
  it("lets a run's refusal win over an older valid check", () => {
    const p = profile({ key_state: "valid", refused_status: 401 });
    expect(keyBadge(p)).toMatchObject({
      tone: "danger",
      title: "The provider refused a run's call with this key (HTTP 401).",
    });
  });
});

describe("keyNoteText", () => {
  it("words each reason from the facts the daemon stored", () => {
    expect(
      keyNoteText(
        profile({
          key_state: "unreachable",
          key_reason: "no_answer",
          key_host: "api.anthropic.com",
        }),
      ),
    ).toBe("Could not reach api.anthropic.com. Try Re-check later.");
    expect(keyNoteText(profile({ key_state: "unreachable", key_reason: "no_answer" }))).toBe(
      "The provider did not answer. Try Re-check later.",
    );
    expect(
      keyNoteText(profile({ key_state: "unreachable", key_reason: "unexpected", key_status: 503 })),
    ).toBe("The provider answered HTTP 503, so the key could not be confirmed.");
    expect(keyNoteText(profile({ key_state: "unchecked", key_reason: "not_checkable" }))).toMatch(
      /^Not checked: /,
    );
  });
  it("says nothing for a valid key or a reason it does not know", () => {
    expect(keyNoteText(profile({ key_state: "valid" }))).toBe("");
    expect(
      keyNoteText(profile({ key_state: "unchecked", key_reason: "later" as unknown as KeyReason })),
    ).toBe("");
  });
});

describe("refusedText", () => {
  it("is empty without a refusal and ignores Go's zero time", () => {
    expect(refusedText(profile({}))).toBe("");
    expect(refusedText(profile({ refused_status: 403, refused_at: "0001-01-01T00:00:00Z" }))).toBe(
      "The provider refused a run's call with this key (HTTP 403).",
    );
  });
  it("says when it happened", () => {
    const at = new Date(Date.now() - 5 * 60_000).toISOString();
    expect(refusedText(profile({ refused_status: 401, refused_at: at }))).toMatch(
      /^The provider refused a run's call with this key \(HTTP 401\) .+ ago\.$/,
    );
  });
});

describe("canRecheck", () => {
  it("offers a re-check only for a key the check can answer", () => {
    expect(canRecheck(profile({ has_key: true, key_state: "unreachable" }))).toBe(true);
    expect(canRecheck(profile({ has_key: false }))).toBe(false);
    expect(
      canRecheck(profile({ has_key: true, key_state: "unchecked", key_reason: "not_checkable" })),
    ).toBe(false);
  });
});

describe("keyTrouble", () => {
  it("says nothing for a good key, an unchecked one, or no profile", () => {
    expect(keyTrouble(undefined)).toBeNull();
    expect(keyTrouble(profile({ key_state: "valid" }))).toBeNull();
    expect(keyTrouble(profile({ key_state: "unchecked", key_reason: "not_checkable" }))).toBeNull();
  });
  it("tells a refused key from a provider that did not answer", () => {
    const down = profile({ key_state: "unreachable", key_reason: "no_answer", key_host: "api.x" });
    expect(keyTrouble(down)).toEqual({ tone: "warn", text: keyNoteText(down) });
    expect(keyNoteTone(down)).toBe("warn");
    expect(keyRejected(down)).toBe(false);
    const bad = profile({ key_state: "invalid", key_reason: "rejected", key_status: 401 });
    expect(keyTrouble(bad)?.tone).toBe("danger");
    expect(keyRejected(bad)).toBe(true);
    const refused = profile({ key_state: "unreachable", refused_status: 403 });
    expect(keyTrouble(refused)).toEqual({ tone: "danger", text: refusedText(refused) });
  });
});

describe("profileMeta last use", () => {
  it("rounds to minutes, like the rest of the dashboard", () => {
    const at = new Date(Date.now() - (40 * 60 + 52) * 1000).toISOString();
    const last = profileMeta(profile({ last_used_at: at })).at(-1) ?? "";
    expect(last).toMatch(/^last used 41 min\. ago$/);
    expect(last).not.toMatch(/s\b/);
  });
});

describe("federation profiles", () => {
  const fed = {
    federation_rule_id: "fdrl_01",
    organization_id: "123e4567-e89b-12d3-a456-426614174000",
    service_account_id: "svac_01",
  };

  it("accepts the shapes Anthropic documents", () => {
    expect(validateFederationForm("wif", fed)).toBeNull();
    expect(validateFederationForm("wif", { ...fed, workspace_id: "wrkspc_01" })).toBeNull();
  });

  it("names the field that is off", () => {
    expect(validateFederationForm("", fed)).toBe("Give the profile a name.");
    expect(validateFederationForm("wif", { ...fed, federation_rule_id: "x" })).toContain("fdrl_");
    expect(validateFederationForm("wif", { ...fed, organization_id: "org" })).toContain("UUID");
    expect(validateFederationForm("wif", { ...fed, service_account_id: "" })).toContain("svac_");
    expect(validateFederationForm("wif", { ...fed, workspace_id: "ws" })).toContain("wrkspc_");
  });

  it("shows the rule and no key badge", () => {
    const p: LLMProfile = {
      id: "p",
      name: "wif",
      engine: "claude",
      federation: fed,
      key_state: "unchecked",
      key_reason: "not_checkable",
    };
    expect(profileMeta(p).join(" ")).toContain("fdrl_01");
    expect(keyBadge(p)).toBeNull();
    expect(keyBadge({ ...p, refused_status: 401 })?.tone).toBe("danger");
  });
});
