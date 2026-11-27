// SARIF 2.1.0 in and out. Every check writes its findings as SARIF with a partialFingerprint that
// survives line shifts, so runs on base and head deduplicate.

import { fingerprint, lineText, relTo } from "./util.js";

const SCHEMA = "https://json.schemastore.org/sarif-2.1.0.json";
const FP_KEY = "thuluGates/v1";

export const SEVERITIES = ["critical", "high", "medium", "low", "info"];

export function levelOf(severity) {
  if (severity === "critical" || severity === "high") return "error";
  if (severity === "medium") return "warning";
  return "note";
}

// severityOf maps a SARIF result (and its rule) to our scale. security-severity (CVSS) wins when
// present, then the result level, then the rule's default level.
export function severityOf(result, rule) {
  const cvss = Number(result?.properties?.["security-severity"] ?? rule?.properties?.["security-severity"]);
  if (Number.isFinite(cvss) && cvss > 0) {
    if (cvss >= 9) return "critical";
    if (cvss >= 7) return "high";
    if (cvss >= 4) return "medium";
    return "low";
  }
  const level = result?.level || rule?.defaultConfiguration?.level || "warning";
  return level === "error" ? "high" : level === "warning" ? "medium" : level === "note" ? "low" : "info";
}

// withFingerprints fills fingerprint on each finding from the flagged line's text.
export function withFingerprints(tool, root, findings) {
  for (const f of findings) {
    if (!f.fingerprint) f.fingerprint = fingerprint(tool, f.rule, f.file, f.file && f.line ? lineText(root, f.file, f.line) : f.message);
  }
  return findings;
}

export function toSarif(runs) {
  return {
    $schema: SCHEMA,
    version: "2.1.0",
    runs: runs.map(({ tool, version, findings }) => {
      const rules = [...new Set(findings.map((f) => f.rule))].map((id) => ({ id }));
      return {
        tool: { driver: { name: tool, version: version || undefined, informationUri: "https://thulu.dev", rules } },
        results: findings.map((f) => {
          const r = {
            ruleId: f.rule,
            level: levelOf(f.severity),
            message: { text: f.message || f.rule },
            partialFingerprints: { [FP_KEY]: f.fingerprint },
            properties: { severity: f.severity, blocking: Boolean(f.blocking) },
          };
          if (f.file) {
            r.locations = [
              {
                physicalLocation: {
                  artifactLocation: { uri: f.file },
                  region: f.line ? { startLine: f.line } : undefined,
                },
              },
            ];
          }
          return r;
        }),
      };
    }),
  };
}

// fromSarif reads any tool's SARIF into findings with paths relative to root.
export function fromSarif(doc, root) {
  const out = [];
  for (const run of doc?.runs || []) {
    const rules = new Map();
    for (const r of run.tool?.driver?.rules || []) rules.set(r.id, r);
    for (const res of run.results || []) {
      const loc = res.locations?.[0]?.physicalLocation;
      const rule = rules.get(res.ruleId) || (res.rule?.index !== undefined ? run.tool.driver.rules?.[res.rule.index] : undefined);
      let uri = loc?.artifactLocation?.uri || "";
      const baseId = loc?.artifactLocation?.uriBaseId;
      if (baseId && run.originalUriBaseIds?.[baseId]?.uri) {
        const b = run.originalUriBaseIds[baseId].uri.replace(/^file:\/\//, "");
        if (!uri.startsWith("/") && b.startsWith("/")) uri = b.replace(/\/$/, "") + "/" + uri;
      }
      out.push({
        rule: res.ruleId || rule?.id || "unknown",
        severity: severityOf(res, rule),
        file: relTo(root, decodeURIComponent(uri)),
        line: loc?.region?.startLine || 0,
        message: res.message?.text || rule?.shortDescription?.text || "",
      });
    }
  }
  return out;
}
