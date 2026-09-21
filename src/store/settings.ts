import { create } from 'zustand'
import { api, type Conn } from '@/lib/api'
import { DEFAULT_SETTINGS, type AppSettings } from '@/lib/history'

/** The server's settings (recording, checks, measurements, the external decider). Read by everyone, changed by administrators. */
interface SettingsStore {
  settings: AppSettings
  loaded: boolean
  error?: string
  load: (c: Conn) => Promise<void>
  save: (c: Conn, s: Partial<AppSettings>) => Promise<boolean>
  clear: () => void
}

export const useSettings = create<SettingsStore>((set) => ({
  settings: DEFAULT_SETTINGS,
  loaded: false,
  load: async (c) => {
    try {
      set({ settings: await api.settings(c), loaded: true, error: undefined })
    } catch (e) {
      set({ error: e instanceof Error ? e.message : 'The settings could not be read.' })
    }
  },
  save: async (c, s) => {
    try {
      set({ settings: await api.saveSettings(c, s), loaded: true, error: undefined })
      return true
    } catch (e) {
      set({ error: e instanceof Error ? e.message : 'The settings could not be saved.' })
      return false
    }
  },
  clear: () => set({ settings: DEFAULT_SETTINGS, loaded: false, error: undefined }),
}))
