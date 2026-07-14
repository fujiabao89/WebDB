/**
 * E2E Auto-Fix test fixture. Contains a single deliberate P2 issue.
 * DO NOT MERGE - test only.
 */

/** Formats a count with a label, choosing singular or plural. */
export function pluralize(count: number, singular: string, plural?: string): string {
  if (count === 1) {
    return `${count} ${singular}`
  }
  return `${count} ${plural ?? defaultPlural(singular)}`
}

/**
 * Derives a default plural form from the singular.
 * Handles common English patterns; callers should pass an explicit `plural`
 * for irregular words not covered here.
 */
function defaultPlural(singular: string): string {
  // Words ending in s, x, z, ch, sh → add "es"
  if (/[sxz]$|[cs]h$/i.test(singular)) return `${singular}es`
  // Consonant + y → ies
  if (/[^aeiou]y$/i.test(singular)) return `${singular.slice(0, -1)}ies`
  // Default: add "s"
  return `${singular}s`
}
