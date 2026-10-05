import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import { describe, expect, test, vi } from 'vitest'
import ProcessStep from '@/components/telemetry/ProcessStep'
import { emptyTelemetry, TAG_LIMIT, type TelemetryInput } from '@/lib/install'

// The guided wizard's Process step: what Continuum adds on its own, the tags a person adds, masking and
// cost, and the debug exporter. All of it is plain state on the telemetry draft; nothing here fetches.

let latest: TelemetryInput = emptyTelemetry
const onContinue = vi.fn()

function Wrapper({ initial = { ...emptyTelemetry, resourceUsage: true }, clusterName }: { initial?: TelemetryInput; clusterName?: string }) {
  const [value, setValue] = useState(initial)
  return <ProcessStep value={value} onChange={(v) => { latest = v; setValue(v) }} testIdPrefix="t" clusterName={clusterName} onBack={() => undefined} onContinue={onContinue} />
}

describe('ProcessStep', () => {
  test('says what is always added, and mentions no scope tag when nothing is narrowed', () => {
    render(<Wrapper />)
    expect(screen.getByTestId('t-guided-fact-org')).toHaveTextContent('continuum.org.id')
    expect(screen.getByTestId('t-guided-fact-cluster')).toHaveTextContent('continuum.cluster.id')
    expect(screen.queryByTestId('t-guided-fact-scope')).not.toBeInTheDocument()
    expect(screen.getByTestId('t-guided-process-auto')).toHaveTextContent('covers everything the agent can see')
  })

  test('a narrowed application signal adds the scope tag, with its value', () => {
    render(<Wrapper initial={{ ...emptyTelemetry, traces: true, tracesScope: { namespaces: ['shop'], exclude: [] } }} />)
    expect(screen.getByTestId('t-guided-fact-scope')).toHaveTextContent('continuum.scope=shop')
  })

  test('tags: add, edit and remove a row; the count follows what is filled in', async () => {
    const user = userEvent.setup()
    render(<Wrapper />)
    expect(screen.getByTestId('t-tag-count')).toHaveTextContent('0 of 8')
    await user.click(screen.getByTestId('t-tag-add'))
    await user.type(screen.getByTestId('t-tag-key-0'), 'team')
    await user.type(screen.getByTestId('t-tag-value-0'), 'payments')
    expect(latest.tags).toEqual([{ key: 'team', value: 'payments' }])
    expect(screen.getByTestId('t-tag-count')).toHaveTextContent('1 of 8')
    await user.click(screen.getByTestId('t-tag-remove-0'))
    expect(latest.tags).toEqual([])
  })

  test('the cluster name is offered in one click, and not again once it is there', async () => {
    const user = userEvent.setup()
    render(<Wrapper clusterName="prod-eu" />)
    await user.click(screen.getByTestId('t-tag-suggest'))
    expect(latest.tags).toEqual([{ key: 'k8s.cluster.name', value: 'prod-eu' }])
    expect(screen.queryByTestId('t-tag-suggest')).not.toBeInTheDocument()
  })

  test('the continuum. prefix is refused with a reason, and Continue waits until it is fixed', async () => {
    const user = userEvent.setup()
    render(<Wrapper />)
    await user.click(screen.getByTestId('t-tag-add'))
    await user.type(screen.getByTestId('t-tag-key-0'), 'continuum.org.id')
    await user.type(screen.getByTestId('t-tag-value-0'), 'me')
    expect(screen.getByTestId('t-tag-problems')).toHaveTextContent('reserved')
    expect(screen.getByTestId('t-guided-continue')).toBeDisabled()
    await user.clear(screen.getByTestId('t-tag-key-0'))
    await user.type(screen.getByTestId('t-tag-key-0'), 'team')
    expect(screen.queryByTestId('t-tag-problems')).not.toBeInTheDocument()
    await user.click(screen.getByTestId('t-guided-continue'))
    expect(onContinue).toHaveBeenCalled()
  })

  test(`no more than ${TAG_LIMIT} tags can be added`, () => {
    const tags = Array.from({ length: TAG_LIMIT }, (_, i) => ({ key: `k${i}`, value: 'v' }))
    render(<Wrapper initial={{ ...emptyTelemetry, resourceUsage: true, tags }} />)
    expect(screen.getByTestId('t-tag-add')).toBeDisabled()
    expect(screen.getByTestId('t-tag-count')).toHaveTextContent('8 of 8')
  })

  test('debug output starts as count-only; full content warns about secrets and the feedback loop; off switches it off', async () => {
    const user = userEvent.setup()
    render(<Wrapper />)
    expect(screen.getByTestId('t-debug-basic')).toBeChecked()
    expect(screen.queryByTestId('t-debug-warning')).not.toBeInTheDocument()
    await user.click(screen.getByTestId('t-debug-detailed'))
    expect(latest.debugVerbosity).toBe('detailed')
    expect(screen.getByTestId('t-debug-warning')).toHaveTextContent('feed straight back')
    await user.click(screen.getByTestId('t-debug-off'))
    expect(latest.debugVerbosity).toBe('')
    expect(screen.queryByTestId('t-debug-warning')).not.toBeInTheDocument()
  })

  test('masking is on by default, and sampling is only offered once traces are on', async () => {
    const user = userEvent.setup()
    const { unmount } = render(<Wrapper />)
    expect(screen.getByTestId('t-redaction')).toBeChecked()
    expect(screen.queryByTestId('t-traces-sampling')).not.toBeInTheDocument()
    await user.click(screen.getByTestId('t-redaction'))
    expect(latest.redaction).toBe(false)
    unmount()
    render(<Wrapper initial={{ ...emptyTelemetry, traces: true }} />)
    expect(screen.getByTestId('t-traces-sampling')).toBeInTheDocument()
  })
})
