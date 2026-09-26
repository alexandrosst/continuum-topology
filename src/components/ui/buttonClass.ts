import clsx from 'clsx'

export type ButtonVariant = 'primary' | 'secondary' | 'danger' | 'ghost'

/** A light press-down on click, not a bounce: the control is still exactly where the pointer is, it just answers
 * back. Shared by `buttonClass` and the handful of hand-styled buttons that can't use it directly (they pick
 * their own color/size but should still feel like part of the same set of controls) - the topology view tabs. */
export const PRESS_CLASS = 'transition-[background-color,color,transform] duration-100 active:scale-[0.97]'

/** The look of a button, for a link that must be one (a link inside a button is not valid, and reads badly to a screen reader). */
export function buttonClass(variant: ButtonVariant = 'secondary', size: 'sm' | 'md' = 'md', className?: string) {
  return clsx(
    'inline-flex items-center justify-center gap-2 rounded-md font-medium',
    PRESS_CLASS,
    'disabled:cursor-not-allowed disabled:opacity-50 disabled:active:scale-100 focus-visible:outline-2 focus-visible:outline-accent/60',
    size === 'md' ? 'h-9 px-4 text-sm' : 'h-7 px-2.5 text-xs',
    variant === 'primary' && 'bg-accent text-white hover:bg-accent-600',
    variant === 'secondary' && 'border border-nb-800 bg-nb-925 text-nb-300 hover:bg-nb-940',
    variant === 'danger' && 'border border-bad/30 bg-bad/10 text-bad hover:bg-bad/20',
    variant === 'ghost' && 'text-nb-400 hover:bg-nb-940 hover:text-nb-300',
    className,
  )
}
