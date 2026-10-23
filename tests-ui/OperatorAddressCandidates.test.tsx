import { cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { OperatorAddressModal } from '@/features/pipeline/components/operators/OperatorAddress'
import type { AddressCandidates, RegionalOperator } from '@/lib/types'

const candidates = vi.fn(async (..._a: unknown[]): Promise<AddressCandidates> => ({ found: false, candidates: [] }))
const setCluster = vi.fn(async (..._a: unknown[]): Promise<{ clusterId: string }> => ({ clusterId: '' }))
const setAddress = vi.fn(async (..._a: unknown[]): Promise<RegionalOperator> => ({}) as RegionalOperator)

const connFn = () => ({ url: '', org: 'o' })
vi.mock('@/store/server', () => ({
  useServer: (selector?: (s: { conn: typeof connFn }) => unknown) => {
    const state = { conn: connFn }
    return selector ? selector(state) : state
  },
}))
vi.mock('@/lib/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/lib/api')>()
  return { ...actual, api: { ...actual.api, operatorAddressCandidates: (...a: unknown[]) => candidates(...a), setOperatorAddress: (...a: unknown[]) => setAddress(...a), setServerCluster: (...a: unknown[]) => setCluster(...a) } }
})

const op = { id: 'site-athens', name: 'site-athens', exposure: 'nodeport' } as RegionalOperator

const open = () => render(<OperatorAddressModal operator={op} onClose={() => {}} onDone={() => {}} />)

beforeEach(() => {
  setCluster.mockReset()
  setCluster.mockResolvedValue({ clusterId: '' })
  candidates.mockReset()
  setAddress.mockReset()
  setAddress.mockResolvedValue({} as RegionalOperator)
})
afterEach(cleanup)

describe('the address dialog offers what discovery found', () => {
  it('lists the addresses, and picking one fills the field without saving', async () => {
    candidates.mockResolvedValue({
      found: true,
      hostClusterName: 'athens-edge',
      candidates: [
        { id: 'node-port|10.20.0.14:31317', address: '10.20.0.14:31317', kind: 'node-port', source: 'Node athens-edge-1, internal address.', node: 'athens-edge-1' },
        { id: 'load-balancer|203.0.113.9:4317', address: '203.0.113.9:4317', kind: 'load-balancer', source: 'The address the load balancer gave the Service.' },
      ],
    })
    open()
    const rows = await screen.findAllByTestId('operator-address-candidate')
    expect(rows).toHaveLength(2)
    expect(screen.getByTestId('operator-address-candidates').textContent).toContain('athens-edge')
    await userEvent.click(screen.getByRole('button', { name: 'Use 10.20.0.14:31317' }))
    expect((screen.getByTestId('operator-address-input') as HTMLInputElement).value).toBe('10.20.0.14:31317')
    expect(setAddress).not.toHaveBeenCalled()
    expect(screen.getByRole('button', { name: 'Use 10.20.0.14:31317' }).textContent).toBe('Selected')
    await userEvent.click(screen.getByTestId('operator-address-save'))
    await waitFor(() => expect(setAddress).toHaveBeenCalledWith(expect.anything(), 'site-athens', '10.20.0.14:31317'))
  })

  it('names the network and interface of a node address, and shows the hints', async () => {
    candidates.mockResolvedValue({
      found: true,
      hostClusterName: 'athens-edge',
      candidates: [
        { id: 'node-port|100.64.3.7:31317', address: '100.64.3.7:31317', kind: 'node-port', source: 'Node a, mesh address on tailscale0.', node: 'a', network: 'mesh', interface: 'tailscale0' },
        { id: 'node-port|10.20.0.14:31317', address: '10.20.0.14:31317', kind: 'node-port', source: 'Node a, internal address.', node: 'a', network: 'private' },
      ],
      hints: ['Turn on the node probe in the agent to also offer each node’s mesh and other addresses.'],
    })
    open()
    const rows = await screen.findAllByTestId('operator-address-candidate')
    expect(rows[0].textContent).toContain('Mesh (tailscale0)')
    expect(rows[1].textContent).toContain('Private network')
    expect(rows[1].textContent).not.toContain('(')
    expect((await screen.findByTestId('operator-address-hint')).textContent).toContain('node probe')
  })

  it('shows a card with the command that exposes the central gateway, and the one for a load balancer on a click', async () => {
    candidates.mockResolvedValue({
      found: true,
      candidates: [],
      expose: {
        service: 'fusion-central',
        namespace: 'ikhnos-system',
        value: 'fusion.central.service.type',
        commands: {
          nodePort: `kubectl --namespace ikhnos-system patch svc fusion-central -p '{"spec":{"type":"NodePort"}}'`,
          loadBalancer: `kubectl --namespace ikhnos-system patch svc fusion-central -p '{"spec":{"type":"LoadBalancer"}}'`,
        },
      },
    })
    open()
    const card = await screen.findByTestId('operator-address-expose')
    expect(card).toHaveTextContent('not reachable from other clusters yet')
    expect(screen.getByTestId('operator-address-expose-command')).toHaveTextContent('"type":"NodePort"')
    expect(card).toHaveTextContent('fusion.central.service.type=NodePort')
    expect(card).toHaveTextContent('restarts nothing')
    await userEvent.click(screen.getByTestId('operator-address-expose-loadBalancer'))
    expect(screen.getByTestId('operator-address-expose-command')).toHaveTextContent('"type":"LoadBalancer"')
    expect(card).toHaveTextContent('fusion.central.service.type=LoadBalancer')
    // The card says it, so the generic empty sentence is not repeated under it.
    expect(screen.queryByTestId('operator-address-candidates-none')).toBeNull()
  })

  it('says why the list is empty, in the two ways it can be', async () => {
    candidates.mockResolvedValue({ found: false, candidates: [] })
    const first = open()
    expect((await screen.findByTestId('operator-address-candidates-none')).textContent).toContain('No agent has reported this operator')
    first.unmount()
    candidates.mockResolvedValue({ found: true, hostClusterName: 'athens-edge', candidates: [] })
    open()
    expect((await screen.findByTestId('operator-address-candidates-none')).textContent).toContain('no address outside the cluster yet')
  })

  it('does not list the typed address again as a find, and still works when the lookup fails', async () => {
    candidates.mockResolvedValue({ found: false, candidates: [{ id: 'recorded|otlp.example.com:4317', address: 'otlp.example.com:4317', kind: 'recorded', source: 'Recorded for this operator.', recorded: true }] })
    const first = open()
    await screen.findByTestId('operator-address-candidates-none')
    expect(screen.queryByTestId('operator-address-candidate')).toBeNull()
    first.unmount()
    candidates.mockRejectedValue(new Error('network'))
    open()
    await waitFor(() => expect(candidates).toHaveBeenCalledTimes(2))
    expect(screen.queryByTestId('operator-address-candidates')).toBeNull()
    expect(screen.getByTestId('operator-address-input')).toBeTruthy()
  })
})

describe('the central operator is offered what was found for its gateway too', () => {
  it('lists the gateway\'s addresses in its own dialog', async () => {
    candidates.mockResolvedValue({ found: true, candidates: [{ id: 'load-balancer|203.0.113.50:4317', address: '203.0.113.50:4317', kind: 'load-balancer', source: 'The address the load balancer gave the operator\'s Service.' }] })
    render(<OperatorAddressModal operator={{ id: 'op-central', name: 'Central (FUSION)' } as RegionalOperator} central={{ service: 'ikhnos-fusion-central', namespace: 'ikhnos' }} onClose={() => {}} onDone={() => {}} />)
    expect(await screen.findAllByTestId('operator-address-candidate')).toHaveLength(1)
    expect(candidates).toHaveBeenCalledWith(expect.anything(), 'op-central')
    await userEvent.click(screen.getByRole('button', { name: 'Use 203.0.113.50:4317' }))
    expect((screen.getByTestId('operator-address-input') as HTMLInputElement).value).toBe('203.0.113.50:4317')
    expect(screen.getByTestId('operator-address-central')).toBeInTheDocument()
  })
})

describe('the central operator says which cluster the server runs in', () => {
  const central = { id: 'op-central', name: 'Central (FUSION)', exposure: 'nodeport' } as RegionalOperator
  const openCentral = () => render(<MemoryRouter><OperatorAddressModal operator={central} central={{ service: 'fusion-central', namespace: 'ikhnos' }} onClose={() => {}} onDone={() => {}} /></MemoryRouter>)
  const lb = { id: 'load-balancer|203.0.113.9:4317', address: '203.0.113.9:4317', kind: 'load-balancer' as const, source: 'The address the load balancer gave the Service.' }

  it('marks the cluster the server runs in, says how it knows, and lets it be changed', async () => {
    candidates.mockResolvedValue({
      found: true,
      hostClusterId: 'cl-1',
      hostClusterName: 'nuc-edge',
      candidates: [lb],
      serverCluster: { state: 'known', source: 'install', clusterId: 'cl-1', options: [{ id: 'cl-1', name: 'nuc-edge' }, { id: 'cl-2', name: 'patras-edge' }] },
    })
    openCentral()
    expect((await screen.findByTestId('operator-address-server-cluster-pill')).textContent).toBe('Where the server runs')
    expect(screen.getByTestId('operator-address-server-cluster')).toHaveTextContent('runs in nuc-edge from its install')
    await userEvent.click(screen.getByTestId('operator-address-server-cluster-change'))
    await userEvent.click(screen.getByTestId('operator-address-server-cluster-save'))
    // Nothing chosen in the list: the server is asked to work it out again.
    expect(setCluster).toHaveBeenCalledWith(expect.anything(), 'op-central', '')
    await waitFor(() => expect(candidates).toHaveBeenCalledTimes(2))
  })

  it('asks to connect the cluster when it has no agent, instead of a sentence about hints', async () => {
    candidates.mockResolvedValue({
      found: true,
      candidates: [lb],
      hints: ['The server does not know which cluster FUSION’s gateway runs in, or that cluster has no agent, so …'],
      serverCluster: { state: 'noAgent', source: 'install', clusterId: 'cl-9' },
    })
    openCentral()
    const card = await screen.findByTestId('operator-address-server-cluster')
    expect(card).toHaveTextContent("The server's cluster has no agent")
    expect(screen.getByTestId('operator-address-server-cluster-connect')).toBeTruthy()
    // The card says it, so the generic sentence is not repeated.
    expect(screen.queryByTestId('operator-address-hint')).toBeNull()
  })

  it('says the nodes come from the server\u2019s own node probes when no agent is there', async () => {
    candidates.mockResolvedValue({
      found: true,
      candidates: [lb],
      serverCluster: { state: 'known', source: 'probe', nodes: 'probe' },
    })
    openCentral()
    const card = await screen.findByTestId('operator-address-server-cluster')
    expect(card).toHaveAttribute('data-state', 'known')
    expect(card).toHaveTextContent('because its own node probes report from it')
    expect(card).toHaveTextContent('no agent is needed')
    // Nothing to ask the person to connect.
    expect(screen.queryByTestId('operator-address-server-cluster-connect')).toBeNull()
  })

  it('fills the list in by itself once the command has been run, without reopening the dialog', async () => {
    const expose = { service: 'fusion-central', namespace: 'ikhnos', value: 'fusion.central.service.type', commands: { nodePort: 'kubectl patch np', loadBalancer: 'kubectl patch lb' } }
    candidates.mockResolvedValueOnce({ found: true, candidates: [], expose, serverCluster: { state: 'known', source: 'install' } })
    candidates.mockResolvedValue({
      found: true,
      candidates: [{ id: 'node-port|10.20.0.14:31317', address: '10.20.0.14:31317', kind: 'node-port', source: 'Node n1.', node: 'n1' }],
      serverCluster: { state: 'known', source: 'install' },
    })
    vi.useFakeTimers({ shouldAdvanceTime: true })
    try {
      openCentral()
      expect(await screen.findByTestId('operator-address-expose')).toBeTruthy()
      expect(screen.queryByTestId('operator-address-candidate')).toBeNull()
      // Somebody runs the command in a terminal; nothing is clicked here.
      await vi.advanceTimersByTimeAsync(4500)
      expect(await screen.findByTestId('operator-address-candidate')).toBeTruthy()
      expect(screen.queryByTestId('operator-address-expose')).toBeNull()
      // Addresses are there now, so it stops asking.
      const calls = candidates.mock.calls.length
      await vi.advanceTimersByTimeAsync(20000)
      expect(candidates.mock.calls.length).toBe(calls)
    } finally {
      vi.useRealTimers()
    }
  })

  it('asks again at once when the person comes back to the window while it is waiting', async () => {
    candidates.mockResolvedValueOnce({ found: true, candidates: [], serverCluster: { state: 'noAgent', source: 'install', clusterId: 'cl-9' } })
    candidates.mockResolvedValue({ found: true, candidates: [{ id: 'node-port|10.20.0.14:31317', address: '10.20.0.14:31317', kind: 'node-port', source: 'Node n1.', node: 'n1' }], serverCluster: { state: 'known', source: 'install' } })
    openCentral()
    expect(await screen.findByTestId('operator-address-server-cluster')).toBeTruthy()
    window.dispatchEvent(new Event('focus'))
    expect(await screen.findByTestId('operator-address-candidate')).toBeTruthy()
  })

  it('asks which cluster the server is in when nothing could say, and saves the choice', async () => {
    candidates.mockResolvedValue({
      found: false,
      candidates: [lb],
      serverCluster: { state: 'unknown', options: [{ id: 'cl-1', name: 'nuc-edge' }, { id: 'cl-2', name: 'patras-edge' }] },
    })
    openCentral()
    const card = await screen.findByTestId('operator-address-server-cluster')
    expect(card).toHaveTextContent('Which cluster is the server in?')
    // Nothing to save until a cluster is picked.
    expect(screen.getByTestId('operator-address-server-cluster-save')).toBeDisabled()
    await userEvent.click(screen.getByRole('combobox', { name: 'Cluster' }))
    await userEvent.click(screen.getByRole('option', { name: 'nuc-edge' }))
    await userEvent.click(screen.getByTestId('operator-address-server-cluster-save'))
    expect(setCluster).toHaveBeenCalledWith(expect.anything(), 'op-central', 'cl-1')
  })
})
