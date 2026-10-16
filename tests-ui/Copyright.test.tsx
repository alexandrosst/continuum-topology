import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { Copyright, COPYRIGHT_YEAR, LICENSE, OWNER } from '@/components/ui/brand'

describe('Copyright', () => {
  it('says the code is open source under the Apache License 2.0, with the product name and the year of its first release', () => {
    render(<Copyright />)
    const text = screen.getByTestId('copyright').textContent ?? ''
    expect(text).toContain(`${OWNER} © ${COPYRIGHT_YEAR}`)
    expect(text).toContain('Open source under the Apache License 2.0')
    // Not a claim that no rights are given.
    expect(text.toLowerCase()).not.toContain('all rights reserved')
    const link = screen.getByTestId('license-link')
    expect(link.getAttribute('href')).toBe(LICENSE.url)
    expect(link.getAttribute('rel')).toContain('noopener')
    expect(link.getAttribute('target')).toBe('_blank')
  })

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
