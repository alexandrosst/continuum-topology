import { useEffect, useState } from 'react'

/** One column a list page's ColumnPicker offers to hide - key is what is stored, label is what shows in
 *  the picker. A page's "core" columns (name, actions) simply never appear here: they're not optional.
 *  `width` is only needed by a page whose `<Table>` uses a fixed `cols` layout (most list pages don't -
 *  see NamespacesPage for the one that does): keeping it on the same definition as the key/label means a
 *  page doesn't hand-maintain a second, parallel array that has to stay in the same order as this one. */
export interface ColumnDef {
  key: string
  label: string
  width?: string
}

const PREFIX = 'ikhnos:columns:'

function readHidden(storageKey: string, defaultHidden: readonly string[]): Set<string> {
  try {
    const raw = localStorage.getItem(PREFIX + storageKey)
    // Nothing remembered yet: the page's own default. Once a person has touched the picker their choice is what counts.
    if (raw === null) return new Set(defaultHidden)
    const arr = JSON.parse(raw) as unknown
    return new Set(Array.isArray(arr) ? arr.filter((x): x is string => typeof x === 'string') : [])
  } catch {
    return new Set(defaultHidden)
  }
}

/**
 * Which of a list page's optional columns are hidden, remembered per browser under `storageKey` - the
 * same per-browser convenience the theme toggle and the collapsed sidebar already use, not an account
 * setting shared with anyone else. Columns start visible; a person trims the ones they don't look
 * at instead of being stuck with whichever set the page shipped with, the way Nodes/Clusters/Applications
 * and similar dense tables tend to grow more columns than fit a laptop screen at once. A page may list
 * `defaultHidden` columns that start off for someone who has not chosen anything yet (detail that is there
 * to compare across rows, not to read at a glance); they are one click away in the picker.
 */
export function useColumnVisibility(storageKey: string, defaultHidden: readonly string[] = []) {
  const [hidden, setHidden] = useState<Set<string>>(() => readHidden(storageKey, defaultHidden))

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
