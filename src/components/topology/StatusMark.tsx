import clsx from 'clsx'
import { CircleX, TriangleAlert } from 'lucide-react'
import { SHOWN_WORD, type Shown } from '@/lib/detail'

/** The glyph alone: a dot, a triangle, a crossed circle or a hollow ring. The colours are the theme's own ok / warn / bad steps, which hold 3:1 as a glyph on a light surface as well as a dark one. */
function Glyph({ state, far }: { state: Shown; far?: boolean }) {
  const size = far ? 22 : 14
  return state === 'attention' ? (
    <TriangleAlert size={size} strokeWidth={2.25} className="text-warn" aria-hidden />
  ) : state === 'down' ? (
    <CircleX size={size} strokeWidth={2.25} className="text-bad" aria-hidden />
  ) : state === 'unknown' ? (
    <span className={clsx('rounded-full border-[1.5px] border-nb-500', far ? 'size-3' : 'size-2')} aria-hidden />
  ) : (
    <span className={clsx('rounded-full bg-ok', far ? 'size-3' : 'size-2')} aria-hidden />
  )
}

/**
 * The one status marker of a box or a card: a shape, a word and a colour, never the colour alone. Healthy is a quiet filled dot (the thing that
 * is true of nearly everything, so it earns no louder glyph), Needs attention a triangle, Not working a crossed circle, Unknown a hollow ring.
 * The word is the marker's name for assistive technology and its tooltip. `far` draws it for a canvas zoomed out, where it must stay legible on screen.
 */
export function StatusMark({ state, far, className }: { state: Shown; far?: boolean; className?: string }) {
  const word = SHOWN_WORD[state]
  return (
    <span role="img" aria-label={word} title={word} data-status={state} className={clsx('grid shrink-0 place-items-center', far ? 'size-6' : 'size-4', className)}>
      <Glyph state={state} far={far} />
    </span>
  )
}

/** The four states with their words: the key to the one status language, in the canvas legend and on the Telemetry tab. */
export function StatusKey({ className }: { className?: string }) {
  return (
    <span className={clsx('flex flex-wrap items-center gap-x-4 gap-y-1', className)} data-testid="status-key">
      {(['healthy', 'attention', 'down', 'unknown'] as const).map((state) => (
        <span key={state} className="flex items-center gap-1.5">
          <span className="grid size-4 shrink-0 place-items-center"><Glyph state={state} /></span>
          {SHOWN_WORD[state]}
        </span>
      ))}
    </span>
  )
}
