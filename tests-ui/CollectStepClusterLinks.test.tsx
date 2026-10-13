import { fireEvent, render, screen } from '@testing-library/react'
import { useState } from 'react'
import { describe, expect, it } from 'vitest'
import CollectStep from '@/components/telemetry/CollectStep'
import { emptyTelemetry, telemetryProblems, type TelemetryInput } from '@/lib/install'

function Harness({ onValue }: { onValue: (v: TelemetryInput) => void }) {
  const [v, setV] = useState<TelemetryInput>({ ...emptyTelemetry, exportEndpoint: 'x:4317' })
  onValue(v)
  return <CollectStep value={v} onChange={setV} testIdPrefix="t" />
}

describe('CollectStep: Cluster links', () => {
  it('offers probing and throughput only once the signal is on, and asks for the Secret with throughput', () => {
    let latest = emptyTelemetry
    render(<Harness onValue={(v) => (latest = v)} />)
    expect(screen.queryByTestId('t-links-probe')).toBeNull()
    fireEvent.click(screen.getByTestId('t-clusterLinks'))
    expect(latest.clusterLinks).toBe(true)
    expect(screen.getByTestId('t-links-probe')).toBeChecked()
    expect(screen.queryByTestId('t-links-secret')).toBeNull()
    fireEvent.click(screen.getByTestId('t-links-throughput'))
    expect(screen.getByTestId('t-links-secret')).toBeInTheDocument()
    expect(telemetryProblems(latest).join(' ')).toMatch(/Secret that holds the iperf3 credentials/)
    fireEvent.change(screen.getByTestId('t-links-secret'), { target: { value: 'continuum-link-iperf' } })
    expect(telemetryProblems(latest)).toEqual([])
    fireEvent.click(screen.getByTestId('t-links-probe'))
    expect(screen.queryByTestId('t-links-throughput')).toBeNull()
  })
})
