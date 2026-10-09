import { fireEvent, render, screen } from '@testing-library/react'
import { describe, expect, test, vi } from 'vitest'
import ClusterStep from '@/components/telemetry/ClusterStep'
import type { Agent, Cluster } from '@/lib/types'

const agent = (id: string, over: Partial<Agent> = {}) => ({ id, name: `${id}-agent`, clusterId: `cl-${id}`, status: 'approved', connected: true, ...over }) as Agent
const cluster = (id: string) => ({ id: `cl-${id}`, name: `${id}-cluster`, tier: 'cloud' }) as Cluster
const diagnostics = (installed: string[]) => ({ reportedAt: new Date().toISOString(), installedTelemetry: installed })
const raw = (id: string, installed: string[]) => ({ id, diagnostics: diagnostics(installed) })

describe('ClusterStep: Where from', () => {
  test('no cluster with an approved agent: an empty state with the way to connect one', () => {
    const onConnect = vi.fn()
    render(<ClusterStep agents={[]} clusters={[]} rawAgents={[]} selected={undefined} onSelect={() => undefined} onConnect={onConnect} testIdPrefix="t" />)
    expect(screen.getByText('Connect a cluster first')).toBeInTheDocument()
    fireEvent.click(screen.getByTestId('t-connect'))
    expect(onConnect).toHaveBeenCalled()
  })

  test('one radio per cluster, saying what it sends today; picking one reports it', () => {
    const onSelect = vi.fn()
    render(<ClusterStep agents={[agent('a'), agent('b')]} clusters={[cluster('a'), cluster('b')]} rawAgents={[raw('a', ['resourceUsage']), raw('b', [])]} selected="b" onSelect={onSelect} onConnect={() => undefined} testIdPrefix="t" />)
    const rows = screen.getAllByRole('radio')
    expect(rows).toHaveLength(2)
    expect(rows[0]).toHaveTextContent(/Sends metrics/)
    expect(rows[1]).toHaveTextContent('No telemetry set up yet')
    expect(rows[1]).toHaveAttribute('aria-checked', 'true')
    fireEvent.click(rows[0])
    expect(onSelect).toHaveBeenCalledWith('a')
  })

  test('a cluster whose agent is not connected can still be chosen, and says what that costs', () => {
    render(<ClusterStep agents={[agent('a', { connected: false })]} clusters={[cluster('a')]} rawAgents={[]} selected={undefined} onSelect={() => undefined} onConnect={() => undefined} testIdPrefix="t" />)
    expect(screen.getByTestId('t-offline')).toHaveTextContent(/not connected.*still set this up/)
  })
})
