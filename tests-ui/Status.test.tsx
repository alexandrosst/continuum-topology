import { fireEvent, render, screen } from '@testing-library/react'
import { describe, expect, test } from 'vitest'
import { StatusBadge, StatusPip, STATUS_WORD, type StatusTone } from '@/components/ui/Status'
import { TabPanel, Tabs } from '@/components/ui/Tabs'

const TONES: StatusTone[] = ['healthy', 'attention', 'down', 'setup', 'unknown', 'off', 'notice']

describe('StatusBadge', () => {
  test.each(TONES)('%s says its words, with a glyph, so colour is never the only signal', (tone) => {
    const { container } = render(<StatusBadge tone={tone} />)
    expect(container).toHaveTextContent(STATUS_WORD[tone])
    expect(container.querySelector('svg')).not.toBeNull()
    expect(container.firstElementChild).toHaveAttribute('data-tone', tone)
  })

  test('a label replaces the words, and a compact count keeps them in the accessible name', () => {
    render(<StatusBadge tone="attention" label="needs attention" count={3} compact />)
    expect(screen.getByLabelText('needs attention')).toBeInTheDocument()
    expect(screen.getByText('3')).toBeInTheDocument()
  })

  test('the pip only pulses when asked', () => {
    expect(render(<StatusPip tone="healthy" />).container.querySelector('[class*="animate-ping"]')).toBeNull()
    expect(render(<StatusPip tone="healthy" pulse />).container.querySelector('[class*="animate-ping"]')).not.toBeNull()
  })
})

describe('Tabs', () => {
  function Demo({ onChange }: { onChange: (id: string) => void }) {
    return (
      <>
        <Tabs label="Views" idBase="v" value="b" onChange={onChange} items={[{ id: 'a', label: 'A' }, { id: 'b', label: 'B' }, { id: 'c', label: 'C', count: 2 }]} />
        <TabPanel idBase="v" id="b" active>panel b</TabPanel>
      </>
    )
  }

  test('only the open tab is in the tab order, and it names its panel', () => {
    render(<Demo onChange={() => {}} />)
    const tabs = screen.getAllByRole('tab')
    expect(tabs.map((t) => t.getAttribute('tabindex'))).toEqual(['-1', '0', '-1'])
    expect(tabs[1]).toHaveAttribute('aria-selected', 'true')
    expect(tabs[1]).toHaveAttribute('aria-controls', 'v-panel-b')
    expect(screen.getByRole('tabpanel')).toHaveAttribute('aria-labelledby', 'v-tab-b')
  })

  test('arrow keys, Home and End move between tabs and wrap', () => {
    const seen: string[] = []
    render(<Demo onChange={(id) => seen.push(id)} />)
    const b = screen.getByRole('tab', { name: 'B' })
    fireEvent.keyDown(b, { key: 'ArrowRight' })
    fireEvent.keyDown(b, { key: 'ArrowLeft' })
    fireEvent.keyDown(b, { key: 'Home' })
    fireEvent.keyDown(b, { key: 'End' })
    fireEvent.keyDown(screen.getByRole('tab', { name: /^C/ }), { key: 'ArrowRight' })
    expect(seen).toEqual(['c', 'a', 'a', 'c', 'a'])
  })
})
