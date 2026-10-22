import type { CertCell } from './operatorsView'

const DAY = 86_400_000
/** Operator certificates are issued for 30 days (backend pki.OperatorLifetime). */
export const CERT_LIFETIME_MS = 30 * DAY

export interface CertLife {
  /** How much of the 30-day lifetime is left, 0 to 1. */
  fraction: number
  /** neutral while renewing normally, warn when renewal is overdue or failing, bad once it ended. */
  tone: 'neutral' | 'warn' | 'bad'
  /** Longer-lived than an operator certificate is ever issued for: an older one, which does not renew the way new ones do. */
  legacy: boolean
  /** "expires in 25 days", for the bar's accessible value and its tooltip. */
  text: string
  /** Said in words only when something is wrong. */
  exception?: string
}

const days = (n: number) => `${n} day${n === 1 ? '' : 's'}`

/** The lifetime bar of a certificate cell: null when there is no date to measure (an agent's own certificate, or none issued).
 *  The row carries no issue date, so a certificate with more than its 30 days left is shown full and marked legacy. */
export function certLife(cert: CertCell, now: number): CertLife | null {
  const ends = cert.kind === 'auto' ? cert.endsAt : cert.kind === 'none' ? undefined : cert.until
  const at = ends ? Date.parse(ends) : NaN
  if (Number.isNaN(at)) return null
  const left = at - now
  const fraction = Math.max(0, Math.min(1, left / CERT_LIFETIME_MS))
  if (cert.kind === 'expired' || left <= 0) {
    const ago = Math.floor(-left / DAY)
    return { fraction: 0, tone: 'bad', legacy: false, text: ago < 1 ? 'expired today' : `expired ${days(ago)} ago`, exception: 'Expired' }
  }
  const n = Math.ceil(left / DAY)
  return {
    fraction,
    tone: cert.kind === 'failing' ? 'warn' : 'neutral',
    legacy: left > CERT_LIFETIME_MS + DAY,
    text: `expires in ${days(n)}`,
    exception: cert.kind === 'failing' ? 'Renewal failing' : undefined,
  }
}
