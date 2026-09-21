import { create } from 'zustand'
import { api, ApiError, type Conn } from '@/lib/api'
import { seedTopology } from '@/lib/seed'
import { rehydrate, toDeclared } from '@/lib/declared'
import type { TopologyFile } from '@/lib/types'
import { exportTopology, normalize, useRawTopology } from './topology'

/**
 * Keeps the topology in this browser and the workspace document on the server in step.
 *
 *  - On sign-in the server's copy replaces what the browser holds.
 *  - Every change is saved after a short pause, guarded by the revision this browser last saw, so
 *    two people editing at once are told rather than silently overwriting each other.
 *  - The server's revision is polled; someone else's save is loaded automatically when this browser
 *    has nothing unsaved, and reported as a conflict when it does.
 *  - Viewers never save. What they change stays in their browser until they reload.
 */
export type SyncStatus = 'off' | 'loading' | 'saved' | 'dirty' | 'saving' | 'conflict' | 'error' | 'readonly' | 'choose'

export interface LocalSummary {
  clusters: number
  nodes: number
  services: number
  devices: number
  applications: number
}

interface Workspace {
  status: SyncStatus
  rev: number
  updatedBy?: string
  updatedAt?: string
  /** A newer revision exists on the server while this browser has unsaved changes. */
  conflict?: { rev: number; updatedBy: string; updatedAt: string }
  error?: string
  /** The server is empty but this browser holds work of its own: ask what to do with it. */
  local?: LocalSummary
  /** What the server changed in the document when it upgraded it (observed records it removed from an older workspace). */
  note?: string
  dismissNote: () => void

  start: (conn: Conn, canWrite: boolean) => Promise<void>
  stop: (flush: boolean) => Promise<void>
  poll: () => Promise<void>
  adoptLocal: () => Promise<void>
  startEmpty: () => Promise<void>
  useTheirs: () => Promise<void>
  overwrite: () => Promise<void>
  saveNow: () => Promise<void>
}

const SAVE_DELAY_MS = 1500
const PENDING_PREFIX = 'continuum-workspace-pending'
/** One marker per organisation: unsaved edits made in one are never applied on top of another. */
const pendingKey = () => `${PENDING_PREFIX}/${conn?.org ?? ''}`

/**
 * Remembers, across a reload, that this browser holds edits the server has not seen yet and which
 * server revision they were made on top of. Without it a reload inside the save delay would replace
 * the person's latest change with the server's older copy.
 */
const pending = {
  read(): { rev: number } | null {
    try {
      const v = JSON.parse(localStorage.getItem(pendingKey()) ?? 'null')
      return v && typeof v.rev === 'number' ? { rev: v.rev } : null
    } catch {
      return null
    }
  },
  set(rev: number) {
    try {
      localStorage.setItem(pendingKey(), JSON.stringify({ rev }))
    } catch {
      /* storage unavailable: the delay is the only exposure */
    }
  },
  clear() {
    try {
      localStorage.removeItem(pendingKey())
    } catch {
      /* nothing to clear */
    }
  },
}

/** Forget unsaved edits for good: used when the person signs out and the browser copy is wiped. */
export const forgetPending = () => {
  try {
    const keys: string[] = []
    for (let i = 0; i < localStorage.length; i++) keys.push(localStorage.key(i) ?? '')
    for (const k of keys) if (k.startsWith(PENDING_PREFIX)) localStorage.removeItem(k)
  } catch {
    /* storage unavailable: nothing was kept */
  }
}

let conn: Conn | null = null
let canWrite = false
let lastSaved = '' // serialized document as the server last held it
let timer: ReturnType<typeof setTimeout> | undefined
let unsubscribe: (() => void) | undefined
let saving: Promise<void> | undefined

/**
 * What is saved to the server is what people declared. What agents observe (clusters, nodes, namespaces and
 * services they discovered, heartbeats, certificates) and the server's own audit events come from the server on
 * every refresh, so saving them would make every open browser rewrite the workspace on every tick and fight the
 * others over it. What a person said about a discovered record is saved as a ref to it.
 */
const isHumanEvent = (e: { id: string }) => e.id.startsWith('ev-')
const forWorkspace = (m: TopologyFile): TopologyFile => ({ ...toDeclared(m).model, schemaVersion: m.schemaVersion })

/**
 * Key order is not meaningful and the server does not preserve it, so documents are compared (and
 * sent) with keys sorted. Otherwise merely reading the workspace back would look like an edit.
 */
const canonical = (v: unknown): unknown =>
  Array.isArray(v)
    ? v.map(canonical)
    : v && typeof v === 'object'
      ? Object.fromEntries(Object.keys(v).sort().map((k) => [k, canonical((v as Record<string, unknown>)[k])]))
      : v

const serialize = () => JSON.stringify(canonical(forWorkspace(exportTopology())))

/** What a person made here. Records an agent discovered belong to the server and are never uploaded, so they are not "work" either. */
const own = <T extends { deletedAt?: string; source?: string }>(l: T[]) => l.filter((x) => !x.deletedAt && x.source !== 'discovered')

const summary = (): LocalSummary => {
  const m = useRawTopology.getState()
  return { clusters: own(m.clusters).length, nodes: own(m.nodes).length, services: own(m.services).length, devices: own(m.devices).length, applications: own(m.applications).length }
}

/**
 * An empty model, or the untouched sample data a fresh browser starts with, is not "work" worth uploading. What
 * agents discovered does not count: a person who connected a cluster and reloaded has made nothing of their own.
 */
function isUntouchedSample() {
  const s = summary()
  if (s.clusters + s.nodes + s.services + s.devices + s.applications === 0) return true
  const m = useRawTopology.getState()
  const seed = seedTopology()
  const ids = (l: { id: string }[]) => l.map((x) => x.id).sort().join(',')
  return ids(own(m.clusters)) === ids(own(seed.clusters)) && ids(own(m.nodes)) === ids(own(seed.nodes)) && ids(own(m.services)) === ids(own(seed.services)) && ids(own(m.devices)) === ids(own(seed.devices)) && ids(own(m.applications)) === ids(own(seed.applications))
}

export const useWorkspace = create<Workspace>((set, get) => {
  const schedule = () => {
    clearTimeout(timer)
    timer = setTimeout(() => void get().saveNow(), SAVE_DELAY_MS)
  }

  const onLocalChange = () => {
    const { status } = get()
    if (!conn || !canWrite || status === 'loading' || status === 'choose' || status === 'conflict' || status === 'off') return
    if (serialize() === lastSaved) {
      if (status === 'dirty') set({ status: 'saved' })
      pending.clear()
      return
    }
    if (status !== 'saving') set({ status: 'dirty' })
    pending.set(get().rev)
    schedule()
  }

  const watch = () => {
    unsubscribe?.()
    unsubscribe = useRawTopology.subscribe(onLocalChange)
  }

  /**
   * `keep`: this browser is already showing this workspace (a save by someone else arrived), so the discovered records it
   * holds from the server stay, with the workspace's refs applied to them. Without it (signing in, switching
   * organisation) nothing observed carries over: what a previous session or organisation reported is not this one's,
   * and the refresh that follows brings the right records in.
   */
  const replaceLocal = (data: unknown, keep = false) => {
    const now = useRawTopology.getState()
    const loaded = normalize(data)
    const incoming = { ...loaded, agents: now.agents, auditLog: [...now.auditLog.filter((e) => !isHumanEvent(e)), ...loaded.auditLog] }
    useRawTopology.getState().replaceAll(keep ? rehydrate(incoming, now) : incoming)
    lastSaved = serialize()
    pending.clear()
  }

  return {
    status: 'off',
    rev: 0,
    dismissNote: () => set({ note: undefined }),

    start: async (c, write) => {
      conn = c
      canWrite = write
      set({ status: 'loading', error: undefined, conflict: undefined, local: undefined })
      try {
        const doc = await api.workspace(c)
        const unsaved = write ? pending.read() : null
        if (!write) pending.clear()
        if (unsaved && doc.rev > 0 && doc.data !== undefined) {
          // Edits from before a reload or a session timeout: keep them rather than replacing them.
          lastSaved = '' // the server's copy is not what this browser holds
          set({ rev: unsaved.rev, updatedBy: doc.updatedBy, updatedAt: doc.updatedAt })
          if (unsaved.rev === doc.rev) {
            set({ status: 'dirty' })
            schedule()
          } else {
            set({ status: 'conflict', conflict: { rev: doc.rev, updatedBy: doc.updatedBy, updatedAt: doc.updatedAt } })
          }
        } else if (doc.rev > 0 && doc.data !== undefined) {
          replaceLocal(doc.data)
          set({ status: write ? 'saved' : 'readonly', rev: doc.rev, updatedBy: doc.updatedBy, updatedAt: doc.updatedAt, note: doc.note })
        } else if (isUntouchedSample()) {
          // Nothing of value here: begin with an empty workspace instead of the sample.
          useRawTopology.getState().clear()
          lastSaved = serialize()
          set({ status: write ? 'saved' : 'readonly', rev: 0 })
        } else if (write) {
          set({ status: 'choose', rev: 0, local: summary() })
        } else {
          useRawTopology.getState().clear()
          lastSaved = serialize()
          set({ status: 'readonly', rev: 0 })
        }
        watch()
      } catch (e) {
        set({ status: 'error', error: e instanceof Error ? e.message : 'Could not load the workspace.' })
      }
    },

    stop: async (flush) => {
      clearTimeout(timer)
      unsubscribe?.()
      unsubscribe = undefined
      if (flush && conn && canWrite && get().status === 'dirty') await get().saveNow()
      conn = null
      set({ status: 'off', rev: 0, conflict: undefined, local: undefined, error: undefined, updatedBy: undefined, updatedAt: undefined, note: undefined })
    },

    saveNow: async () => {
      if (saving) return saving
      const c = conn
      if (!c || !canWrite) return
      const attempt = async () => {
        try {
          clearTimeout(timer)
          const body = serialize()
          if (body === lastSaved) {
            set({ status: 'saved' })
            return
          }
          set({ status: 'saving' })
          const meta = await api.saveWorkspace(c, get().rev, JSON.parse(body))
          lastSaved = body
          pending.clear()
          // Edits made while the request was in flight are still pending.
          set({ rev: meta.rev, updatedBy: meta.updatedBy, updatedAt: meta.updatedAt, status: serialize() === lastSaved ? 'saved' : 'dirty', error: undefined })
          if (get().status === 'dirty') {
            pending.set(meta.rev)
            schedule()
          }
        } catch (e) {
          if (e instanceof ApiError && e.status === 409 && e.body) {
            set({ status: 'conflict', conflict: { rev: Number(e.body.rev), updatedBy: String(e.body.updatedBy ?? ''), updatedAt: String(e.body.updatedAt ?? '') } })
          } else if (e instanceof ApiError && e.status === 403) {
            canWrite = false
            set({ status: 'readonly', error: 'You no longer have permission to save.' })
          } else {
            // Network trouble: keep the changes, try again shortly.
            set({ status: 'error', error: e instanceof Error ? e.message : 'Could not save.' })
            schedule()
          }
        }
      }
      // Cleared in a microtask, after `saving` is assigned even when attempt() finishes synchronously.
      saving = attempt().finally(() => {
        saving = undefined
      })
      return saving
    },

    poll: async () => {
      const c = conn
      const { status, rev } = get()
      if (!c || status === 'loading' || status === 'choose' || status === 'saving' || status === 'off') return
      try {
        const meta = await api.workspaceMeta(c)
        if (meta.rev <= rev) return
        if (status === 'dirty' || status === 'conflict' || serialize() !== lastSaved) {
          if (canWrite) set({ status: 'conflict', conflict: { rev: meta.rev, updatedBy: meta.updatedBy, updatedAt: meta.updatedAt } })
          else await get().useTheirs()
          return
        }
        await get().useTheirs()
      } catch {
        /* the next poll tries again; connection trouble is shown by the server store */
      }
    },

    useTheirs: async () => {
      const c = conn
      if (!c) return
      const doc = await api.workspace(c)
      if (doc.data !== undefined) replaceLocal(doc.data, true)
      set({ status: canWrite ? 'saved' : 'readonly', rev: doc.rev, updatedBy: doc.updatedBy, updatedAt: doc.updatedAt, conflict: undefined, error: undefined })
    },

    overwrite: async () => {
      const c = conn
      if (!c) return
      const latest = await api.workspaceMeta(c)
      set({ rev: latest.rev, conflict: undefined, status: 'dirty' })
      await get().saveNow()
    },

    adoptLocal: async () => {
      lastSaved = '' // the server holds nothing yet
      set({ local: undefined, status: 'dirty' })
      await get().saveNow()
    },

    startEmpty: async () => {
      useRawTopology.getState().clear()
      lastSaved = serialize()
      pending.clear()
      set({ local: undefined, status: 'saved' })
    },
  }
})
