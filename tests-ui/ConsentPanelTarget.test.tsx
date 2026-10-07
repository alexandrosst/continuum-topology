import { render, screen } from '@testing-library/react'
import { beforeEach, describe, expect, test, vi } from 'vitest'
import { ConsentPanel } from '@/components/agents/AgentInsight'
import type { Agent } from '@/lib/types'

// The widen-the-ceiling command is the cluster owner's to run: it must name where THIS agent is installed, not the chart's defaults, and
// the chart reference it prints is read again when the panel opens (a tab open for days would otherwise print an old version).

const reloadInfo = vi.fn(async () => undefined)
const serverState = {
  info: { implementedTier: 2, install: { chartRef: 'oci://registry.example.com/team/continuum-agent', chartVersion: '0.9.0' } },
  conn: () => ({ url: '', org: 'o' }),
  refresh: async () => undefined,
  reloadInfo,
}
vi.mock('@/store/server', () => ({
  useServer: (selector?: (s: typeof serverState) => unknown) => (selector ? selector(serverState) : serverState),
  useConn: () => ({ url: '', org: 'o' }),
}))

const agent = (over: Partial<Agent> = {}) => ({ id: 'a1', clusterId: 'c1', status: 'approved', accessTier: 0, installedTier: 0, name: 'edge-1', ...over }) as Agent

beforeEach(() => reloadInfo.mockClear())

describe('ConsentPanel widen command', () => {
  test('names the agent\'s own release and namespace', () => {
    render(<ConsentPanel agent={agent({ namespace: 'observability', releaseName: 'agent-eu' })} />)
    expect(reloadInfo).toHaveBeenCalledTimes(1)
    const cmd = screen.getByTestId('helm-command').textContent ?? ''
    expect(cmd).toBe('helm upgrade agent-eu oci://registry.example.com/team/continuum-agent --version 0.9.0 --namespace observability --reset-then-reuse-values --set access.tier=1')
  })

  test('falls back to the documented defaults while the agent has not said where it lives', () => {
    render(<ConsentPanel agent={agent()} />)
    expect(screen.getByTestId('helm-command').textContent).toContain('helm upgrade continuum-agent oci://registry.example.com/team/continuum-agent --version 0.9.0 --namespace continuum-system --reset-then-reuse-values')
  })
})
