/**
 * E2E Auto-Fix test fixture. Contains a single deliberate P2 issue.
 * DO NOT MERGE - test only.
 */

/** Formats a count with a label, choosing singular or plural. */
export function pluralize(count: number, singular: string): string {
  // P2: incorrect default — always appends "s", fails for irregular plurals
  // and produces "1 itemss" for words already ending in "s"
  if (count === 1) {
    return `${count} ${singular}`
  }
  return `${count} ${singular}s`
}
