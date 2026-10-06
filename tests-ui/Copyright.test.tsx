import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { Copyright, OWNER } from '@/components/ui/brand'

describe('Copyright', () => {
  it('names the owner and credits the lab with a link to its site', () => {
    render(<Copyright />)
    expect(screen.getByTestId('copyright').textContent).toContain(OWNER)
    const link = screen.getByTestId('lab-link')
    expect(link.getAttribute('href')).toBe('https://www.netmode.ntua.gr/')
    expect(link.getAttribute('aria-label')).toBe('NETMODE, NTUA')
    // The logo is drawn through <use> so its lettering follows the theme's text colour; an <img> could not.
    expect(link.querySelector('use')?.getAttribute('href')).toBe('/brand/lab/netmode.svg#netmode')
    expect(link.querySelector('img')).toBeNull()
    // It leaves the app, so it must not hand the new page a handle back to this one.
    expect(link.getAttribute('rel')).toContain('noopener')
    expect(link.getAttribute('target')).toBe('_blank')
  })
})
