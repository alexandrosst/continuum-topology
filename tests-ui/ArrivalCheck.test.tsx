import { render, screen } from '@testing-library/react'
import { describe, expect, test } from 'vitest'
import ArrivalCheck from '@/components/telemetry/ArrivalCheck'
import type { AgentDiagnostics, AgentExportHealth } from '@/lib/consent'

const NOW = Date.parse('2026-10-06T12:10:00Z')

function diag(h?: AgentExportHealth, o: { installed?: string[]; reportedAt?: string } = {}): AgentDiagnostics {
  return { reportedAt: o.reportedAt ?? '2026-10-06T12:09:00Z', agentVersion: '1', uptimeSeconds: 1, installedTier: 2, approvedTier: 2, effectiveTier: 2, pausedCollectors: [], excludedNamespaces: 0, collectors: [], informers: [], problems: [], installedTelemetry: o.installed ?? ['resourceUsage'], exportHealth: h }
}
const show = (d: AgentDiagnostics | undefined, wanted = ['resourceUsage'], extra: { connected?: boolean; passive?: boolean } = {}) =>
  render(<ArrivalCheck wanted={wanted} diagnostics={d} cluster="vradipus" now={NOW} {...extra} />)

describe('ArrivalCheck: Check that data arrives', () => {
  test('a state is a glyph, a word and a colour from the product vocabulary, per signal type, with its last data', () => {
    const h: AgentExportHealth = {
      podsReached: 3,
      podsFailed: 0,
      routes: [
        { exporter: 'otlphttp/metrics', signal: 'metrics', state: 'exporting', sent: 120, failed: 0, lastSentAt: '2026-10-06T12:08:00Z' },
        { exporter: 'otlphttp/logs', signal: 'logs', state: 'failing', sent: 0, failed: 12, lastFailedAt: '2026-10-06T12:09:30Z' },
      ],
    }
    show(diag(h, { installed: ['resourceUsage', 'systemLogs', 'traces'] }), ['resourceUsage', 'systemLogs', 'traces'])
    expect(screen.getByTestId('arrival-state')).toHaveTextContent('Not working')
    expect(screen.getByTestId('arrival-metrics')).toHaveAttribute('data-health', 'healthy')
    expect(screen.getByTestId('arrival-metrics')).toHaveTextContent('HealthyLast data 2 min ago')
    expect(screen.getByTestId('arrival-logs')).toHaveTextContent(/Not working.*No data yet.*12 sends failed/)
    expect(screen.getByTestId('arrival-traces')).toHaveTextContent(/Unknown.*No data yet/)
    // A failure says what to do, once.
    expect(screen.getByTestId('arrival-todo')).toHaveTextContent(/What to do: .*Where to send/)
  })

  test('data that is arriving says so and has nothing to do', () => {
    show(diag({ podsReached: 1, podsFailed: 0, routes: [{ exporter: 'otlp', signal: 'metrics', state: 'exporting', sent: 3, failed: 0, lastSentAt: '2026-10-06T12:09:40Z' }] }))
    expect(screen.getByTestId('arrival')).toHaveAttribute('data-health', 'healthy')
    expect(screen.getByRole('status')).toHaveTextContent('Data is arriving.')
    expect(screen.queryByTestId('arrival-todo')).toBeNull()
  })

  test('nothing reported at all is Unknown and says which agent to look at', () => {
    show(undefined)
    expect(screen.getByTestId('arrival-state')).toHaveTextContent('Unknown')
    expect(screen.getByTestId('arrival-todo')).toHaveTextContent(/discovery agent of vradipus/)
  })

  test('an agent that has stopped reporting says the picture is old, and an agent that is not connected says the same', () => {
    const h: AgentExportHealth = { podsReached: 1, podsFailed: 0, routes: [] }
    const { unmount } = show(diag(h, { reportedAt: '2026-10-06T10:00:00Z' }))
    expect(screen.getByRole('status')).toHaveTextContent(/has not reported for 2 h/)
    unmount()
    show(diag(h), ['resourceUsage'], { connected: false })
    expect(screen.getByTestId('arrival')).toHaveAttribute('data-health', 'unknown')
  })

  test('counters it cannot read are said to say nothing about the data; the partial read is named', () => {
    const { unmount } = show(diag({ podsReached: 0, podsFailed: 2, lastError: 'find x: no such host', routes: [] }))
    expect(screen.getByTestId('arrival')).toHaveAttribute('data-health', 'attention')
    expect(screen.getByRole('status')).toHaveTextContent(/cannot read the collectors.*find x: no such host/)
    expect(screen.queryByTestId('arrival-metrics')).toBeNull()
    unmount()
    show(diag({ podsReached: 2, podsFailed: 1, lastError: 'timeout', routes: [{ exporter: 'otlp', signal: 'metrics', state: 'exporting', sent: 3, failed: 0, lastSentAt: '2026-10-06T12:09:40Z' }] }))
    expect(screen.getByTestId('arrival-partial')).toHaveTextContent('1 of 3 collector reads failed')
  })

  test('passive (no command just run) never calls "no data yet" overdue', () => {
    show(diag({ podsReached: 1, podsFailed: 0, routes: [] }), ['resourceUsage'], { passive: true })
    expect(screen.getByTestId('arrival')).toHaveAttribute('data-health', 'unknown')
  })
})
