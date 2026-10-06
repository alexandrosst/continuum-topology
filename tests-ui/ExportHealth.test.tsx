import { render, screen } from '@testing-library/react'
import { describe, expect, test } from 'vitest'
import ExportHealth from '@/components/telemetry/ExportHealth'
import type { AgentDiagnostics, AgentExportHealth } from '@/lib/consent'

const NOW = Date.parse('2026-10-06T12:10:00Z')

function diag(h?: AgentExportHealth, reportedAt = '2026-10-06T12:09:00Z'): AgentDiagnostics {
  return { reportedAt, agentVersion: '1', uptimeSeconds: 1, installedTier: 2, approvedTier: 2, effectiveTier: 2, pausedCollectors: [], excludedNamespaces: 0, collectors: [], informers: [], problems: [], exportHealth: h }
}

describe('ExportHealth', () => {
  test('says nothing when no telemetry is installed', () => {
    const { container } = render(<ExportHealth installed={[]} diagnostics={diag()} now={NOW} />)
    expect(container).toBeEmptyDOMElement()
  })

  test('says so when the agent does not report it, and when nothing has been reported at all', () => {
    const { rerender } = render(<ExportHealth installed={['resourceUsage']} diagnostics={diag()} now={NOW} />)
    expect(screen.getByTestId('export-health-none')).toHaveTextContent(/not reporting export health/)
    rerender(<ExportHealth installed={['resourceUsage']} now={NOW} />)
    expect(screen.getByTestId('export-health-none')).toHaveTextContent('Not reported yet.')
  })

  test('shows each signal type with its state, exporter and what to make of it', () => {
    const h: AgentExportHealth = {
      podsReached: 3,
      podsFailed: 0,
      routes: [
        { exporter: 'otlphttp/metrics', signal: 'metrics', state: 'exporting', sent: 120, failed: 0, lastSentAt: '2026-10-06T12:09:40Z' },
        { exporter: 'otlphttp/logs', signal: 'logs', state: 'failing', sent: 0, failed: 12, lastFailedAt: '2026-10-06T12:09:30Z' },
      ],
    }
    render(<ExportHealth installed={['resourceUsage', 'systemLogs', 'traces']} diagnostics={diag(h)} now={NOW} />)
    expect(screen.getByTestId('export-health-metrics-state')).toHaveTextContent('Sending')
    expect(screen.getByTestId('export-health-metrics')).toHaveTextContent('otlphttp/metrics')
    expect(screen.getByTestId('export-health-logs-state')).toHaveTextContent('Failing')
    expect(screen.getByTestId('export-health-logs')).toHaveTextContent(/12 sends failed.*refusing the data or cannot be reached/)
    // Traces is installed but nothing has gone out through it yet.
    expect(screen.getByTestId('export-health-traces-state')).toHaveTextContent('Waiting for data')
  })

  test('a quiet signal is not called an error', () => {
    const h: AgentExportHealth = { podsReached: 1, podsFailed: 0, routes: [{ exporter: 'otlp', signal: 'logs', state: 'silent', sent: 40, failed: 0, lastSentAt: '2026-10-06T11:50:00Z' }] }
    render(<ExportHealth installed={['kubernetesEvents']} diagnostics={diag(h)} now={NOW} />)
    expect(screen.getByTestId('export-health-logs')).toHaveAttribute('data-state', 'silent')
    expect(screen.getByTestId('export-health-logs')).toHaveTextContent(/normal when nothing is producing logs/)
  })

  test('counters it cannot read are said to say nothing about the data', () => {
    const h: AgentExportHealth = { podsReached: 0, podsFailed: 2, lastError: 'find x: no such host', routes: [] }
    render(<ExportHealth installed={['resourceUsage']} diagnostics={diag(h)} now={NOW} />)
    expect(screen.getByTestId('export-health-unreadable')).toHaveTextContent(/cannot read.*find x: no such host.*cannot say whether data is arriving/)
    expect(screen.queryByTestId('export-health-metrics')).toBeNull()
  })

  test('some pods unreadable: the rest still counts, and the gap is named', () => {
    const h: AgentExportHealth = { podsReached: 2, podsFailed: 1, lastError: 'read 10.0.0.3:8888: timeout', routes: [{ exporter: 'otlp', signal: 'metrics', state: 'exporting', sent: 3, failed: 0, lastSentAt: '2026-10-06T12:09:40Z' }] }
    render(<ExportHealth installed={['resourceUsage']} diagnostics={diag(h)} now={NOW} />)
    expect(screen.getByTestId('export-health-metrics-state')).toHaveTextContent('Sending')
    expect(screen.getByTestId('export-health-partial')).toHaveTextContent('1 of 3 collector reads failed')
  })

  test('an agent that has stopped reporting says the picture is old', () => {
    const h: AgentExportHealth = { podsReached: 1, podsFailed: 0, routes: [] }
    render(<ExportHealth installed={['resourceUsage']} diagnostics={diag(h, '2026-10-06T10:00:00Z')} now={NOW} />)
    expect(screen.getByTestId('export-health-stale')).toHaveTextContent('last reported 2 h ago')
  })
})
