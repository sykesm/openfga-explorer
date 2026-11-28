// @ts-check
// Type matching, shared by the Explore type finder and the Check type
// combobox. Pure functions (no DOM), unit-tested with node:test.

/**
 * Splits a query into lower-case words at spaces and identifier punctuation.
 * @param {string} q
 * @returns {string[]}
 */
export const typeWords = (q) => q.toLowerCase().split(/[\s_:.\-/#]+/).filter(Boolean);

/** @param {string | undefined} s space-separated relation names */
export const splitRels = (s) => (s || '').split(' ').filter(Boolean);

/**
 * @typedef {{ score: number, rels: string[] }} TypeMatch
 * score: 0 name starts with the first word, 1 name contains every word,
 * 2 found through relations (rels names them).
 */

/**
 * Matches a type when every word is in its name, or the words missing from
 * the name all appear in one of its relations.
 * @param {string} name
 * @param {string[]} rels
 * @param {string[]} words
 * @returns {TypeMatch | null}
 */
export function matchType(name, rels, words) {
  if (!words.length) return { score: 0, rels: [] };
  const n = name.toLowerCase();
  const rest = words.filter((w) => !n.includes(w));
  if (!rest.length) return { score: n.startsWith(words[0]) ? 0 : 1, rels: [] };
  const hits = rels.filter((r) => { const l = r.toLowerCase(); return rest.every((w) => l.includes(w)); });
  return hits.length ? { score: 2, rels: hits } : null;
}

/**
 * The tag shown next to a type found through relations: up to two names.
 * @param {string[]} rels
 */
export const relTag = (rels) => (rels.length ? `${rels.slice(0, 2).join(', ')}${rels.length > 2 ? ` +${rels.length - 2}` : ''}` : '');

/**
 * Ranks options by match: best score first, model order within a score.
 * @template {{ label: string, rels: string[] }} T
 * @param {T[]} options
 * @param {string} q
 * @returns {{ o: T, m: TypeMatch }[]}
 */
export function rankTypes(options, q) {
  const words = typeWords(q);
  return options
    .map((o, i) => ({ o, m: matchType(o.label, o.rels, words), i }))
    .filter((x) => x.m)
    .sort((a, b) => /** @type {TypeMatch} */ (a.m).score - /** @type {TypeMatch} */ (b.m).score || a.i - b.i)
    .map(({ o, m }) => ({ o, m: /** @type {TypeMatch} */ (m) }));
}

/**
 * Most-recent-first list with value moved to the front, capped.
 * @template T
 * @param {T[]} list @param {T} value @param {number} [cap]
 */
export const pushRecent = (list, value, cap = 8) => [value, ...list.filter((v) => v !== value)].slice(0, cap);
