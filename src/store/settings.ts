import { create } from 'zustand'
import { api, ApiError, type Conn } from '@/lib/api'
import { DEFAULT_SETTINGS, normalizeSettings, type AppSettings } from '@/lib/history'

/** The server's settings (recording, checks, measurements, the external decider). Read by everyone, changed by administrators. */
interface SettingsStore {
  settings: AppSettings
  loaded: boolean
  error?: string
  load: (c: Conn) => Promise<void>
  save: (c: Conn, s: Partial<AppSettings> & { deciderSecret?: string; clearDeciderSecret?: boolean }) => Promise<boolean>
  clear: () => void
}

// server.ts's activate() calls clear() on every organisation switch, before loading or saving the new
// one's settings (see its own wipeLocal()/activate() comments) - bumping epoch there, and checking it
// back here once a load/save's request returns, is the same guard workspace.ts's own epoch already
// applies to its start()/stop(): a load or save left in flight from the organisation just left must not
// write its answer over whatever the switch already put in its place.
let epoch = 0

/** What a save says when the server has a newer document than the one the edit started from. */
export const SETTINGS_CONFLICT = 'Someone else changed the settings; reloaded - try again.'

export const useSettings = create<SettingsStore>((set, get) => ({
  settings: DEFAULT_SETTINGS,
  loaded: false,
  load: async (c) => {
    const myEpoch = epoch
    try {
      const settings = await api.settings(c)
      if (myEpoch !== epoch) return
      set({ settings, loaded: true, error: undefined })
    } catch (e) {
      if (myEpoch !== epoch) return
      set({ error: e instanceof Error ? e.message : 'The settings could not be read.' })
    }
  },
  // Every save goes through here, and says which revision of the document it edited (the one in `s` when the caller started
  // from a copy of the document, else the one held now), so a save made from a stale copy is refused rather than undoing someone else's.
  save: async (c, s) => {
    const myEpoch = epoch
    try {
      const settings = await api.saveSettings(c, s, s.rev ?? get().settings.rev ?? 0)
      if (myEpoch !== epoch) return false
      set({ settings, loaded: true, error: undefined })
      return true
    } catch (e) {
      if (myEpoch !== epoch) return false
      if (e instanceof ApiError && e.status === 409) {
        // The 409 carries the current document; adopt it so the next attempt starts from what is there now. If it
        // does not (a proxy rewrote the answer), read it again.
        if (typeof e.body?.rev === 'number') {
          const { error: _message, ...current } = e.body
          set({ settings: normalizeSettings(current as Partial<AppSettings>), loaded: true, error: SETTINGS_CONFLICT })
        } else {
          await get().load(c)
          if (myEpoch !== epoch) return false
          set({ error: SETTINGS_CONFLICT })
        }
        return false
      }
      set({ error: e instanceof Error ? e.message : 'The settings could not be saved.' })
      return false
    }
  },
  clear: () => {
    epoch++
    set({ settings: DEFAULT_SETTINGS, loaded: false, error: undefined })
  },
}))
