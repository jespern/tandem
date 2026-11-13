export type JSONValue = null | boolean | number | string | JSONValue[] | {[name: string]: JSONValue};
export function validUnicode(s: string): void {
  for (let i = 0; i < s.length; i++) {
    const c = s.charCodeAt(i);
    if (c >= 0xd800 && c <= 0xdbff) { const d = s.charCodeAt(++i); if (!(d >= 0xdc00 && d <= 0xdfff)) throw new Error('unpaired surrogate'); }
    else if (c >= 0xdc00 && c <= 0xdfff) throw new Error('unpaired surrogate');
  }
}
/** One reading: fatal UTF-8, unique decoded names, plain integers, no BOM. */
export function parseJSON(data: Uint8Array): JSONValue {
  const s = new TextDecoder('utf-8', {fatal: true, ignoreBOM: true}).decode(data); let i = 0;
  const space = () => { while (' \t\r\n'.includes(s[i] ?? '\0')) i++; };
  const bad = (): never => { throw new Error(`invalid protocol JSON at character ${i}`); };
  function string(): string {
    const start = i++; let escaped = false;
    while (i < s.length) { const c = s[i++]; if (!escaped && c === '"') { const out: unknown = JSON.parse(s.slice(start, i)); if (typeof out !== 'string') return bad(); validUnicode(out); return out; }
      if (!escaped && c === '\\') escaped = true; else escaped = false; }
    return bad();
  }
  function value(depth: number): JSONValue {
    if (depth > 64) return bad(); space(); const c = s[i];
    if (c === '"') return string();
    if (c === '[') { i++; space(); const out: JSONValue[] = []; if (s[i] === ']') { i++; return out; }
      while (true) { out.push(value(depth + 1)); space(); const next = s[i++]; if (next === ']') return out; if (next !== ',') return bad(); } }
    if (c === '{') { i++; space(); const out: {[name: string]: JSONValue} = Object.create(null); const names = new Set<string>();
      if (s[i] === '}') { i++; return out; }
      while (true) { space(); if (s[i] !== '"') return bad(); const key = string(); if (names.has(key)) return bad(); names.add(key); space(); if (s[i++] !== ':') return bad();
        out[key] = value(depth + 1); space(); const next = s[i++]; if (next === '}') return out; if (next !== ',') return bad(); } }
    for (const [text, result] of [['true', true], ['false', false], ['null', null]] as const) {
      if (s.startsWith(text, i)) { i += text.length; return result; }
    }
    const match = /^-?(?:0|[1-9][0-9]*)/.exec(s.slice(i));
    if (!match) return bad(); i += match[0].length;
    // Check the token before Number can round an out-of-range integer to 2**53.
    const exact = BigInt(match[0]);
    if (exact > (1n << 53n) || exact < -(1n << 53n)) return bad();
    return Number(exact);
  }
  const result = value(0); space(); if (i !== s.length) return bad(); return result;
}
