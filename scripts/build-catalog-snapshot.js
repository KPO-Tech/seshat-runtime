// Builds internal/catalog/snapshot.json.gz from a models.dev catalog (the output of https://models.dev/api.json, or the snapshot
// opencode bundles at packages/core/src/models-dev/snapshot.txt).
// Usage: node scripts/build-catalog-snapshot.js <api.json> [out.json.gz]
//
// Only what Seshat reads is kept, so the embedded file stays small. The provider ids, base URLs and environment variable
// names in this file are the trusted ones: a catalog fetched later never overrides them (see internal/catalog/merge.go).
const fs = require('fs');
const zlib = require('zlib');
const [src, out = 'internal/catalog/snapshot.json.gz'] = process.argv.slice(2);
const raw = JSON.parse(fs.readFileSync(src, 'utf8'));
const MIN_PROVIDERS = 100;
if (Object.keys(raw).length < MIN_PROVIDERS) {
  console.error(`the catalog has fewer than ${MIN_PROVIDERS} providers; refusing to write a snapshot`);
  process.exit(1);
}
const num = (x) => (typeof x === 'number' && x > 0 ? x : undefined);
const providers = {};
for (const [id, p] of Object.entries(raw)) {
  const models = {};
  for (const [mid, m] of Object.entries(p.models || {})) {
    const input = (m.modalities && m.modalities.input) || ['text'];
    const output = (m.modalities && m.modalities.output) || ['text'];
    // Only text-producing models that can call tools are useful to an agent.
    if (!output.includes('text') || !m.tool_call) continue;
    models[mid] = {
      n: m.name,
      c: num(m.limit && m.limit.context),
      o: num(m.limit && m.limit.output),
      i: input.filter((x) => x !== 'text').join(',') || undefined,
      r: m.reasoning ? 1 : undefined,
      p: m.cost && num(m.cost.input) !== undefined ? [m.cost.input, m.cost.output, m.cost.cache_read, m.cost.cache_write] : undefined,
      s: m.status || undefined,
      d: m.release_date || undefined,
    };
  }
  if (Object.keys(models).length === 0) continue;
  providers[id] = { n: p.name, a: p.api || undefined, e: p.env && p.env.length ? p.env : undefined, k: p.npm || undefined, m: models };
}
const body = JSON.stringify({ v: 1, providers });
fs.writeFileSync(out, zlib.gzipSync(Buffer.from(body), { level: 9 }));
console.log(`${Object.keys(providers).length} providers, ${Object.values(providers).reduce((n, p) => n + Object.keys(p.m).length, 0)} models, ${body.length} bytes raw, ${fs.statSync(out).size} bytes compressed`);
