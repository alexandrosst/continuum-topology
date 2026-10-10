import clsx from 'clsx'
import { CircleX, TriangleAlert } from 'lucide-react'
import { SHOWN_WORD, type Shown } from '@/lib/detail'

/**
 * The one status marker of a box or a card: a shape, a word and a colour, never the colour alone. Healthy is a quiet filled dot (the thing that
 * is true of nearly everything, so it earns no louder glyph), Needs attention a triangle, Not working a crossed circle, Unknown a hollow ring.
 * The word is the marker's name for assistive technology and its tooltip; the colours are the theme's own ok / warn / bad steps, which hold 3:1
 * as a glyph on a light surface as well as a dark one. `far` draws it for a canvas zoomed out, where it must stay legible on screen.
 */
export function StatusMark({ state, far, className }: { state: Shown; far?: boolean; className?: string }) {
  const word = SHOWN_WORD[state]
  const size = far ? 22 : 14
  return (
    <span role="img" aria-label={word} title={word} data-status={state} className={clsx('grid shrink-0 place-items-center', far ? 'size-6' : 'size-4', className)}>
      {state === 'attention' ? (
        <TriangleAlert size={size} strokeWidth={2.25} className="text-warn" aria-hidden />
      ) : state === 'down' ? (
        <CircleX size={size} strokeWidth={2.25} className="text-bad" aria-hidden />
      ) : state === 'unknown' ? (
        <span className={clsx('rounded-full border-[1.5px] border-nb-500', far ? 'size-3' : 'size-2')} aria-hidden />
      ) : (
        <span className={clsx('rounded-full bg-ok', far ? 'size-3' : 'size-2')} aria-hidden />
      )}
    </span>
  )
}
