import { render, screen } from '@testing-library/react'
import { beforeEach, describe, expect, test, vi } from 'vitest'
import { ConsentPanel } from '@/features/agents/components/AgentInsight'
import type { Agent } from '@/lib/types'
import { answerTo, forgetUpgrades, lastUpgrade } from './upgradeServer'

// The widen-the-ceiling command is the cluster owner's to run: it must be asked for where THIS agent is installed, not the chart's defaults.
// The server writes it, naming the chart it serves (backend/internal/server/admin_commands_test.go); the panel reads the install settings
// again when it opens, and shows what the server answers.

const reloadInfo = vi.fn(async () => undefined)
const serverState = {
  info: { implementedTier: 2, install: { chartRef: 'oci://registry.example.com/team/continuum-agent', chartVersion: '0.9.0' } },
  conn: () => ({ url: '', org: 'o' }),
  refresh: async () => undefined,
  reloadInfo,
}
vi.mock('@/lib/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/lib/api')>()
  return { ...actual, api: { ...actual.api, agentUpgradeCommand: async (...a: Parameters<typeof import('./upgradeServer').answer>) => (await import('./upgradeServer')).answer(...a) } }
})
vi.mock('@/store/server', () => ({
  useServer: (selector?: (s: typeof serverState) => unknown) => (selector ? selector(serverState) : serverState),
  useConn: () => ({ url: '', org: 'o' }),
}))

const agent = (over: Partial<Agent> = {}) => ({ id: 'a1', clusterId: 'c1', status: 'approved', accessTier: 0, installedTier: 0, name: 'edge-1', ...over }) as Agent

beforeEach(() => {
  reloadInfo.mockClear()
  forgetUpgrades()
})

describe('ConsentPanel widen command', () => {
  test('asks for the command at the agent\'s own release and namespace, and shows what the server writes', async () => {
    render(<ConsentPanel agent={agent({ namespace: 'observability', releaseName: 'agent-eu' })} />)
    expect(reloadInfo).toHaveBeenCalledTimes(1)
    expect(await screen.findByTestId('helm-command')).toHaveTextContent(answerTo({ namespace: 'observability', release: 'agent-eu', tier: 1 }))
    expect(lastUpgrade()).toEqual({ namespace: 'observability', release: 'agent-eu', tier: 1 })
  })

  test('asks without a namespace or release while the agent has not said where it lives, which the server answers with the chart\'s defaults', async () => {
    render(<ConsentPanel agent={agent()} />)
    expect(await screen.findByTestId('helm-command')).toHaveTextContent(answerTo({ tier: 1 }))
    expect(lastUpgrade()).toEqual({ tier: 1 })
  })

  test('shows no command, and asks for none, while there is no wider tier to offer', () => {
    render(<ConsentPanel agent={agent({ accessTier: 2, installedTier: 2 })} />)
    expect(screen.queryByTestId('helm-command')).not.toBeInTheDocument()
    expect(screen.queryByTestId('helm-command-pending')).not.toBeInTheDocument()
  })
})
