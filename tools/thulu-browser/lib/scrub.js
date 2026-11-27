// Values under 4 chars are not scrubbed: they would cut ordinary words out of the report.
export function scrubber(values) {
  const cut = [...new Set([...values].filter((v) => typeof v === "string" && v.length >= 4))]
    .sort((a, b) => b.length - a.length);
  return (text) => {
    let out = String(text ?? "");
    for (const v of cut) out = out.split(v).join("•••");
    return out;
  };
}
