import { StatusBadge, type StatusTone } from '@/components/ui/Status'
import { SHOWN_WORD, type Shown } from '@/features/fusion/lib/fusionStatus'

/** Starting is a part that is coming up: the "setting up" tone, in FUSION's own word. */
const TONE: Record<Shown, StatusTone> = { healthy: 'healthy', attention: 'attention', down: 'down', unknown: 'unknown', starting: 'setup', off: 'off' }

/** A status as the glyph, the word and the colour together: Healthy, Needs attention, Not working or Unknown, and for a part that is coming up
 *  or switched off, Starting and Off. The one `Status` chip every screen uses. */
export function StatusChip({ status, className, ...p }: { status: Shown; className?: string; 'data-testid'?: string }) {
  return <StatusBadge tone={TONE[status]} label={SHOWN_WORD[status]} className={className} data-status={status} data-testid={p['data-testid']} />
}
