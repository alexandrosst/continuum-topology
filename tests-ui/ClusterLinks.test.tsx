import { fireEvent, render, screen, within } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import ClusterLinks from '@/components/ClusterLinks'
import { api } from '@/lib/api'
import { LINK_REQUIREMENT, type LinksLatest, type SetupStep } from '@/lib/clusterLinks'
import { DEFAULT_SETTINGS, type LinkPair } from '@/lib/history'
import { useServer } from '@/store/server'
import { SETTINGS_CONFLICT, useSettings } from '@/store/settings'
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

function seed(role: 'admin' | 'viewer', links: LinkPair[], opts: { loaded?: boolean } = {}) {
  saved = undefined
  save.mockClear()
  useServer.setState({ status: 'connected', role, orgId: 'org1', orgs: [{ id: 'org1', name: 'Acme', role }], checked: true } as never)
  useRawTopology.setState({
    clusters: [cluster('c-eu', 'cloud-eu'), cluster('c-us', 'cloud-us'), cluster('c-gr', 'edge-athens')],
    agents: [agent('c-eu'), agent('c-us'), agent('c-gr', 1)],
  } as never)
  useSettings.setState({ settings: { ...DEFAULT_SETTINGS, links }, loaded: opts.loaded ?? true, error: undefined, save: save as never })
}

const pick = (label: string, option: string) => {
  fireEvent.click(screen.getByRole('combobox', { name: label }))
  fireEvent.click(screen.getByRole('option', { name: option }))
}

const NOW = new Date().toISOString()
const answer = (o: Partial<LinksLatest>): LinksLatest => ({ available: true, pairs: [], notes: [], setup: [], at: NOW, ...o })

describe('ClusterLinks', () => {
  beforeEach(() => {
    seed('admin', [])
    vi.restoreAllMocks()
    vi.spyOn(api, 'linksLatest').mockResolvedValue(answer({}))
  })

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
      { a: 'c-us', b: 'c-gr' },
    ])
    render(<ClusterLinks />)
    const rows = screen.getAllByTestId('pair-row')
    expect(rows).toHaveLength(3)
    expect(within(rows[2]).getByText('probing runs from edge-athens; no throughput tests')).toBeInTheDocument()
    expect(within(rows[0]).getByText('every 6 hours')).toBeInTheDocument()
    expect(within(rows[0]).getByText('up to 500 MB a day')).toBeInTheDocument()
    expect(within(rows[0]).getByText('probing and tests run from cloud-eu')).toBeInTheDocument()
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

  it('pins a pair to one kind of address from the edit panel, saves it with the other choices and says so in the row', async () => {
    seed('admin', [{ a: 'c-eu', b: 'c-us', throughput: { intervalMinutes: 360, budgetMB: 500 } }])
    const { unmount } = render(<ClusterLinks />)
    fireEvent.click(screen.getByRole('button', { name: 'Edit cloud-eu and cloud-us' }))
    const select = screen.getByRole('combobox', { name: 'Measure through' })
    expect(select).toHaveTextContent('The address your traffic uses (recommended)')
    expect(screen.getByText(/follow the address your clusters’ real traffic is seen using/)).toBeInTheDocument()
    pick('Measure through', 'Mesh addresses (100.64.x.x and similar)')
    fireEvent.click(screen.getByTestId('save-pair'))
    await vi.waitFor(() => expect(saved).toEqual([{ a: 'c-eu', b: 'c-us', via: 'mesh', throughput: { intervalMinutes: 360, budgetMB: 500 } }]))
    unmount()
    // the saved settings are what the row reads
    render(<ClusterLinks />)
    expect(within(screen.getByTestId('pair-row')).getByText('probing and tests run from cloud-eu, through mesh addresses')).toBeInTheDocument()
  })

  it('shows a pinned pair\'s choice when editing, and goes back to following the traffic without leaving a trace in the settings', async () => {
    seed('admin', [{ a: 'c-eu', b: 'c-us', via: 'private' }])
    render(<ClusterLinks />)
    expect(within(screen.getByTestId('pair-row')).getByText('probing runs from cloud-eu, through private addresses; no throughput tests')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Edit cloud-eu and cloud-us' }))
    expect(screen.getByRole('combobox', { name: 'Measure through' })).toHaveTextContent('Private addresses')
    pick('Measure through', 'The address your traffic uses (recommended)')
    fireEvent.click(screen.getByTestId('save-pair'))
    await vi.waitFor(() => expect(saved).toEqual([{ a: 'c-eu', b: 'c-us' }]))
  })

  it('keeps a pair\'s pin when something else about it is changed', async () => {
    seed('admin', [{ a: 'c-eu', b: 'c-us', via: 'public' }])
    render(<ClusterLinks />)
    fireEvent.click(screen.getByRole('button', { name: 'Edit cloud-eu and cloud-us' }))
    fireEvent.click(screen.getByRole('switch', { name: 'Probing' }))
    fireEvent.click(screen.getByTestId('save-pair'))
    await vi.waitFor(() => expect(saved).toEqual([{ a: 'c-eu', b: 'c-us', noProbe: true, via: 'public' }]))
  })

  it('offers the choice when adding a pair, following the traffic unless one is picked', async () => {
    render(<ClusterLinks />)
    fireEvent.click(screen.getByTestId('add-pair'))
    pick('First cluster', 'cloud-us')
    pick('Second cluster', 'cloud-eu')
    expect(screen.getByRole('combobox', { name: 'Measure through' })).toHaveTextContent('recommended')
    pick('Measure through', 'Public addresses')
    fireEvent.click(screen.getByTestId('confirm-add-pair'))
    await vi.waitFor(() => expect(saved).toEqual([{ a: 'c-eu', b: 'c-us', via: 'public' }]))
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

  it('offers nothing to change, and sends nothing, until the settings have loaded', () => {
    seed('admin', [], { loaded: false })
    render(<ClusterLinks />)
    expect(screen.queryByTestId('add-pair')).toBeNull()
    expect(screen.queryByTestId('no-pairs')).toBeNull()
    expect(screen.queryByTestId('pair-row')).toBeNull()
    expect(save).not.toHaveBeenCalled()
  })

  it('says when the settings could not be read, and retries', () => {
    seed('admin', [], { loaded: false })
    useSettings.setState({ error: 'boom' })
    const load = vi.fn(async () => {})
    useSettings.setState({ load: load as never })
    render(<ClusterLinks />)
    expect(screen.getByTestId('links-load-error')).toHaveTextContent('boom')
    expect(screen.queryByTestId('no-pairs')).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: 'Retry' }))
    expect(load).toHaveBeenCalledTimes(1)
  })

  it('shows the reason and keeps the panel open when a save fails', async () => {
    seed('admin', [{ a: 'c-eu', b: 'c-us' }])
    save.mockImplementationOnce(async () => {
      useSettings.setState({ error: 'too many pairs' })
      return false
    })
    render(<ClusterLinks />)
    fireEvent.click(screen.getByRole('button', { name: 'Edit cloud-eu and cloud-us' }))
    fireEvent.click(screen.getByRole('switch', { name: 'Probing' }))
    fireEvent.click(screen.getByTestId('save-pair'))
    expect(await screen.findByText('too many pairs')).toBeInTheDocument()
    expect(screen.getByTestId('save-pair')).toBeEnabled()
  })

  it('cannot be cancelled or dismissed while a save is in flight', async () => {
    seed('admin', [])
    let finish: (ok: boolean) => void = () => {}
    save.mockImplementationOnce(() => new Promise<boolean>((r) => { finish = r }))
    render(<ClusterLinks />)
    fireEvent.click(screen.getByTestId('add-pair'))
    pick('First cluster', 'cloud-us')
    pick('Second cluster', 'cloud-eu')
    fireEvent.click(screen.getByTestId('confirm-add-pair'))
    expect(screen.getByRole('button', { name: 'Cancel' })).toBeDisabled()
    fireEvent.keyDown(document, { key: 'Escape' })
    expect(screen.getByTestId('confirm-add-pair')).toBeInTheDocument()
    finish(true)
    await vi.waitFor(() => expect(screen.queryByTestId('confirm-add-pair')).toBeNull())
  })

  it('builds the saved list from the store at save time, not from the render before', async () => {
    seed('admin', [{ a: 'c-eu', b: 'c-gr' }])
    render(<ClusterLinks />)
    fireEvent.click(screen.getByTestId('add-pair'))
    pick('First cluster', 'cloud-us')
    pick('Second cluster', 'cloud-eu')
    useSettings.setState({ settings: { ...DEFAULT_SETTINGS, links: [{ a: 'c-eu', b: 'c-gr' }, { a: 'c-gr', b: 'c-us' }] } })
    fireEvent.click(screen.getByTestId('confirm-add-pair'))
    await vi.waitFor(() => expect(saved).toEqual([{ a: 'c-eu', b: 'c-gr' }, { a: 'c-gr', b: 'c-us' }, { a: 'c-eu', b: 'c-us' }]))
  })

  it('does not pin explicit throughput numbers on a pair that has none set', async () => {
    seed('admin', [{ a: 'c-eu', b: 'c-us', throughput: {} }])
    render(<ClusterLinks />)
    fireEvent.click(screen.getByRole('button', { name: 'Edit cloud-eu and cloud-us' }))
    fireEvent.click(screen.getByRole('switch', { name: 'Probing' }))
    fireEvent.click(screen.getByTestId('save-pair'))
    await vi.waitFor(() => expect(saved).toEqual([{ a: 'c-eu', b: 'c-us', noProbe: true, throughput: {} }]))
  })

  it('marks a row whose clusters cannot be measured yet', () => {
    seed('admin', [{ a: 'c-eu', b: 'c-us' }, { a: 'c-eu', b: 'c-gr' }])
    render(<ClusterLinks />)
    const rows = screen.getAllByTestId('pair-row')
    expect(within(rows[0]).queryByTestId('pair-warning')).toBeNull()
    expect(within(rows[1]).getByTestId('pair-warning')).toHaveTextContent('edge-athens is at access level 1')
  })

  it('ties each throughput error to its own field', () => {
    seed('admin', [{ a: 'c-eu', b: 'c-us', throughput: { intervalMinutes: 360, budgetMB: 500 } }])
    render(<ClusterLinks />)
    fireEvent.click(screen.getByRole('button', { name: 'Edit cloud-eu and cloud-us' }))
    fireEvent.click(screen.getByRole('button', { name: 'Custom' }))
    fireEvent.change(screen.getByTestId('tp-hours'), { target: { value: '0.01' } })
    const hours = screen.getByTestId('tp-hours')
    expect(hours).toHaveAttribute('aria-invalid', 'true')
    expect(document.getElementById(hours.getAttribute('aria-describedby')!)).toHaveTextContent('time between tests')
    expect(screen.getByTestId('tp-budget')).toHaveAttribute('aria-invalid', 'false')
    expect(screen.getByTestId('tp-budget')).not.toHaveAttribute('aria-describedby')
  })

  it('describes a switch by its explanation', () => {
    seed('admin', [{ a: 'c-eu', b: 'c-us' }])
    render(<ClusterLinks />)
    fireEvent.click(screen.getByRole('button', { name: 'Edit cloud-eu and cloud-us' }))
    expect(screen.getByRole('switch', { name: 'Probing' })).toHaveAccessibleDescription(/Small packets, both ways/)
  })

  it('shows a conflict with another administrator\'s change and keeps the dialog open', async () => {
    // What the store does on a 409: adopt the document that is there now, say so, and report the save as not made.
    save.mockImplementationOnce(async () => {
      useSettings.setState({ settings: { ...useSettings.getState().settings, links: [{ a: 'c-eu', b: 'c-gr' }] }, error: SETTINGS_CONFLICT })
      return false
    })
    render(<ClusterLinks />)
    fireEvent.click(screen.getByTestId('add-pair'))
    pick('First cluster', 'cloud-us')
    pick('Second cluster', 'cloud-eu')
    fireEvent.click(screen.getByTestId('confirm-add-pair'))
    expect(await screen.findByText(SETTINGS_CONFLICT)).toBeInTheDocument()
    expect(screen.getByTestId('confirm-add-pair')).toBeEnabled()
    expect(saved).toBeUndefined()
  })

  describe('latest result', () => {
    it('shows the figures of each pair, with the loss in the tone of the worse direction', async () => {
      seed('viewer', [{ a: 'c-eu', b: 'c-us' }, { a: 'c-eu', b: 'c-gr' }])
      vi.spyOn(api, 'linksLatest').mockResolvedValue(answer({ pairs: [
        { a: 'c-eu', b: 'c-us', lossAToB: 0, lossBToA: 0.001, rttSeconds: 0.012, jitterSeconds: 0.0004, mtuBytes: 1500, throughputAToB: 9.2e8, throughputBToA: 8.7e8, probedAt: NOW },
        { a: 'c-eu', b: 'c-gr', lossAToB: 0.018, lossBToA: 0, mtuBytes: 1420, probedAt: NOW },
      ] }))
      render(<ClusterLinks />)
      const rows = await screen.findAllByTestId('pair-row')
      const first = await within(rows[0]).findByTestId('latest')
      expect(first).toHaveTextContent('loss 0.0 % → · 0.1 % ←')
      expect(first).toHaveTextContent('round trip 12 ms · jitter 0.40 ms · MTU 1500 · 920 Mbit/s → · 870 Mbit/s ←')
      expect(first).toHaveTextContent('probed just now')
      expect(first).toHaveAccessibleDescription(/→ is from cloud-eu to cloud-us/)
      const second = await within(rows[1]).findByTestId('latest')
      expect(second).toHaveTextContent('loss 1.8 % → · 0.0 % ←')
      expect(second.querySelector('.text-warn')).not.toBeNull()
      expect(first.querySelector('.text-warn')).toBeNull()
    })

    it('says so when a pair has no figures yet, and for a pair without probing', async () => {
      seed('viewer', [{ a: 'c-eu', b: 'c-us' }, { a: 'c-eu', b: 'c-gr', noProbe: true }])
      render(<ClusterLinks />)
      const empty = await screen.findAllByTestId('latest-empty')
      await vi.waitFor(() => expect(screen.getAllByTestId('latest-empty').map((e) => e.textContent)).toEqual(['No data yet', 'No probing: see the traffic in FUSION']))
      expect(empty).toHaveLength(2)
      expect(await screen.findByTestId('latest-notice')).toHaveTextContent('No figures have arrived yet')
    })

    it('shows the server\'s reason under the pair it applies to, even without FUSION', async () => {
      seed('viewer', [{ a: 'c-eu', b: 'c-us' }, { a: 'c-us', b: 'c-gr' }])
      vi.spyOn(api, 'linksLatest').mockResolvedValue(
        answer({ available: false, reason: 'no-fusion', notes: [{ cluster: 'c-eu', peer: 'c-us', code: 'tier', text: 'This cluster\'s agent is at access tier 1.' }] }),
      )
      render(<ClusterLinks />)
      const note = await screen.findByTestId('pair-note')
      expect(note).toHaveTextContent('cloud-eu: This cluster\'s agent is at access tier 1.')
      // only the pair that has cloud-eu in it shows it
      expect(within(screen.getAllByTestId('pair-row')[0]).getByTestId('pair-note')).toBe(note)
      expect(within(screen.getAllByTestId('pair-row')[1]).queryByTestId('pair-note')).toBeNull()
    })

    it('says when a pinned pair\'s other cluster has no address of that kind', async () => {
      seed('viewer', [{ a: 'c-eu', b: 'c-us', via: 'mesh' }])
      vi.spyOn(api, 'linksLatest').mockResolvedValue(
        answer({ notes: [{ cluster: 'c-eu', peer: 'c-us', code: 'no-address-of-kind', text: 'The pair is set to measure through mesh addresses, but c-us has none.' }] }),
      )
      render(<ClusterLinks />)
      expect(await screen.findByTestId('pair-note')).toHaveTextContent('cloud-eu: The pair is set to measure through mesh addresses, but c-us has none.')
    })

    it('explains a server without FUSION once, not on every row', async () => {
      seed('viewer', [{ a: 'c-eu', b: 'c-us' }])
      vi.spyOn(api, 'linksLatest').mockResolvedValue(answer({ available: false, reason: 'no-fusion' }))
      render(<ClusterLinks />)
      expect(await screen.findByTestId('latest-notice')).toHaveTextContent('which this server does not have')
      expect(screen.getByTestId('latest-empty')).toHaveTextContent('—')
    })

    it('says when FUSION could not be read, whether the server or the request failed', async () => {
      seed('viewer', [{ a: 'c-eu', b: 'c-us' }])
      vi.spyOn(api, 'linksLatest').mockResolvedValue(answer({ available: false, reason: 'unavailable' }))
      const { unmount } = render(<ClusterLinks />)
      expect(await screen.findByTestId('latest-notice')).toHaveTextContent('could not be read from FUSION just now')
      unmount()
      vi.spyOn(api, 'linksLatest').mockRejectedValue(new Error('network'))
      render(<ClusterLinks />)
      expect(await screen.findByTestId('latest-notice')).toHaveTextContent('could not be read from FUSION just now')
    })

    it('reads nothing while there are no pairs or the settings are not loaded, and again when a pair is added', async () => {
      const read = vi.spyOn(api, 'linksLatest')
      seed('admin', [], { loaded: false })
      const { unmount } = render(<ClusterLinks />)
      expect(read).not.toHaveBeenCalled()
      unmount()
      seed('admin', [])
      render(<ClusterLinks />)
      expect(read).not.toHaveBeenCalled()
      fireEvent.click(screen.getByTestId('add-pair'))
      pick('First cluster', 'cloud-us')
      pick('Second cluster', 'cloud-eu')
      fireEvent.click(screen.getByTestId('confirm-add-pair'))
      await vi.waitFor(() => expect(read).toHaveBeenCalledTimes(1))
    })
  })
  describe('setup checklist', () => {
    const st = (code: string, state: SetupStep['state'], text: string, cluster?: string): SetupStep => ({ code, state, text, ...(cluster ? { cluster } : {}) })
    const ready = [
      st('agent', 'ok', 'cloud-eu has an approved agent at access tier 2 with the flow collector running.', 'c-eu'),
      st('agent', 'ok', 'cloud-us has an approved agent at access tier 2 with the flow collector running.', 'c-us'),
      st('links-on', 'ok', 'Cluster links and probing are switched on in cloud-eu.', 'c-eu'),
      st('export', 'ok', 'cloud-eu runs the probes for this pair and sends its telemetry onward, so what it measures can reach FUSION.', 'c-eu'),
    ]
    const withSetup = (steps: SetupStep[], extra: Partial<LinksLatest> = {}) =>
      vi.spyOn(api, 'linksLatest').mockResolvedValue(answer({ setup: [{ a: 'c-eu', b: 'c-us', steps }], ...extra }))

    it('shows nothing extra under a pair that is ready, only a quiet word on the Setup button', async () => {
      seed('viewer', [{ a: 'c-eu', b: 'c-us' }])
      withSetup(ready)
      render(<ClusterLinks />)
      expect(await screen.findByTestId('pair-setup-summary')).toHaveTextContent('ready')
      expect(screen.queryByTestId('pair-warning')).toBeNull()
      expect(screen.queryByTestId('pair-setup-steps')).toBeNull()
    })

    it('lists what is still to do as warning lines, two at most and then how many more', async () => {
      seed('viewer', [{ a: 'c-eu', b: 'c-us' }])
      withSetup([
        ...ready,
        st('export', 'todo', 'cloud-eu runs the probes for this pair but has no telemetry sent to FUSION, so what it measures stays in the cluster.', 'c-eu'),
        st('links-on', 'todo', 'Cluster links is not switched on in the install of cloud-us.', 'c-us'),
        st('prober', 'todo', 'The link prober in cloud-eu has not asked its agent for the clusters to measure.', 'c-eu'),
      ])
      render(<ClusterLinks />)
      const lines = await screen.findAllByTestId('pair-warning')
      expect(lines).toHaveLength(2)
      expect(lines[0]).toHaveTextContent('cloud-eu runs the probes for this pair but has no telemetry sent to FUSION')
      expect(lines[1]).toHaveTextContent('Cluster links is not switched on in the install of cloud-us.')
      expect(screen.getByTestId('pair-warning-more')).toHaveTextContent('and 1 more')
      expect(screen.getByTestId('pair-setup-summary')).toHaveTextContent('3 to do')
    })

    it('opens the full list from a button that says whether it is open, and marks each step with a word as well as a shape', async () => {
      seed('viewer', [{ a: 'c-eu', b: 'c-us' }])
      withSetup([
        ...ready,
        st('export', 'todo', 'cloud-eu has no telemetry sent to FUSION.', 'c-eu'),
        st('capabilities', 'unknown', "The agent of cloud-us is too old to say what its install can do.", 'c-us'),
        st('peer-export', 'optional', 'cloud-us sends no telemetry to FUSION. Optional.', 'c-us'),
      ])
      render(<ClusterLinks />)
      const toggle = await screen.findByTestId('pair-setup-toggle')
      expect(toggle).toHaveAttribute('aria-expanded', 'false')
      expect(toggle).toHaveAccessibleName(/Setup/)
      fireEvent.click(toggle)
      expect(toggle).toHaveAttribute('aria-expanded', 'true')
      const list = screen.getByTestId('pair-setup-steps')
      expect(toggle).toHaveAttribute('aria-controls', list.id)
      const items = within(list).getAllByTestId('setup-step')
      expect(items).toHaveLength(7)
      expect(items.map((i) => i.getAttribute('data-state'))).toEqual(['ok', 'ok', 'ok', 'ok', 'todo', 'unknown', 'optional'])
      expect(items[4]).toHaveTextContent('To do: cloud-eu has no telemetry sent to FUSION.')
      expect(items[0]).toHaveTextContent('Done: cloud-eu has an approved agent')
      expect(items[5]).toHaveTextContent('Not known: The agent of cloud-us is too old to say what its install can do.')
      expect(items[6]).toHaveTextContent('Optional: cloud-us sends no telemetry to FUSION.')
      // an agent too old to say is not a warning on the row
      expect(screen.getAllByTestId('pair-warning')).toHaveLength(1)
      fireEvent.click(toggle)
      expect(toggle).toHaveAttribute('aria-expanded', 'false')
      expect(screen.queryByTestId('pair-setup-steps')).toBeNull()
    })

    it('can be opened from the keyboard: it is a button', async () => {
      seed('viewer', [{ a: 'c-eu', b: 'c-us' }])
      withSetup(ready)
      render(<ClusterLinks />)
      const toggle = await screen.findByTestId('pair-setup-toggle')
      expect(toggle.tagName).toBe('BUTTON')
      toggle.focus()
      expect(toggle).toHaveFocus()
    })

    it('says "not fully known" rather than ready when an agent is too old to say, and does not warn', async () => {
      seed('viewer', [{ a: 'c-eu', b: 'c-us' }])
      withSetup([...ready.slice(0, 2), st('capabilities', 'unknown', 'The agent of cloud-us is too old to say what its install can do.', 'c-us')])
      render(<ClusterLinks />)
      expect(await screen.findByTestId('pair-setup-summary')).toHaveTextContent('not fully known')
      expect(screen.queryByTestId('pair-warning')).toBeNull()
    })

    it('shows a server reason once: the note is left out when the checklist says the same of that cluster', async () => {
      seed('viewer', [{ a: 'c-eu', b: 'c-us' }])
      withSetup(
        [st('agent', 'todo', "The agent of cloud-eu is at access tier 1. Link measuring needs tier 2.", 'c-eu'), st('agent', 'ok', 'cloud-us has an approved agent.', 'c-us')],
        { notes: [{ cluster: 'c-eu', peer: 'c-us', code: 'tier', text: "This cluster's agent is at access tier 1." }, { cluster: 'c-us', peer: 'c-eu', code: 'peer-silent', text: 'The agent of c-eu has not reported any node address yet.' }] },
      )
      render(<ClusterLinks />)
      expect(await screen.findByTestId('pair-warning')).toHaveTextContent('The agent of cloud-eu is at access tier 1.')
      // the tier note is covered by the step; the other note says something the checklist does not, so it stays
      const notes = screen.getAllByTestId('pair-note')
      expect(notes).toHaveLength(1)
      expect(notes[0]).toHaveTextContent('cloud-us: The agent of c-eu has not reported any node address yet.')
    })

    it('does not say the same of a low access level twice when the server says it too', async () => {
      seed('viewer', [{ a: 'c-eu', b: 'c-gr' }]) // edge-athens is at access level 1
      vi.spyOn(api, 'linksLatest').mockResolvedValue(answer({ setup: [{ a: 'c-eu', b: 'c-gr', steps: [
        st('agent', 'ok', 'cloud-eu has an approved agent.', 'c-eu'),
        st('agent', 'todo', 'The agent of edge-athens is at access tier 1. Raise its access tier to 2 under Agents.', 'c-gr'),
      ] }] }))
      render(<ClusterLinks />)
      await vi.waitFor(() => expect(screen.getByTestId('pair-warning')).toHaveTextContent('The agent of edge-athens is at access tier 1.'))
      expect(screen.getAllByTestId('pair-warning')).toHaveLength(1)
    })

    it('shows no Setup button from a server that does not send the checklist, and keeps the page\'s own checks', async () => {
      seed('viewer', [{ a: 'c-eu', b: 'c-gr' }])
      render(<ClusterLinks />)
      expect(await screen.findByTestId('pair-warning')).toHaveTextContent('edge-athens is at access level 1')
      await screen.findByTestId('latest-empty')
      expect(screen.queryByTestId('pair-setup')).toBeNull()
    })
  })

  describe('what a pair needs, said up front', () => {
    it('states the requirement in the section intro and in the Add pair dialog', () => {
      seed('admin', [])
      render(<ClusterLinks />)
      expect(screen.getByTestId('links-requirement')).toHaveTextContent(LINK_REQUIREMENT)
      expect(LINK_REQUIREMENT).toMatch(/two clusters that each run the Ikhnos agent with Cluster links on/)
      expect(LINK_REQUIREMENT).toMatch(/send telemetry to FUSION \(a local operator\)/)
      fireEvent.click(screen.getByTestId('add-pair'))
      expect(screen.getByTestId('add-pair-requirement')).toHaveTextContent(LINK_REQUIREMENT)
    })

    it('disables Add pair, with the reason beside it, when fewer than two clusters have an approved agent', () => {
      seed('admin', [])
      useRawTopology.setState({ clusters: [cluster('c-eu', 'cloud-eu'), cluster('c-us', 'cloud-us')], agents: [agent('c-eu'), { ...agent('c-us'), status: 'pending' }] } as never)
      render(<ClusterLinks />)
      const add = screen.getByTestId('add-pair')
      expect(add).toBeDisabled()
      expect(add).toHaveAccessibleDescription('Needs two clusters with an approved agent')
      expect(screen.getByTestId('add-pair-why')).toBeInTheDocument()
      fireEvent.click(add)
      expect(screen.queryByTestId('add-pair-requirement')).toBeNull()
    })

    it('enables Add pair with two clusters that have an approved agent', () => {
      seed('admin', [])
      render(<ClusterLinks />)
      expect(screen.getByTestId('add-pair')).toBeEnabled()
      expect(screen.queryByTestId('add-pair-why')).toBeNull()
    })
  })
})
