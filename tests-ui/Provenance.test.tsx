import { render, screen } from '@testing-library/react'
import { describe, expect, test } from 'vitest'
import { Provenance } from '@/components/ui/primitives'

// The shared "Source / Confidence / freshness" strip (see primitives.tsx's own doc comment) that Part 1 of
// the Inspector provenance-stack consolidation introduced to replace six separate per-entity
// reimplementations (Origin, WeakValues, Why, a node's "Not sure" prose, an external endpoint's bespoke
// "seen in traffic" line, and a dependency's un-colored Found by/Confidence rows).
describe('Provenance', () => {
  test('always shows the label and the source, even with nothing else to say', () => {
    render(<Provenance source="Detected" />)
    expect(screen.getByText('Provenance')).toBeInTheDocument()
    expect(screen.getByText('Detected')).toBeInTheDocument()
    expect(screen.queryByTestId('provenance-confidence')).not.toBeInTheDocument()
    expect(screen.queryByTestId('provenance-freshness')).not.toBeInTheDocument()
  })

  test('the confidence chip carries the tone class and the title as its explanation', () => {
    render(<Provenance source="node probe" confidence={{ tone: 'warn', label: 'medium', title: 'inferred from the chassis' }} />)
    const chip = screen.getByTestId('provenance-confidence')
    expect(chip).toHaveTextContent('medium')
    expect(chip).toHaveAttribute('title', 'inferred from the chassis')
    expect(chip.className).toMatch(/warn/)
  })

  test('freshness renders "Seen Xm ago" from the timestamp, with an optional note appended', () => {
    const at = new Date(Date.now() - 5 * 60_000).toISOString()
    render(<Provenance source="Detected" freshness={{ label: 'Seen', at, note: 'found in traffic, not declared anywhere' }} />)
    expect(screen.getByTestId('provenance-freshness')).toHaveTextContent('Seen 5 min ago · found in traffic, not declared anywhere')
  })

  test('"First seen" is a distinct wording from "Seen", not a palette of its own', () => {
    const at = new Date(Date.now() - 3 * 86400_000).toISOString()
    render(<Provenance source="declared + observed" freshness={{ label: 'First seen', at }} />)
    expect(screen.getByTestId('provenance-freshness')).toHaveTextContent('First seen 3 d ago')
  })

  test('a secondary badge sits next to Source for a genuinely entity-specific caveat, not a second wording system', () => {
    render(<Provenance source="observed" sourceBadge={<span data-testid="via-badge">eBPF</span>} />)
    expect(screen.getByTestId('via-badge')).toBeInTheDocument()
  })
})
