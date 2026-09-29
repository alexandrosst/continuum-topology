import { useEffect, useMemo } from 'react'
import { api } from '@/lib/api'
import { indexModel, type ModelEntity } from '@/lib/provenance'
import { useObserved } from './observed'
import { useServer } from './server'

// Every mounted consumer of useEffectiveModel (the placement hook that's always active on Topology/Clusters,
// the Inspector's evidence rows, MobilityPanel...) used to independently re-run its own effect on every poll
// tick, each firing its own full ~70KB GET /api/v1/model - state.generatedAt changes on the clock on every
// response (see hub.go), not on the model's content, so this happened whether or not the model actually
// changed, and happened once per consumer mounted at the same time (an Inspector panel open next to the
// placement hook was already two). The server was always built to answer "nothing changed" with a cheap 304
// via If-None-Match (see twin.go's Admin.model's own comment) - no client here ever sent it. `lastEtag`
// remembers the ETag per connection so a poll can ask conditionally; `inflight` shares one in-progress
// request across however many components want the model on the same tick, instead of each firing its own.
let lastEtag: { key: string; etag: string | undefined } | undefined
let inflight: { key: string; promise: Promise<void> } | undefined

/**
 * The server's effective model for the organisation in use, loaded while something on screen needs it (the
 * Evidence section) and re-read whenever the state poll brings a new picture - cheaply, via the conditional
 * GET above, so a tick where nothing changed costs a 304 rather than the whole payload.
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
    const connKey = `${c.url}|${c.org ?? ''}`
    const tickKey = `${connKey}|${generatedAt}`
    if (!inflight || inflight.key !== tickKey) {
      const priorEtag = lastEtag?.key === connKey ? lastEtag.etag : undefined
      inflight = {
        key: tickKey,
        promise: api
          .modelIfChanged(c, priorEtag)
          .then((res) => {
            lastEtag = { key: connKey, etag: res ? (res.etag ?? undefined) : priorEtag }
            if (res) useObserved.getState().setModel(res.model)
          })
          .catch(() => {
            /* an older server has no model API, or a network hiccup: the record's own fields are shown instead */
          }),
      }
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
