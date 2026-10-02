import { create } from 'zustand'
import { api, type Conn } from '@/lib/api'
import { DEFAULT_SETTINGS, type AppSettings } from '@/lib/history'

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

export const useSettings = create<SettingsStore>((set) => ({
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
  save: async (c, s) => {
    const myEpoch = epoch
    try {
      const settings = await api.saveSettings(c, s)
      if (myEpoch !== epoch) return false
      set({ settings, loaded: true, error: undefined })
      return true
    } catch (e) {
      if (myEpoch !== epoch) return false
      set({ error: e instanceof Error ? e.message : 'The settings could not be saved.' })
      return false
    }
  },
  clear: () => {
    epoch++
    set({ settings: DEFAULT_SETTINGS, loaded: false, error: undefined })
  },
}))
