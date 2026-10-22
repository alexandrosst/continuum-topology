import { fireEvent, render, screen } from '@testing-library/react'
import { useState } from 'react'
import { describe, expect, it } from 'vitest'
import CollectStep from '@/features/pipeline/components/telemetry/CollectStep'
import { LINK_REQUIREMENT } from '@/lib/clusterLinks'
import { emptyTelemetry, telemetryProblems, type TelemetryInput } from '@/lib/install'

function Harness({ onValue }: { onValue: (v: TelemetryInput) => void }) {
  const [v, setV] = useState<TelemetryInput>({ ...emptyTelemetry, exportEndpoint: 'x:4317' })
  onValue(v)
  return <CollectStep value={v} onChange={setV} testIdPrefix="t" />
}

describe('CollectStep: Cluster links', () => {
  it('offers probing and throughput only once the signal is on; the Secret is optional and tucked away', () => {
    let latest = emptyTelemetry
    render(<Harness onValue={(v) => (latest = v)} />)
    expect(screen.queryByTestId('t-links-probe')).toBeNull()
    fireEvent.click(screen.getByTestId('t-clusterLinks'))
    expect(latest.clusterLinks).toBe(true)
    expect(screen.getByTestId('t-links-probe')).toBeChecked()
    // The ports and the no-NAT rule are what cost the most debugging when missed, so they are said where probing is chosen.
    expect(screen.getByTestId('t-links-probe').closest('label')).toHaveTextContent(/TCP 8861 and UDP 18900–18919 with no address translation \(NAT\)/)
    expect(screen.queryByTestId('t-links-own-credentials')).toBeNull()
    fireEvent.click(screen.getByTestId('t-links-throughput'))
    // Nothing is required: Ikhnos issues the credentials, and the field sits closed under an Advanced disclosure.
    expect(telemetryProblems(latest)).toEqual([])
    const details = screen.getByTestId('t-links-own-credentials') as HTMLDetailsElement
    expect(details.open).toBe(false)
    expect(details).toHaveTextContent('Advanced: use my own iperf3 credentials')
    expect(details).toHaveTextContent(/Ikhnos issues the credentials/)
    fireEvent.change(screen.getByTestId('t-links-secret'), { target: { value: 'Bad_Name' } })
    expect(telemetryProblems(latest).join(' ')).toMatch(/credential Secret name/)
    fireEvent.change(screen.getByTestId('t-links-secret'), { target: { value: 'continuum-link-iperf' } })
    expect(telemetryProblems(latest)).toEqual([])
    expect(latest.clusterLinksAuthSecret).toBe('continuum-link-iperf')
    fireEvent.click(screen.getByTestId('t-links-probe'))
    expect(screen.queryByTestId('t-links-throughput')).toBeNull()
  })

  it('says what a pair needs, in one sentence, as soon as the signal is on', () => {
    render(<Harness onValue={() => {}} />)
    expect(screen.queryByTestId('t-links-requirement')).toBeNull()
    fireEvent.click(screen.getByTestId('t-clusterLinks'))
    expect(screen.getByTestId('t-links-requirement')).toHaveTextContent(LINK_REQUIREMENT)
    expect(screen.getByTestId('t-links-requirement')).toHaveTextContent('send telemetry to FUSION (a local operator)')
  })
})
