import { describe, expect, it } from "vitest";
import {
  collectorSnippet,
  ingestBase,
  ingestSourcesHTML,
  ingestTokensHTML,
  labelReject,
  parseCIDRs,
  type IngestSource,
  type IngestToken,
} from "./ingest";

function token(over: Partial<IngestToken> = {}): IngestToken {
  return {
    id: "t1",
    name: "collector",
    prefix: "oct_abc",
    status: "active",
    allow_cidrs: [],
    created_at: "2026-07-31T10:00:00Z",
    ...over,
  };
}

function source(over: Partial<IngestSource> = {}): IngestSource {
  return {
    token_id: "t1",
    ip: "203.0.113.4",
    accepted: 1200,
    rejected: 0,
    last_seen: "2026-07-31T11:00:00Z",
    ...over,
  };
}

describe("labelReject", () => {
  it("spells out the reasons the wire uses", () => {
    expect(labelReject("ip_not_allowed")).toBe("address not on the allowlist");
    expect(labelReject("revoked")).toBe("token revoked");
    expect(labelReject("bad_token")).toBe("token not recognised");
  });
  it("still says something for an unknown or missing reason", () => {
    expect(labelReject("something_new")).toBe("refused");
    expect(labelReject()).toBe("refused");
  });
});

describe("collectorSnippet", () => {
  it("points the exporter at this origin and carries the token", () => {
    const s = collectorSnippet("secret123", "https://thulu.example.com");
    expect(s).toContain("OTEL_EXPORTER_OTLP_ENDPOINT=https://thulu.example.com/ingest");
    expect(s).toContain("x-octopus-token=secret123");
    expect(s.split("\n")).toHaveLength(3);
  });
});

describe("ingestBase", () => {
  it("prefers the telemetry address setting", () => {
    expect(ingestBase("https://ingest.example.com/", "http://10.0.0.4:8585")).toEqual({
      base: "https://ingest.example.com",
      fromSetting: true,
    });
  });
  it("falls back to the browser's origin and says so", () => {
    for (const unset of ["", "  ", null, undefined]) {
      expect(ingestBase(unset, "http://10.0.0.4:8585")).toEqual({
        base: "http://10.0.0.4:8585",
        fromSetting: false,
      });
    }
  });
});

describe("ingestTokensHTML", () => {
  it("invites the first token", () => {
    expect(ingestTokensHTML([])).toContain("No token yet");
  });
  it("calls an empty allowlist what it is", () => {
    expect(ingestTokensHTML([token()])).toContain("any address");
  });
  it("lists the CIDRs when there are some", () => {
    const html = ingestTokensHTML([token({ allow_cidrs: ["10.0.0.0/8", "203.0.113.4"] })]);
    expect(html).toContain("<code>10.0.0.0/8</code>");
    expect(html).not.toContain("any address");
  });
  it("keeps a revoked token listed, dimmed, and without actions", () => {
    const html = ingestTokensHTML([token({ status: "revoked" })]);
    expect(html).toContain('<tr class="list-table-dim">');
    expect(html).toContain("revoked</span>");
    expect(html).not.toContain("data-ing-revoke");
  });
  it("names every column for a phone, the actions only for a screen reader", () => {
    const html = ingestTokensHTML([token()]);
    expect(html).toContain('class="list-table list-table-stack"');
    expect(html).toContain('data-label="Allowed from"');
    expect(html).toContain('<span class="visually-hidden">Actions</span>');
  });
  it("gives an active token both actions", () => {
    const html = ingestTokensHTML([token()]);
    expect(html).toContain('data-ing-cidr="t1"');
    expect(html).toContain('data-ing-revoke="t1"');
  });
  it("escapes the token name", () => {
    expect(ingestTokensHTML([token({ name: '<img src=x onerror="y">' })])).not.toContain("<img");
  });
});

describe("ingestSourcesHTML", () => {
  it("says nothing has connected", () => {
    expect(ingestSourcesHTML([], [])).toContain("Nothing has connected yet");
  });
  it("names the token an address used", () => {
    expect(ingestSourcesHTML([source()], [token()])).toContain("collector");
  });
  it("falls back to an em dash for a token that is gone", () => {
    expect(ingestSourcesHTML([source({ token_id: "gone" })], [token()])).toContain(">—<");
  });
  it("flags a refusal and says why", () => {
    const html = ingestSourcesHTML(
      [source({ rejected: 7, last_reject: "ip_not_allowed" })],
      [token()],
    );
    expect(html).toContain('data-tone="danger"');
    expect(html).toContain('<span class="cell-sub">address not on the allowlist</span>');
  });
  it("prints a plain zero when nothing was refused", () => {
    const html = ingestSourcesHTML([source()], [token()]);
    expect(html).toMatch(/<td class="num"[^>]*>0<\/td>/);
    expect(html).not.toContain("data-tone");
  });
});

describe("parseCIDRs", () => {
  it("trims and drops the gaps", () => {
    expect(parseCIDRs(" 10.0.0.0/8 , , 203.0.113.4 ")).toEqual(["10.0.0.0/8", "203.0.113.4"]);
  });
  it("reads an empty answer as 'anywhere'", () => {
    expect(parseCIDRs("")).toEqual([]);
    expect(parseCIDRs("  ,  ")).toEqual([]);
  });
});
