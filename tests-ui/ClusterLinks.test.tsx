import { fireEvent, render, screen, within } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import ClusterLinks from '@/components/ClusterLinks'
import { DEFAULT_SETTINGS, type LinkPair } from '@/lib/history'
import { useServer } from '@/store/server'
import { useSettings } from '@/store/settings'
import { useRawTopology } from '@/store/topology'

const cluster = (id: string, name: string) => ({ id, name })
const agent = (clusterId: string, accessTier = 2) => ({
  id: `ag-${clusterId}`, orgId: 'o', name: clusterId, clusterId, version: '1', accessTier, status: 'approved', fingerprint: '', modules: [], connected: true,
  observer: { lastReport: '', collectors: [{ node: 'n', method: 'ebpf', bytesKnown: true }], lost: 0 },
})

let saved: LinkPair[] | undefined
const save = vi.fn(async (_c: unknown, s: { links?: LinkPair[] }) => {
  saved = s.links
  useSettings.setState({ settings: { ...useSettings.getState().settings, links: s.links ?? [] } })
  return true
})

function seed(role: 'admin' | 'viewer', links: LinkPair[]) {
  saved = undefined
  save.mockClear()
  useServer.setState({ status: 'connected', role, orgId: 'org1', orgs: [{ id: 'org1', name: 'Acme', role }], checked: true } as never)
  useRawTopology.setState({
    clusters: [cluster('c-eu', 'cloud-eu'), cluster('c-us', 'cloud-us'), cluster('c-gr', 'edge-athens')],
    agents: [agent('c-eu'), agent('c-us'), agent('c-gr', 1)],
  } as never)
  useSettings.setState({ settings: { ...DEFAULT_SETTINGS, links }, loaded: true, save: save as never })
}

const pick = (label: string, option: string) => {
  fireEvent.click(screen.getByRole('combobox', { name: label }))
  fireEvent.click(screen.getByRole('option', { name: option }))
}

describe('ClusterLinks', () => {
  beforeEach(() => seed('admin', []))

  it('shows an empty state and nothing to edit for a viewer', () => {
    seed('viewer', [{ a: 'c-eu', b: 'c-us' }])
    render(<ClusterLinks />)
    expect(screen.getByTestId('pair-row')).toHaveTextContent('cloud-eu')
    expect(screen.queryByTestId('add-pair')).toBeNull()
    expect(screen.queryByRole('button', { name: /Edit/ })).toBeNull()
  })

  it('lists what is on for each pair, in words', () => {
    seed('admin', [
      { a: 'c-eu', b: 'c-us', throughput: { intervalMinutes: 360, budgetMB: 500 } },
      { a: 'c-eu', b: 'c-gr', noProbe: true },
    ])
    render(<ClusterLinks />)
    const rows = screen.getAllByTestId('pair-row')
    expect(rows).toHaveLength(2)
    expect(within(rows[0]).getByText('every 6 hours')).toBeInTheDocument()
    expect(within(rows[0]).getByText('up to 500 MB a day')).toBeInTheDocument()
    expect(within(rows[0]).getByText('tests run from cloud-eu')).toBeInTheDocument()
    expect(within(rows[1]).getByText('real traffic only')).toBeInTheDocument()
  })

  it('adds a pair with probing on and throughput off, ids in order', async () => {
    render(<ClusterLinks />)
    expect(screen.getByTestId('no-pairs')).toBeInTheDocument()
    fireEvent.click(screen.getByTestId('add-pair'))
    pick('First cluster', 'cloud-us')
    pick('Second cluster', 'cloud-eu')
    fireEvent.click(screen.getByTestId('confirm-add-pair'))
    await vi.waitFor(() => expect(saved).toEqual([{ a: 'c-eu', b: 'c-us' }]))
  })

  it('warns, without blocking, about a cluster below access level 2', () => {
    render(<ClusterLinks />)
    fireEvent.click(screen.getByTestId('add-pair'))
    pick('First cluster', 'cloud-eu')
    pick('Second cluster', 'edge-athens')
    expect(screen.getByTestId('link-checks')).toHaveTextContent('probing needs level 2')
    expect(screen.getByTestId('confirm-add-pair')).toBeEnabled()
  })

  it('refuses the same cluster twice and a pair already listed', () => {
    seed('admin', [{ a: 'c-eu', b: 'c-us' }])
    render(<ClusterLinks />)
    fireEvent.click(screen.getByTestId('add-pair'))
    pick('First cluster', 'cloud-us')
    pick('Second cluster', 'cloud-us')
    expect(screen.getByRole('alert')).toHaveTextContent('two different clusters')
    expect(screen.getByTestId('confirm-add-pair')).toBeDisabled()
    pick('Second cluster', 'cloud-eu')
    expect(screen.getByRole('alert')).toHaveTextContent('already listed')
    expect(screen.getByTestId('confirm-add-pair')).toBeDisabled()
  })

  it('turns throughput on from a named amount and saves the numbers', async () => {
    seed('admin', [{ a: 'c-eu', b: 'c-us' }])
    render(<ClusterLinks />)
    fireEvent.click(screen.getByRole('button', { name: 'Edit cloud-eu and cloud-us' }))
    fireEvent.click(screen.getByRole('switch', { name: 'Throughput tests' }))
    expect(screen.getByTestId('tp-cost')).toHaveTextContent('About 4 sets of tests a day')
    fireEvent.click(screen.getByRole('button', { name: 'Light' }))
    expect(screen.getByTestId('tp-budget')).toHaveValue('50')
    fireEvent.click(screen.getByRole('switch', { name: 'Probing' }))
    fireEvent.click(screen.getByTestId('save-pair'))
    await vi.waitFor(() => expect(saved).toEqual([{ a: 'c-eu', b: 'c-us', noProbe: true, throughput: { intervalMinutes: 1440, budgetMB: 50 } }]))
  })

  it('holds custom numbers to what the agents accept', () => {
    seed('admin', [{ a: 'c-eu', b: 'c-us', throughput: { intervalMinutes: 360, budgetMB: 500 } }])
    render(<ClusterLinks />)
    fireEvent.click(screen.getByRole('button', { name: 'Edit cloud-eu and cloud-us' }))
    fireEvent.click(screen.getByRole('button', { name: 'Custom' }))
    fireEvent.change(screen.getByTestId('tp-hours'), { target: { value: '0.01' } })
    expect(screen.getByTestId('save-pair')).toBeDisabled()
    fireEvent.change(screen.getByTestId('tp-hours'), { target: { value: '0.5' } })
    fireEvent.change(screen.getByTestId('tp-budget'), { target: { value: '2048' } })
    expect(screen.getByTestId('save-pair')).toBeEnabled()
  })

  it('removes a pair after confirming', async () => {
    seed('admin', [{ a: 'c-eu', b: 'c-us' }, { a: 'c-eu', b: 'c-gr' }])
    render(<ClusterLinks />)
    fireEvent.click(screen.getByRole('button', { name: 'Edit cloud-eu and cloud-us' }))
    fireEvent.click(screen.getByRole('button', { name: 'Remove pair' }))
    fireEvent.click(screen.getAllByRole('button', { name: 'Remove pair' }).at(-1)!)
    await vi.waitFor(() => expect(saved).toEqual([{ a: 'c-eu', b: 'c-gr' }]))
  })
})
