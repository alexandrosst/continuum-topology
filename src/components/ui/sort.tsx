import clsx from 'clsx'
import { ArrowDown, ArrowUp, ChevronsUpDown } from 'lucide-react'
import { useMemo, useState, type ComponentProps, type ReactNode } from 'react'
import { Th } from '@/components/ui/primitives'

export type SortDir = 'asc' | 'desc'
export interface SortState<K extends string> { key: K; dir: SortDir }

/**
 * Sort a list by a column: `by` says, for each sortable column, what to compare (a number or a string; a missing value always goes last).
 * Clicking the sorted column again flips it; a third click goes back to the order the list came in.
 */
export function useSort<T, K extends string>(rows: readonly T[], by: Record<K, (row: T) => string | number | undefined>, initial?: SortState<K>) {
  const [sort, setSort] = useState<SortState<K> | undefined>(initial)
  const toggle = (key: K) => setSort((s) => (!s || s.key !== key ? { key, dir: 'asc' } : s.dir === 'asc' ? { key, dir: 'desc' } : undefined))
  const sorted = useMemo((): T[] => {
    if (!sort) return [...rows]
    const get = by[sort.key]
    const sign = sort.dir === 'asc' ? 1 : -1
    return [...rows].sort((a, b) => {
      const x = get(a)
      const y = get(b)
      if (x === undefined && y === undefined) return 0
      if (x === undefined) return 1
      if (y === undefined) return -1
      return (typeof x === 'number' && typeof y === 'number' ? x - y : String(x).localeCompare(String(y), undefined, { numeric: true, sensitivity: 'base' })) * sign
    })
  }, [rows, sort, by])
  return { sorted, sort, toggle }
}

/** A column header that sorts: a button inside the cell, with `aria-sort` on the cell and an arrow that says which way. */
export function SortTh<K extends string>({ id, sort, onSort, children, className, ...p }: Omit<ComponentProps<'th'>, 'onClick'> & { id: K; sort: SortState<K> | undefined; onSort: (key: K) => void; children: ReactNode }) {
  const on = sort?.key === id
  const Arrow = !on ? ChevronsUpDown : sort.dir === 'asc' ? ArrowUp : ArrowDown
  return (
    <Th {...p} className={className} aria-sort={on ? (sort.dir === 'asc' ? 'ascending' : 'descending') : 'none'}>
      <button type="button" onClick={() => onSort(id)} className={clsx('group -mx-1 inline-flex items-center gap-1 rounded px-1 uppercase tracking-wide transition-colors duration-[var(--dur-fast)] hover:text-nb-300 focus-visible:outline-2 focus-visible:outline-accent', on && 'text-nb-300')}>
        {children}
        <Arrow size={12} aria-hidden className={clsx('transition-opacity duration-[var(--dur-fast)]', on ? 'opacity-100' : 'opacity-0 group-hover:opacity-60 group-focus-visible:opacity-60')} />
      </button>
    </Th>
  )
}
