/** An agent's clock is worth a warning beyond this many milliseconds from the server's (certificate checks start to misbehave). */
export const SKEW_WARN_MS = 120_000

const words = (ms: number) => {
  const a = Math.abs(ms)
  return a < 3_600_000 ? `${Math.max(1, Math.round(a / 60_000))} min` : a < 48 * 3_600_000 ? `${Math.round(a / 3_600_000)} h` : `${Math.round(a / 86_400_000)} d`
}

/** "clock 3 min off": the chip on an agent whose clock differs from the server's by minutes. */
export const skewLabel = (ms: number) => `clock ${words(ms)} off`

/** The explanation behind the chip, or undefined when the difference is small enough to ignore. */
export function skewWarning(ms: number | undefined): string | undefined {
  if (ms === undefined || Math.abs(ms) <= SKEW_WARN_MS) return undefined
  return `This cluster's clock is ${words(ms)} ${ms > 0 ? 'ahead of' : 'behind'} the server's. Certificates are checked against the clock, so a difference this large can show up as "certificate expired" or "not yet valid" errors and hide the real cause. Fix time synchronisation (NTP, chrony) on the cluster's nodes.`
}
