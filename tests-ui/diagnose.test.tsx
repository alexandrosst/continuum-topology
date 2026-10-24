import { fireEvent, render, screen } from '@testing-library/react'
import { describe, expect, test, vi } from 'vitest'
import { DiagnoseDrawer } from '@/features/agents/components/DiagnoseDrawer'
import { diagnoseAgent, diagnosisText } from '@/lib/diagnose'
import type { AgentDiagnostics, AgentExtras } from '@/lib/consent'
import type { Agent } from '@/lib/types'

const NOW = Date.parse('2026-10-01T12:00:00Z')
const ago = (s: number) => new Date(NOW - s * 1000).toISOString()
const ahead = (s: number) => new Date(NOW + s * 1000).toISOString()

const agent = (over: Partial<Agent> = {}): Agent => ({ id: 'a1', orgId: 'o', name: 'edge-1', clusterId: 'c1', version: '1.4.0', accessTier: 2, status: 'approved', fingerprint: 'x', modules: [], connected: true, lastHeartbeat: ago(10), lateAfterSeconds: 75, certExpiresAt: ahead(20 * 3600), namespace: 'ikhnos-system', link: { connects: 1, bytes: 1, syncs: 1, flows: 0, measurements: 0, heartbeats: 5, connectedSince: ago(3600) }, ...over }) as Agent

const diag = (over: Partial<AgentDiagnostics> = {}): AgentDiagnostics => ({ reportedAt: ago(30), agentVersion: '1.4.0', uptimeSeconds: 3600, installedTier: 2, approvedTier: 2, effectiveTier: 2, pausedCollectors: [], excludedNamespaces: 0, collectors: [], informers: [{ name: 'pods', synced: true, objects: 3 }], problems: [], ...over })

const well: AgentExtras = {
  diagnostics: diag(),
  conditions: [
    { type: 'Connected', status: 'True', reason: 'Open', message: 'a stream from the agent is open' },
    { type: 'ConfigInForce', status: 'True', reason: 'Echoed', message: 'the agent names the Config last sent as the one in force' },
    { type: 'Compatible', status: 'True', reason: 'Current', message: 'the agent speaks protocol 3' },
  ],
}

describe('diagnoseAgent', () => {
  test('an agent that is well says so, and still shows what was checked', () => {
    const dx = diagnoseAgent(agent(), well, NOW)
    expect(dx.verdict).toBe('healthy')
    expect(dx.findings).toEqual([])
    expect(dx.checks.map((c) => c.id)).toEqual(expect.arrayContaining(['stream', 'heartbeat', 'cond-ConfigInForce', 'cond-Compatible', 'cert', 'tier', 'watches', 'report']))
    expect(dx.checks.every((c) => c.tone === 'healthy' || c.tone === 'unknown')).toBe(true)
  })

  test('a closed stream is the first thing to do, with the commands to look at the pod', () => {
    const dx = diagnoseAgent(agent({ connected: false, lastHeartbeat: ago(7200) }), { ...well, conditions: [{ type: 'Connected', status: 'False', reason: 'NoStream', message: 'no stream from the agent is open' }] }, NOW)
    expect(dx.verdict).toBe('down')
    expect(dx.findings[0]).toMatchObject({ id: 'stream', tone: 'down' })
    expect(dx.findings[0].command).toContain('kubectl -n ikhnos-system logs deploy/ikhnos-agent')
  })

  test('findings are worst first: a failed stream, then a late clock, then a notice', () => {
    const dx = diagnoseAgent(agent({ connected: false, clockSkewMs: 5 * 60_000, consistency: { lastCheck: ago(60), checks: 4, differences: 2 } }), well, NOW)
    expect(dx.findings.map((f) => f.tone)).toEqual(['down', 'attention', 'notice'])
  })

  test('an expired certificate is not working; one nearly out on a live agent needs attention', () => {
    expect(diagnoseAgent(agent({ certExpiresAt: ago(3600) }), well, NOW).findings[0]).toMatchObject({ id: 'cert', tone: 'down' })
    expect(diagnoseAgent(agent({ certExpiresAt: ahead(1800) }), well, NOW).findings[0]).toMatchObject({ id: 'cert-soon', tone: 'attention' })
  })

  test('the server saying the agent has not echoed its configuration is a finding with the log command', () => {
    const dx = diagnoseAgent(agent(), { ...well, conditions: [well.conditions![0], { type: 'ConfigInForce', status: 'False', reason: 'NotEchoed', message: 'the agent names an earlier Config as the one in force' }] }, NOW)
    expect(dx.findings[0]).toMatchObject({ id: 'config', tone: 'attention' })
  })

  test('what the agent reports about itself is carried over with its severity, errors before warnings', () => {
    const dx = diagnoseAgent(agent(), { ...well, diagnostics: diag({ problems: [{ code: 'clock_skew', severity: 'warn', message: 'w', since: ago(5) }, { code: 'rbac_forbidden', severity: 'error', message: 'e', since: ago(5) }] }) }, NOW)
    expect(dx.findings.map((f) => f.id)).toEqual(['problem-rbac_forbidden', 'problem-clock_skew'])
    expect(dx.verdict).toBe('down')
  })

  test('without a self-report the verdict is unknown, not healthy', () => {
    const dx = diagnoseAgent(agent(), { conditions: well.conditions }, NOW)
    expect(dx.verdict).toBe('unknown')
  })

  test('a pending agent has one question: its approval', () => {
    const dx = diagnoseAgent(agent({ status: 'pending', connected: undefined }), {}, NOW)
    expect(dx.verdict).toBe('attention')
    expect(dx.findings).toHaveLength(1)
    expect(dx.findings[0].fix).toMatch(/Approve/)
  })

  test('a connection that keeps being re-made is called out', () => {
    const dx = diagnoseAgent(agent({ link: { connects: 9, bytes: 1, syncs: 1, flows: 0, measurements: 0, heartbeats: 1, connectedSince: ago(120) } }), well, NOW)
    expect(dx.findings.map((f) => f.id)).toContain('flapping')
  })

  test('the copied summary carries the verdict and each finding, and no address', () => {
    const a = agent({ connected: false, connectingIp: '203.0.113.9' })
    const dx = diagnoseAgent(a, well, NOW)
    const text = diagnosisText(a, well, dx, NOW)
    expect(text).toContain('edge-1')
    expect(text).toContain('The agent is not connected')
    expect(text).not.toContain('203.0.113.9')
  })
})

vi.mock('@/store/server', () => ({
  useServer: (sel?: (s: unknown) => unknown) => { const s = { isAdmin: () => false, refresh: async () => {} }; return sel ? sel(s) : s },
}))
vi.mock('@/features/fusion/components/useFusion', () => ({ useFusion: () => ({ status: null }) }))

describe('DiagnoseDrawer', () => {
  test('shows the verdict, the to-do list in order, and everything that was checked', () => {
    render(<DiagnoseDrawer agent={agent({ connected: false })} extras={well} now={NOW} onClose={() => {}} />)
    expect(screen.getByTestId('diagnose-verdict')).toHaveAttribute('data-verdict', 'down')
    expect(screen.getAllByTestId('diagnose-finding')[0]).toHaveAttribute('data-finding', 'stream')
    expect(screen.getAllByTestId('diagnose-check').length).toBeGreaterThan(5)
    expect(screen.queryByTestId('diagnose-grafana')).toBeNull()
  })

  test('Escape closes it', () => {
    const close = vi.fn()
    render(<DiagnoseDrawer agent={agent()} extras={well} now={NOW} onClose={close} />)
    fireEvent.keyDown(document, { key: 'Escape' })
    expect(close).toHaveBeenCalled()
  })
})
