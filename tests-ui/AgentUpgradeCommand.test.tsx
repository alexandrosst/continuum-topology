import { act, render, screen } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'
import { ServerCommand } from '@/components/agents/AgentInsight'
import { type AgentUpgradeAnswer, type AgentUpgradeRequest, UPGRADE_DEBOUNCE_MS, upgradeRequest, useAgentUpgradeCommand } from '@/lib/agentUpgrade'
import { emptyTelemetry } from '@/lib/install'

// How the page asks the server for the command that changes an installed agent: after a rest, never for what is not wanted, and only
// the answer to what is on screen is shown - whichever order the answers come back in.

let CONN = { url: 'https://example.test', org: 'org-1' }
vi.mock('@/store/server', () => ({ useConn: () => CONN, useServer: { getState: () => ({ boostPolling: () => undefined }) } }))

const asked = vi.fn<(req: AgentUpgradeRequest) => Promise<AgentUpgradeAnswer>>()
vi.mock('@/lib/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/lib/api')>()
  return { ...actual, api: { ...actual.api, agentUpgradeCommand: (_c: unknown, req: AgentUpgradeRequest) => asked(req) } }
})

const answer = (command: string, problems: string[] = []): AgentUpgradeAnswer => ({ command, upgrade: command, secretCommand: '', secrets: [], problems })

function Probe({ request, id = 'cmd' }: { request: AgentUpgradeRequest | null; id?: string }) {
  return <ServerCommand command={useAgentUpgradeCommand(request)} testId={id} />
}

beforeEach(() => {
  vi.useFakeTimers()
  asked.mockReset()
  CONN = { url: 'https://example.test', org: 'org-1' }
})
afterEach(() => vi.useRealTimers())

const rest = () => act(async () => { await vi.advanceTimersByTimeAsync(UPGRADE_DEBOUNCE_MS + 1) })
const settle = () => act(async () => { await vi.advanceTimersByTimeAsync(0) })

describe('useAgentUpgradeCommand', () => {
  test('asks nothing, and shows nothing, when nothing is wanted', async () => {
    render(<Probe request={null} />)
    await rest()
    expect(asked).not.toHaveBeenCalled()
    expect(screen.queryByTestId('cmd')).not.toBeInTheDocument()
    expect(screen.queryByTestId('cmd-pending')).not.toBeInTheDocument()
  })

  test('waits for an edit to rest, then asks once for the last state', async () => {
    asked.mockResolvedValue(answer('helm upgrade last'))
    const { rerender } = render(<Probe request={{ tier: 1 }} />)
    expect(screen.getByTestId('cmd-pending')).toBeInTheDocument()
    await act(async () => { await vi.advanceTimersByTimeAsync(UPGRADE_DEBOUNCE_MS - 50) })
    rerender(<Probe request={{ tier: 2 }} />) // typing on: the earlier state is never asked
    await act(async () => { await vi.advanceTimersByTimeAsync(UPGRADE_DEBOUNCE_MS - 50) })
    expect(asked).not.toHaveBeenCalled()
    await rest()
    expect(asked).toHaveBeenCalledTimes(1)
    expect(asked).toHaveBeenCalledWith({ tier: 2 })
    expect(screen.getByTestId('cmd')).toHaveTextContent('helm upgrade last')
  })

  test('a request that comes out the same is not asked again', async () => {
    asked.mockResolvedValue(answer('one'))
    const { rerender } = render(<Probe request={{ tier: 1, namespace: 'x' }} />)
    await rest()
    rerender(<Probe request={{ tier: 1, namespace: 'x' }} />) // a new object, the same words
    await rest()
    expect(asked).toHaveBeenCalledTimes(1)
  })

  test('an answer to an earlier draft that arrives after the newer one is dropped', async () => {
    let first!: (a: AgentUpgradeAnswer) => void
    asked.mockImplementationOnce(() => new Promise((res) => { first = res }))
    asked.mockResolvedValueOnce(answer('for tier 2'))
    const { rerender } = render(<Probe request={{ tier: 1 }} />)
    await rest() // asked for tier 1; the server is slow
    rerender(<Probe request={{ tier: 2 }} />)
    await rest()
    expect(screen.getByTestId('cmd')).toHaveTextContent('for tier 2')
    first(answer('for tier 1, late'))
    await settle()
    expect(screen.getByTestId('cmd')).toHaveTextContent('for tier 2')
    expect(screen.queryByText(/for tier 1, late/)).not.toBeInTheDocument()
  })

  test('while a newer draft waits, the last command is not shown beside it', async () => {
    asked.mockResolvedValue(answer('for tier 1'))
    const { rerender } = render(<Probe request={{ tier: 1 }} />)
    await rest()
    expect(screen.getByTestId('cmd')).toHaveTextContent('for tier 1')
    rerender(<Probe request={{ tier: 2 }} />)
    expect(screen.queryByTestId('cmd')).not.toBeInTheDocument()
    expect(screen.getByTestId('cmd-pending')).toBeInTheDocument()
  })

  test('a draft the server cannot write a command from shows why, and offers nothing to copy', async () => {
    asked.mockResolvedValue({ ...answer('', ['An export endpoint is required once any telemetry signal is on', 'another']), upgrade: '' })
    render(<Probe request={{ telemetry: upgradeRequest({ telemetry: emptyTelemetry }).telemetry }} />)
    await rest()
    expect(screen.getByTestId('cmd-problems')).toHaveTextContent('There is no command yet: An export endpoint is required once any telemetry signal is on (and 1 more).')
    expect(screen.queryByTestId('cmd')).not.toBeInTheDocument()
  })

  test('a server that fails is told, and the next draft is asked again', async () => {
    asked.mockRejectedValueOnce(new Error('the server is busy'))
    asked.mockResolvedValueOnce(answer('works now'))
    const { rerender } = render(<Probe request={{ tier: 1 }} />)
    await rest()
    expect(screen.getByTestId('cmd-error')).toHaveTextContent('The command could not be written: the server is busy')
    rerender(<Probe request={{ tier: 2 }} />)
    await rest()
    expect(screen.getByTestId('cmd')).toHaveTextContent('works now')
    expect(screen.queryByTestId('cmd-error')).not.toBeInTheDocument()
  })

  test('another organisation starts over: the last one\'s command is not shown for it', async () => {
    asked.mockResolvedValueOnce(answer('for org-1'))
    asked.mockResolvedValueOnce(answer('for org-2'))
    const { rerender } = render(<Probe request={{ tier: 1 }} />)
    await rest()
    CONN = { url: 'https://example.test', org: 'org-2' }
    rerender(<Probe request={{ tier: 1 }} />)
    expect(screen.queryByText('for org-1')).not.toBeInTheDocument()
    await rest()
    expect(screen.getByTestId('cmd')).toHaveTextContent('for org-2')
  })
})

describe('upgradeRequest', () => {
  test('says only what is known: a release that is not reported, a tier not being set and telemetry not being changed are left out', () => {
    expect(upgradeRequest({})).toEqual({})
    expect(upgradeRequest({ tier: 0 })).toEqual({ tier: 0 })
    expect(upgradeRequest({ target: { namespace: 'ns', release: undefined }, tier: 2 })).toEqual({ namespace: 'ns', tier: 2 })
  })
})
