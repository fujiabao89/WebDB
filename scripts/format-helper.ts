/**
 * E2E Auto-Fix test fixture. Contains a single deliberate P2 issue.
 * DO NOT MERGE - test only.
 */

/** Formats a count with a label, choosing singular or plural. */
export function pluralize(count: number, singular: string, plural?: string): string {
  if (count === 1) {
    return `${count} ${singular}`
  }
  return `${count} ${plural ?? `${singular}s`}`
}
