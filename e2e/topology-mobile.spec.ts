import { expect, test, type Page } from '@playwright/test'

/**
 * On a phone the Inspector is a sheet over the bottom of the canvas. What a person went to (by the problems pill, or by a tap) must end up
 * in the part of the canvas the sheet leaves, never behind it. Layout, not logic, so only a real browser can say.
 */

async function loadSampleTopology(page: Page) {
  await page.goto('/settings', { waitUntil: 'networkidle' })
  await page.getByRole('button', { name: /Load sample/i }).click()
  await page.getByRole('button', { name: 'Replace', exact: true }).click()
  await expect(page.getByText(/Sample data/i)).toBeVisible({ timeout: 10_000 })
}

test.use({ viewport: { width: 390, height: 844 }, hasTouch: true })
// A phone-sized page with the whole sample on it is heavy; one at a time keeps a small CI runner from losing the tab.
test.describe.configure({ mode: 'serial' })

test.describe('topology on a phone', () => {
  test('the node the problems pill goes to is in the visible canvas, above the sheet', async ({ page }) => {
    await loadSampleTopology(page)
    await page.goto('/topology', { waitUntil: 'networkidle' })
    await page.waitForSelector('.react-flow__node', { timeout: 15_000 })
    const pill = page.getByTestId('problems-pill')
    // The sample is healthy enough to have nothing wrong on some builds; the assertion is about where a selection lands, so there must be one.
    test.skip(!(await pill.isVisible().catch(() => false)), 'the sample has no problem to go to')
    await pill.getByRole('button', { name: /need|needs/ }).click()
    const sheet = page.locator('aside.modal-pop')
    await expect(sheet).toBeVisible()
    await page.waitForTimeout(900) // the move is 450ms
    const node = page.locator('.react-flow__node.selected').first()
    const nb = (await node.boundingBox())!
    const sb = (await sheet.boundingBox())!
    const pane = (await page.locator('.react-flow__pane').first().boundingBox())!
    expect(nb.y).toBeGreaterThanOrEqual(pane.y - 1)
    expect(nb.y + nb.height).toBeLessThanOrEqual(sb.y + 1)
    // The sheet opens as a header: most of the screen is still canvas.
    expect(sb.height).toBeLessThan(844 * 0.3)
  })

  test('the header is two rows and the same height whichever view is open', async ({ page }) => {
    await loadSampleTopology(page)
    await page.goto('/topology', { waitUntil: 'networkidle' })
    await page.waitForSelector('.react-flow__node', { timeout: 15_000 })
    const bar = page.locator('h1', { hasText: 'Topology' }).locator('xpath=..')
    const app = (await bar.boundingBox())!.height
    await page.getByRole('tab', { name: /^Infrastructure$/ }).click()
    await page.waitForSelector('.react-flow__node')
    const infra = (await bar.boundingBox())!.height
    expect(Math.abs(app - infra)).toBeLessThan(2)
    expect(app).toBeLessThan(120)
  })
})
