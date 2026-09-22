import clsx from 'clsx'

export type ButtonVariant = 'primary' | 'secondary' | 'danger' | 'ghost'

/** The look of a button, for a link that must be one (a link inside a button is not valid, and reads badly to a screen reader). */
export function buttonClass(variant: ButtonVariant = 'secondary', size: 'sm' | 'md' = 'md', className?: string) {
  return clsx(
    'inline-flex items-center justify-center gap-2 rounded-md font-medium transition-[background-color,color,transform] duration-100',
    // A light press-down on click, not a bounce: the button is still exactly where the pointer is, it just answers back.
    'active:scale-[0.97] disabled:cursor-not-allowed disabled:opacity-50 disabled:active:scale-100 focus-visible:outline-2 focus-visible:outline-accent/60',
    size === 'md' ? 'h-9 px-4 text-sm' : 'h-7 px-2.5 text-xs',
    variant === 'primary' && 'bg-accent text-white hover:bg-accent-600',
    variant === 'secondary' && 'border border-nb-800 bg-nb-925 text-nb-300 hover:bg-nb-940',
    variant === 'danger' && 'border border-red-500/30 bg-red-500/10 text-red-300 hover:bg-red-500/20',
    variant === 'ghost' && 'text-nb-400 hover:bg-nb-940 hover:text-nb-300',
    className,
  )
}
