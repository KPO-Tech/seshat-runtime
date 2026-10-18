// Adds models from the models.dev catalog snapshot to internal/providers/registry.go.
// Usage: node scripts/add-models-from-catalog.js <snapshot.json> <spec.json>
//   spec = { "Anthropic": { "snapshot": "anthropic", "add": ["claude-opus-4-7", ...] }, ... }
// A model that is already in the provider's list is skipped. Each added entry copies the provider's own conventions
// (default temperature, SupportsPC, structured output, prompt caching) from its first model, and takes the limits, the
// vision, audio and tool-call flags and the cost of cache reads from the snapshot. New models go before the provider's
// last entry: the first model of a list is the default large model and the last the default small one. The snapshot is the models.dev data that
// opencode bundles (packages/core/src/models-dev/snapshot.txt) or the output of https://models.dev/api.json.
const fs = require('fs');
const [snapPath, specPath] = process.argv.slice(2);
const snap = JSON.parse(fs.readFileSync(snapPath, 'utf8'));
const spec = JSON.parse(fs.readFileSync(specPath, 'utf8'));
const file = 'internal/providers/registry.go';
let go = fs.readFileSync(file, 'utf8');
const crlf = go.includes('\r\n');
go = go.split('\r\n').join('\n');

const ctxLabel = (n) => (n >= 1000000 ? `${(n / 1000000).toFixed(n % 1000000 === 0 ? 0 : 1)}M` : `${Math.round(n / 1000)}k`);
let added = 0;
for (const [provider, { snapshot, add }] of Object.entries(spec)) {
  const re = new RegExp(String.raw`(types\.APIProvider${provider}: \{\n[\s\S]*?\t\t\tModels: \[]ModelInfo\{\n)([\s\S]*?)(\t\t\t\},\n\t\t\},\n)`);
  const m = go.match(re);
  if (!m) throw new Error('provider block not found: ' + provider);
  const body = m[2];
  const first = body.match(/DefaultTemperature: ([0-9.]+), (SupportsPC: true, )?Description[\s\S]*?Capabilities: model\.Capabilities\{([^}]*)\}/);
  if (!first) throw new Error('cannot read the first model of ' + provider);
  const temp = first[1], supportsPC = !!first[2], flags = first[3];
  const providerCaches = /PromptCaching: true/.test(flags), providerStructured = /StructuredOutput: true/.test(flags);
  const have = new Set([...body.matchAll(/Identifier: "([^"]+)"/g)].map((x) => x[1]));
  let lines = '';
  for (const id of add) {
    if (have.has(id)) continue;
    const sm = snap[snapshot]?.models?.[id];
    if (!sm) throw new Error(`${provider}: ${id} is not in the snapshot`);
    const input = sm.modalities?.input || ['text'];
    const caps = [];
    if (input.includes('image')) caps.push('Vision: true');
    if (input.includes('audio')) caps.push('Audio: true');
    if (sm.tool_call) caps.push('FunctionCalling: true');
    caps.push('Streaming: true');
    if (providerCaches && sm.cost && sm.cost.cache_read > 0) caps.push('PromptCaching: true');
    if (providerStructured) caps.push('StructuredOutput: true');
    const bits = [`${ctxLabel(sm.limit.context)} context`];
    if (sm.reasoning) bits.push('reasoning');
    if (input.includes('image')) bits.push('image input');
    const desc = `${sm.name.replace(/ \(latest\)$/, '')} — ${bits.join(', ')}.`.replace(/"/g, '\\"');
    lines += `\t\t\t\t{Identifier: "${id}", ContextWindow: ${sm.limit.context}, MaxOutput: ${sm.limit.output}, DefaultTemperature: ${temp}, ${supportsPC ? 'SupportsPC: true, ' : ''}Description: "${desc}",\n\t\t\t\t\tCapabilities: model.Capabilities{${caps.join(', ')}}},\n`;
    added++;
  }
  // The order of a provider's list matters: the first model is its default large model and the last one its default small
  // model (see the TUI's provider defaults), so new models go before the last entry, which keeps both ends where they were.
  go = go.replace(re, (_, a, b, c) => {
    const entries = b.split(/(?=				{Identifier:)/);
    const last = entries.pop();
    return a + entries.join('') + lines + last + c;
  });
}
fs.writeFileSync(file, crlf ? go.split('\n').join('\r\n') : go);
console.log('added', added, 'models');
