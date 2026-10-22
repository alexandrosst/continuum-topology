import { fireEvent, render, screen } from '@testing-library/react'
import { beforeEach, describe, expect, test, vi } from 'vitest'
import { NeedsLook, SourcesFooter } from '@/features/topology/components/EvidenceSection'
import type { ModelAttr, ModelEntity } from '@/lib/provenance'

const confirmField = vi.fn()
let entity: ModelEntity | undefined

vi.mock('@/store/topology', () => ({
  useTopology: (selector: (s: { confirmField: typeof confirmField }) => unknown) => selector({ confirmField }),
}))
vi.mock('@/store/server', () => ({
  useServer: (selector: (s: { user: { username: string }; state: { agents: { id: string; name: string }[] } }) => unknown) =>
    selector({ user: { username: 'alex' }, state: { agents: [{ id: 'ag1', name: 'kestrel-cluster' }] } }),
}))
vi.mock('@/store/effectiveModel', () => ({ useEntityEvidence: () => entity }))

const rec = (over: Record<string, unknown> = {}) => ({ id: 'cl-1', source: 'discovered' as const, region: 'Dublin', version: 'v1.29', ...over })
const attr = (confidence: ModelAttr['confidence'], value: unknown = 'x'): ModelAttr => ({ value, source: 'agent', agentId: 'ag1', confidence, state: 'live' })
const model = (attributes: Record<string, ModelAttr>): ModelEntity => ({ kind: 'cluster', id: 'cl-1', name: 'c', origin: 'observed', state: 'live', attributes })

beforeEach(() => {
  confirmField.mockReset()
  entity = undefined
})

describe('NeedsLook', () => {
  test('draws nothing when no value is guessed or unknown', () => {
    const { container } = render(<NeedsLook kind="cluster" rec={rec()} />)
    expect(container).toBeEmptyDOMElement()
  })

  test('lists a guessed value with the signal it rests on, and keeps it as it is on one click', () => {
    render(<NeedsLook kind="cluster" rec={rec({ evidence: { tier: { signal: 'node count', confidence: 'low', detail: 'two nodes' } } })} />)
    expect(screen.getByText('Needs a look (1)')).toBeInTheDocument()
    expect(screen.getByText('node count (two nodes)')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Keep as is' }))
    expect(confirmField).toHaveBeenCalledWith('cluster', 'cl-1', 'tier', 'alex')
  })

  test('an unknown value is listed without a keep action: there is no value to keep', () => {
    render(<NeedsLook kind="cluster" rec={rec({ version: undefined })} />)
    expect(screen.getByText('Version')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Keep as is' })).not.toBeInTheDocument()
  })

  test('a value a person set is theirs and is never listed', () => {
    const { container } = render(<NeedsLook kind="cluster" rec={rec({ version: undefined, overrides: { version: 'v1.30' } })} />)
    expect(container).toBeEmptyDOMElement()
  })
})

describe('SourcesFooter', () => {
  test('draws nothing without a model entity', () => {
    const { container } = render(<SourcesFooter kind="cluster" id="cl-1" />)
    expect(container).toBeEmptyDOMElement()
  })

  test('says how many facts are reported, inferred or to check, and opens the full rows only on request', () => {
    entity = model({ cni: attr('reported'), cpu: attr('measured'), ingress: attr('reported'), provider: attr('inferred'), region: attr('guess') })
    render(<SourcesFooter kind="cluster" id="cl-1" />)
    expect(screen.getByText('5 facts · 3 reported or measured · 1 inferred · 1 to check')).toBeInTheDocument()
    expect(screen.queryByTestId('evidence-rows')).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Show sources' }))
    expect(screen.getByTestId('evidence-rows')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Hide sources' }))
    expect(screen.queryByTestId('evidence-rows')).not.toBeInTheDocument()
  })

  test('leaves out a count that is zero', () => {
    entity = model({ cni: attr('reported') })
    render(<SourcesFooter kind="cluster" id="cl-1" />)
    expect(screen.getByText('1 fact · 1 reported or measured')).toBeInTheDocument()
  })
})
