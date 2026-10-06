// Fuzzy matching for the command palette: a query matches text when its
// characters appear in order (case-insensitively). Scores favour matches at
// word starts, consecutive runs, and early positions, so "nses" ranks
// "New session" above "Unassigned sessions". Space-separated query words
// must each match, in any order.

export interface FuzzyMatch {
  score: number;
  /** Matched character positions in the text (for highlighting). */
  indices: number[];
}

const SCORE_MATCH = 16;
const BONUS_BOUNDARY = 10;
const BONUS_CAMEL = 8;
const BONUS_FIRST_CHAR = 12;
const BONUS_CONSECUTIVE = 8;
const PENALTY_GAP_START = 3;
const PENALTY_GAP = 1;
const PENALTY_LEADING = 1;
const MAX_LEADING_PENALTY = 6;

function isSeparator(c: string): boolean {
  return /[\s\-_/.:·,()[\]#@]/.test(c);
}

/** Bonus for matching at text position i (word starts and camelCase humps). */
function positionBonus(text: string, i: number): number {
  if (i === 0) return BONUS_BOUNDARY + BONUS_FIRST_CHAR;
  const prev = text[i - 1];
  const cur = text[i];
  if (isSeparator(prev)) return BONUS_BOUNDARY;
  if (prev >= "a" && prev <= "z" && cur >= "A" && cur <= "Z") return BONUS_CAMEL;
  if (/[a-zA-Z]/.test(prev) && /[0-9]/.test(cur)) return BONUS_CAMEL;
  return 0;
}

/** Match one query word against text; null when its characters don't all appear in order. */
export function fuzzyWord(word: string, text: string): FuzzyMatch | null {
  const q = word.toLowerCase();
  const t = text.toLowerCase();
  const m = q.length;
  const n = t.length;
  if (m === 0) return { score: 0, indices: [] };
  if (m > n) return null;
  // Quick reject: subsequence check.
  for (let i = 0, j = 0; i < m; i++, j++) {
    j = t.indexOf(q[i], j);
    if (j < 0) return null;
  }
  const NEG = Number.NEGATIVE_INFINITY;
  // score[i][j]: best score with q[i] matched at t[j]; from[i][j]: where q[i-1] matched.
  const score: Float64Array[] = [];
  const from: Int32Array[] = [];
  for (let i = 0; i < m; i++) {
    const row = new Float64Array(n).fill(NEG);
    const back = new Int32Array(n).fill(-1);
    // Best of score[i-1][k] - gap(j-k-1) over k <= j-2, carried along j.
    let carry = NEG;
    let carryFrom = -1;
    for (let j = 0; j < n; j++) {
      if (i > 0 && j >= 2) {
        const prev = score[i - 1][j - 2];
        // Extending an existing gap costs PENALTY_GAP per char; opening costs PENALTY_GAP_START.
        const extended = carry - PENALTY_GAP;
        const opened = prev - PENALTY_GAP_START;
        if (opened >= extended) {
          carry = opened;
          carryFrom = j - 2;
        } else {
          carry = extended;
        }
      }
      if (t[j] !== q[i]) continue;
      const bonus = SCORE_MATCH + positionBonus(text, j);
      if (i === 0) {
        row[j] = bonus - Math.min(j * PENALTY_LEADING, MAX_LEADING_PENALTY);
        continue;
      }
      let best = NEG;
      let bestFrom = -1;
      if (j >= 1 && score[i - 1][j - 1] > NEG) {
        best = score[i - 1][j - 1] + BONUS_CONSECUTIVE;
        bestFrom = j - 1;
      }
      if (carry > best) {
        best = carry;
        bestFrom = carryFrom;
      }
      if (best === NEG) continue;
      row[j] = best + bonus;
      back[j] = bestFrom;
    }
    score.push(row);
    from.push(back);
  }
  let end = -1;
  let best = NEG;
  for (let j = 0; j < n; j++) {
    if (score[m - 1][j] > best) {
      best = score[m - 1][j];
      end = j;
    }
  }
  if (end < 0) return null;
  const indices = new Array<number>(m);
  for (let i = m - 1, j = end; i >= 0; i--) {
    indices[i] = j;
    j = from[i][j];
  }
  return { score: best, indices };
}

/** Split a query into words (blank queries have none). */
export function queryWords(query: string): string[] {
  return query.trim().split(/\s+/).filter(Boolean);
}

/**
 * Match every query word against an item's title (highlighted) or, failing
 * that, its secondary text (project, id, keywords) at a discount. Null when a
 * word matches neither. Shorter titles win ties.
 */
export function fuzzyItem(words: readonly string[], title: string, secondary = ""): FuzzyMatch | null {
  let total = 0;
  const indices = new Set<number>();
  for (const w of words) {
    const inTitle = fuzzyWord(w, title);
    const inSecondary = secondary ? fuzzyWord(w, secondary) : null;
    if (inTitle && (!inSecondary || inTitle.score >= inSecondary.score * 0.6)) {
      total += inTitle.score;
      for (const i of inTitle.indices) indices.add(i);
    } else if (inSecondary) {
      total += inSecondary.score * 0.6;
    } else {
      return null;
    }
  }
  total -= title.length * 0.05;
  return { score: total, indices: [...indices].sort((a, b) => a - b) };
}

export interface Ranked<T> {
  item: T;
  match: FuzzyMatch;
}

/**
 * Rank items by match score (plus each item's own boost), keeping the input
 * order among equals, so callers pass items most-relevant-first (recent
 * sessions first, done tasks last).
 */
export function rank<T>(
  items: readonly T[],
  query: string,
  fields: (item: T) => { title: string; secondary?: string; boost?: number },
  limit = 50,
): Ranked<T>[] {
  const words = queryWords(query);
  const out: (Ranked<T> & { order: number; total: number })[] = [];
  items.forEach((item, order) => {
    const f = fields(item);
    const match = words.length ? fuzzyItem(words, f.title, f.secondary) : { score: 0, indices: [] };
    if (!match) return;
    out.push({ item, match, order, total: match.score + (f.boost ?? 0) });
  });
  out.sort((a, b) => (a.total !== b.total ? b.total - a.total : a.order - b.order));
  return out.slice(0, limit).map(({ item, match }) => ({ item, match }));
}
