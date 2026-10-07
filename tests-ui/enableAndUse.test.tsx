import { act, render, renderHook, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, test, vi } from 'vitest'
import DestinationPicker, { useEnableAndUse, type FusionControls } from '@/components/telemetry/DestinationPicker'
import { buildDestinationCatalog, fusionForCatalog, layoutDestinations, type DestinationCatalogEntry } from '@/lib/destinationCatalog'
import type { FusionStatus } from '@/lib/api'
import type { RegionalOperator } from '@/lib/types'

// "Enable and use" waits for FUSION to come up and then picks it. A person who chose something else by hand in the meantime must keep that
// choice: FUSION coming up later (or recovering from a failure) may not silently overwrite it.

const fusionEntry = (usable: boolean): DestinationCatalogEntry => ({ kind: 'fusion', id: 'op-central', label: 'FUSION', fusion: { usable } } as unknown as DestinationCatalogEntry)
const controls = (): FusionControls => ({ busy: false, enable: vi.fn(async () => undefined) })

describe('useEnableAndUse', () => {
  test('baseline: once FUSION is usable after Enable and use, it is handed to onUsable', async () => {
    const onUsable = vi.fn()
    const { result, rerender } = renderHook(({ usable }) => useEnableAndUse([fusionEntry(usable)], controls(), onUsable), { initialProps: { usable: false } })
    await act(async () => { await result.current!.enable!() })
    expect(onUsable).not.toHaveBeenCalled()
    rerender({ usable: true })
    expect(onUsable).toHaveBeenCalledTimes(1)
  })

  test('a manual pick in between cancels it: FUSION coming up afterwards picks nothing', async () => {
    const onUsable = vi.fn()
    const { result, rerender } = renderHook(({ usable }) => useEnableAndUse([fusionEntry(usable)], controls(), onUsable), { initialProps: { usable: false } })
    await act(async () => { await result.current!.enable!() })
    act(() => result.current!.cancel!())
    rerender({ usable: true })
    expect(onUsable).not.toHaveBeenCalled()
  })
})

describe('DestinationPicker', () => {
  test('choosing a row by hand cancels a pending Enable and use', async () => {
    const eu: RegionalOperator = { id: 'op-eu', orgId: 'o', name: 'eu-regional', status: 'active', sourceClusterIds: [], destination: { kind: 'external', endpoint: 'x:1' }, createdAt: '', createdBy: '' }
    const status: FusionStatus = { available: true, state: 'off', components: [{ component: 'central', label: 'Central', desired: 0, ready: 0 }] }
    const catalog = buildDestinationCatalog({
      operators: [eu], enabledModalities: new Set(['metrics', 'logs', 'traces']), quickStartBackends: [], isAdmin: true,
      fusion: fusionForCatalog({ status, operators: [], destinations: [], isAdmin: true }),
    })
    const fusion: FusionControls = { busy: false, enable: vi.fn(async () => undefined), cancel: vi.fn() }
    const onPick = vi.fn()
    render(<DestinationPicker catalog={catalog} layout={layoutDestinations(catalog)} activeKey={null} onPick={onPick} testIdPrefix="t" fusion={fusion} />)
    await userEvent.setup().click(await screen.findByTestId('t-destination-operator-op-eu'))
    expect(fusion.cancel).toHaveBeenCalledTimes(1)
    expect(onPick).toHaveBeenCalledTimes(1)
  })
})
