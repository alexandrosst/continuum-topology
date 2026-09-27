import { useEffect, useState } from 'react'

/** One column a list page's ColumnPicker offers to hide - key is what is stored, label is what shows in
 *  the picker. A page's "core" columns (name, actions) simply never appear here: they're not optional. */
export interface ColumnDef {
  key: string
  label: string
}

const PREFIX = 'continuum:columns:'

function readHidden(storageKey: string): Set<string> {
  try {
    const raw = localStorage.getItem(PREFIX + storageKey)
    const arr = raw ? (JSON.parse(raw) as unknown) : []
    return new Set(Array.isArray(arr) ? arr.filter((x): x is string => typeof x === 'string') : [])
  } catch {
    return new Set()
  }
}

/**
 * Which of a list page's optional columns are hidden, remembered per browser under `storageKey` - the
 * same per-browser convenience the theme toggle and the collapsed sidebar already use, not an account
 * setting shared with anyone else. Every column starts visible; a person trims the ones they don't look
 * at instead of being stuck with whichever set the page shipped with, the way Nodes/Clusters/Applications
 * and similar dense tables tend to grow more columns than fit a laptop screen at once.
 */
export function useColumnVisibility(storageKey: string) {
  const [hidden, setHidden] = useState<Set<string>>(() => readHidden(storageKey))

  useEffect(() => {
    try {
      localStorage.setItem(PREFIX + storageKey, JSON.stringify([...hidden]))
    } catch {
      /* storage unavailable: the choice just is not remembered for next time */
    }
  }, [storageKey, hidden])

  const isVisible = (key: string) => !hidden.has(key)
  const toggle = (key: string) =>
    setHidden((prev) => {
      const next = new Set(prev)
      if (next.has(key)) next.delete(key)
      else next.add(key)
      return next
    })

  return { isVisible, toggle }
}
