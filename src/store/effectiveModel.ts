import { useEffect, useMemo } from 'react'
import { api } from '@/lib/api'
import { indexModel, type ModelEntity } from '@/lib/provenance'
import { useObserved } from './observed'
import { useServer } from './server'

/**
 * The server's effective model for the organisation in use, loaded while something on screen needs it (the
 * Evidence section) and re-read whenever the state poll brings a new picture. The server tags it with a version, so an
 * unchanged model is answered from the browser's cache.
 */
export function useEffectiveModel() {
  const generatedAt = useServer((s) => s.state?.generatedAt)
  const conn = useServer((s) => s.conn)
  const model = useObserved((s) => s.model)
  useEffect(() => {
    const c = conn()
    if (!c || !generatedAt) return
    const server = Date.parse(generatedAt)
    if (Number.isFinite(server)) useObserved.getState().setSkew(server - Date.now())
    let cancelled = false
    api
      .model(c)
      .then((m) => {
        if (!cancelled) useObserved.getState().setModel(m)
      })
      .catch(() => {
        /* an older server has no model API: the record's own fields are shown instead */
      })
    return () => {
      cancelled = true
    }
  }, [generatedAt, conn])
  return model
}

/** One entity of the effective model, or undefined when the server has none (no server, or an older one). */
export function useEntityEvidence(kind: string, id: string | undefined): ModelEntity | undefined {
  const model = useEffectiveModel()
  const index = useMemo(() => indexModel(model), [model])
  return id ? index.get(`${kind}|${id}`) : undefined
}
