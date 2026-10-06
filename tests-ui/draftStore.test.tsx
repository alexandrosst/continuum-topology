import { afterEach, describe, expect, test, vi } from 'vitest'
import { clearDraft, loadDraft, saveDraft } from '@/lib/draftStore'
import { emptyTelemetry } from '@/lib/install'

describe('draftStore', () => {
  afterEach(() => vi.restoreAllMocks())
  const draft = { ...emptyTelemetry, resourceUsage: true, exportEndpoint: 'gw.example.com:4317' }

  test('a draft comes back for the same agent and the same install, and not for another agent', () => {
    saveDraft('a1', 'resourceUsage', draft)
    expect(loadDraft('a1', 'resourceUsage')).toEqual(draft)
    expect(loadDraft('a2', 'resourceUsage')).toBeNull()
  })

  test('a draft made against an install that has since changed is not offered back', () => {
    saveDraft('a1', 'resourceUsage', draft)
    expect(loadDraft('a1', 'resourceUsage,traces')).toBeNull()
  })

  test('a field added since it was saved has its default instead of being undefined', () => {
    sessionStorage.setItem('ikhnos.telemetry-draft.a1', JSON.stringify({ basis: 'b', draft: { resourceUsage: true } }))
    expect(loadDraft('a1', 'b')).toEqual({ ...emptyTelemetry, resourceUsage: true })
  })

  test('clear forgets it; garbage is read as nothing', () => {
    saveDraft('a1', 'b', draft)
    clearDraft('a1')
    expect(loadDraft('a1', 'b')).toBeNull()
    sessionStorage.setItem('ikhnos.telemetry-draft.a1', '{not json')
    expect(loadDraft('a1', 'b')).toBeNull()
  })

  test('storage that throws is the same as no storage: nothing is kept and nothing breaks', () => {
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => { throw new Error('full') })
    vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => { throw new Error('blocked') })
    vi.spyOn(Storage.prototype, 'removeItem').mockImplementation(() => { throw new Error('blocked') })
    expect(() => saveDraft('a1', 'b', draft)).not.toThrow()
    expect(loadDraft('a1', 'b')).toBeNull()
    expect(() => clearDraft('a1')).not.toThrow()
  })
})
