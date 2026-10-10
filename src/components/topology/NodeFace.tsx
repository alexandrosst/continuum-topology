import clsx from 'clsx'
import type { CSSProperties, ReactNode } from 'react'
import { StatusMark } from '@/components/topology/StatusMark'
import type { Shown } from '@/lib/detail'

/**
 * The one face of a box or a card on the canvas, whichever view it is in: a 32px tile that says what kind of thing it is, the name
 * (13px, 600, the only line that is always there), at most one muted 11px line under it, whatever else belongs on the right, and the
 * status mark last. Zoomed out (`far`) the name is drawn larger and the second line is left to the status colour and the tooltip.
 */
export function NodeFace({ tile, tileLabel, tileStyle, title, name, meta, note, trailing, state, far, nameSize = 'card' }: {
  tile: ReactNode
  /** What the tile says, for assistive technology and as its tooltip ("Cloud tier"). */
  tileLabel?: string
  tileStyle?: CSSProperties
  /** The tooltip of the name: its full text, since the name truncates. */
  title?: string
  name: ReactNode
  /** The muted line under the name: where it is, what it is. Left out zoomed out. */
  meta?: ReactNode
  /** What is wrong, in the state colour, on the same line (after the meta). The one line that stays when zoomed out, drawn there in screen pixels. */
  note?: ReactNode
  trailing?: ReactNode
  state: Shown
  far?: boolean
  nameSize?: 'box' | 'card'
}) {
  return (
    <div className="flex min-h-8 items-center gap-3">
      <div role={tileLabel ? 'img' : undefined} aria-label={tileLabel} title={tileLabel} className="grid size-8 shrink-0 place-items-center rounded-lg" style={tileStyle}>{tile}</div>
      <div className="min-w-0 flex-1">
        <span className={clsx('block truncate font-semibold text-nb-300', far ? (nameSize === 'box' ? 'text-[24px] leading-7' : 'text-[19px] leading-6') : 'text-[13px] leading-4')} title={title}>{name}</span>
        {(far ? note : meta || note) && (
          <div className={clsx('flex items-center gap-1.5 text-nb-500', far ? 'leading-none' : 'h-3.5 text-[11px] leading-[14px]')}>
            {!far && meta}
            {!far && meta && note && <span className="text-nb-600" aria-hidden="true">·</span>}
            {note}
          </div>
        )}
      </div>
      {trailing}
      <StatusMark state={state} far={far} />
    </div>
  )
}
