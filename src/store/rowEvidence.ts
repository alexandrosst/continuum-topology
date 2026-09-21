import { useCallback, useMemo } from 'react'
import { indexModel, weakAttributes } from '@/lib/provenance'
import type { Provenance } from '@/lib/types'
import { useEffectiveModel } from './effectiveModel'

/** A value of a discovered record that is a guess or not known: what to say beside it in a table. */
export interface WeakValue {
  level: 'guess' | 'unknown'
  /** The signal it rests on, or why it is not known. */
  why?: string
}

type Kind = 'cluster' | 'node' | 'service'
type Rec = Provenance & { id: string } & Record<string, unknown>

/**
 * For the rows of a table: is this one value of a record a guess, or unknown? Read from the server's effective model
 * when it has the value (it knows how each value was arrived at); otherwise from the record's own evidence.
 * A value a person typed or overrode is theirs, and never weak. Values that are merely inferred by a rule or reported by
 * an agent are the ordinary case and carry nothing, so a table is not covered in chips.
 */
export function useWeakValue(kind: Kind): (rec: Rec, attr: string, field?: string) => WeakValue | undefined {
  const model = useEffectiveModel()
  const index = useMemo(() => indexModel(model), [model])
  return useCallback(
    (rec, attr, field = attr) => {
      if (rec.source !== 'discovered') return undefined
      const a = index.get(`${kind}|${rec.id}`)?.attributes[attr]
      if (a) {
        if (a.source === 'declared') return undefined
        return a.confidence === 'guess' || a.confidence === 'unknown' ? { level: a.confidence, why: a.evidence } : undefined
      }
      const w = weakAttributes(kind, rec).find((x) => x.field === field)
      return w ? { level: w.level, why: w.why } : undefined
    },
    [index, kind],
  )
}
