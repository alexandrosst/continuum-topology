import { expect, test, type ConsoleMessage, type Page } from '@playwright/test'

/**
 * A regression guard for two real production crashes on the topology canvas, both "Maximum update depth
 * exceeded" (React error #185 - React's own guard against an unbounded render loop), found the hard way: a
 * user report, then a live headless-browser investigation, then an adversarial multi-agent stress pass that
 * found a SECOND, deeper loop the first fix's own manual verification had missed. See the two commits that
 * fixed them for the full story of each root cause; this test exists so a third one doesn't need the same
 * afternoon of archaeology.
 *
 * Both bugs were genuine infinite render loops living entirely in the interaction between this app's state
 * and React Flow's own internal effects (SelectionListener, StoreUpdater) - nothing a unit test or a jsdom
 * component test can see, since React Flow's real drag/selection/store-sync behavior doesn't run in jsdom.
 * A real browser against the real, built app is the only thing that actually exercises this.
 *
 * Both crashes were also intermittent under light interaction - the first one needed the *previous* commit's
 * fix confirmed clean with a slow, deliberate click-through before an adversarial agent found the second one
 * under a much faster, more chaotic sequence. So this test errs toward doing the same: real click-and-drag
 * gestures (not just `.click()`), rapid-fire timing, and repeating the riskiest combination (view-toggling
 * interleaved with clicks, after prior multi-select activity) several times in one run, rather than a single
 * polite pass that might not land on the right timing to trip a loop that IS still there.
 */

async function loadSampleTopology(page: Page) {
  await page.goto('/settings', { waitUntil: 'networkidle' })
  await page.getByRole('button', { name: /Load sample/i }).click()
  // ConfirmModal's danger-action button defaults to "Delete" but SettingsPage overrides it with
  // confirmLabel for this specific action (it replaces the topology, it doesn't delete anything) - see
  // ConfirmModal.tsx's confirmLabel prop.
  await page.getByRole('button', { name: 'Replace', exact: true }).click()
  await expect(page.getByText(/Sample data/i)).toBeVisible({ timeout: 10_000 })
}

async function goToTopologyCanvas(page: Page) {
  await page.goto('/topology', { waitUntil: 'networkidle' })
  await page.waitForSelector('.react-flow__node', { timeout: 15_000 })
}

/** A real click-and-drag box-select gesture over the canvas, not just a `.click()` - see graph.ts's
 *  resyncNodes/syncSelected doc comments for why a node mid-*gesture* (not just mid-click) is the case that
 *  actually matters for this bug class. */
async function boxSelectDrag(page: Page) {
  const pane = page.locator('.react-flow__pane').first()
  // Guarded, same as every locator action below that can legitimately find nothing right now (the view
  // could be on Map, which has no .react-flow__pane at all) - an unguarded boundingBox() on a locator
  // matching zero elements waits with no timeout of its own and would otherwise hang until the whole
  // test's timeout kills it, which reads as a false "the page closed mid-sequence" failure rather than the
  // harmless "nothing to select right now" it actually is.
  const box = await pane.boundingBox({ timeout: 2000 }).catch(() => null)
  if (!box) return
  const start = { x: box.x + box.width * 0.15, y: box.y + box.height * 0.15 }
  const end = { x: box.x + box.width * 0.7, y: box.y + box.height * 0.7 }
  await page.mouse.move(start.x, start.y)
  await page.mouse.down()
  const steps = 10
  for (let s = 1; s <= steps; s++) {
    await page.mouse.move(start.x + ((end.x - start.x) * s) / steps, start.y + ((end.y - start.y) * s) / steps)
    await page.waitForTimeout(10)
  }
  await page.mouse.up()
}

/** Drags a single node a short distance - an actual drag gesture, exercising React Flow's own `dragging`
 *  flag rather than only its transient click-driven version of it. */
async function dragFirstNode(page: Page) {
  const node = page.locator('.react-flow__node').first()
  const box = await node.boundingBox({ timeout: 2000 }).catch(() => null) // see boxSelectDrag's own comment
  if (!box) return
  const cx = box.x + box.width / 2
  const cy = box.y + box.height / 2
  await page.mouse.move(cx, cy)
  await page.mouse.down()
  for (const [dx, dy] of [
    [5, 3],
    [12, 8],
    [20, 15],
    [28, 20],
  ]) {
    await page.mouse.move(cx + dx, cy + dy)
    await page.waitForTimeout(15)
  }
  await page.mouse.up()
}

/** Builds and tears down a multi-selection fast, several times - the state the second real bug needed
 *  already built up before its own trigger (view-toggling interleaved with clicks) would reproduce it. */
async function shiftSelectChurn(page: Page) {
  const nodes = page.locator('.react-flow__node')
  const count = await nodes.count()
  const pane = page.locator('.react-flow__pane').first()
  const paneBox = await pane.boundingBox({ timeout: 2000 }).catch(() => null) // see boxSelectDrag's own comment
  if (count < 2 || !paneBox) return
  for (let round = 0; round < 6; round++) {
    for (let i = 0; i < Math.min(3, count); i++) {
      await nodes.nth((round * 2 + i) % count).click({ modifiers: ['Shift'], force: true, timeout: 800 }).catch(() => {})
    }
    await page.mouse.click(paneBox.x + 5, paneBox.y + 5).catch(() => {})
    await page.waitForTimeout(15)
  }
}

/** The exact combination that produced the second, deeper crash: switching the Application/Infrastructure/
 *  Map view tabs (and the Filter/Options toolbar buttons) with node clicks interleaved and no delay between
 *  them. Run this AFTER shiftSelectChurn - the second bug never reproduced from a clean, unselected state. */
async function viewToggleWhileClicking(page: Page) {
  const tabNames = ['Application', 'Infrastructure', 'Map']
  const nodes = page.locator('.react-flow__node')
  for (let round = 0; round < 6; round++) {
    const tabName = tabNames[round % tabNames.length]
    const tab = page.getByRole('tab', { name: new RegExp(`^${tabName}$`, 'i') }).first()
    await tab.click({ force: true, timeout: 800 }).catch(() => {})
    const count = await nodes.count()
    if (count > 0) await nodes.nth(round % count).click({ force: true, timeout: 800 }).catch(() => {})
    const toolbarButton = round % 2 === 0 ? page.getByRole('button', { name: /Filter/i }).first() : page.getByRole('button', { name: /Options/i }).first()
    await toolbarButton.click({ force: true, timeout: 800 }).catch(() => {})
    const count2 = await nodes.count()
    if (count2 > 0) await nodes.nth((round + 1) % count2).click({ force: true, timeout: 800 }).catch(() => {})
    await page.waitForTimeout(10)
  }
  await page.keyboard.press('Escape').catch(() => {})
  // tabNames has 3 entries and this loop always runs 6 rounds, so it deterministically ends on 'Map' - leaving
  // it there would make every subsequent call to shiftSelectChurn (the reps loop below interleaves the two)
  // find zero React Flow nodes/pane and become a no-op, quietly losing most of this test's own repeat
  // coverage. Switch back to Application before returning so each rep genuinely repeats the same interaction.
  await page.getByRole('tab', { name: /^Application$/i }).first().click({ force: true, timeout: 800 }).catch(() => {})
}

test.describe('topology canvas', () => {
  test('heavy interaction never produces a console error or an uncaught exception', async ({ page }) => {
    // The interaction sequence below is deliberately long and repeats the riskiest combination several
    // times (see the block comments above) to reliably trip a timing-sensitive render loop if one is
    // still there - that takes real wall-clock time (~30s of pure interaction against known-good code).
    // Give this real headroom above Playwright's 30s default: a plain timeout looks identical whether the
    // app crashed or the test was merely slow, and we want a crash to fail via the clean assertion below,
    // not an ambiguous "Test timeout of 30000ms exceeded".
    test.setTimeout(90_000)

    const errors: string[] = []
    page.on('console', (msg: ConsoleMessage) => {
      if (msg.type() === 'error') errors.push(`[console.error] ${msg.text()}`)
    })
    page.on('pageerror', (err) => errors.push(`[pageerror] ${err.message}`))
    // A render loop severe enough can peg the renderer process hard enough for Chromium to kill and
    // restart the tab, rather than merely raising a JS error - Playwright surfaces that as a 'crash' event
    // on the page, not as a console message or pageerror. Without this listener, that failure mode shows
    // up only as an opaque "Target page, context or browser has been closed" wherever the next page.* call
    // happens to land - real evidence, but not a legible one. (This is exactly what the pre-fix code did
    // when this test was first validated against it: the page snapshot captured at that timeout showed the
    // app's own error boundary rendered "Minified React error #185" right before the tab went down.)
    let crashed = false
    page.on('crash', () => {
      crashed = true
      errors.push(
        '[page crash] the renderer process crashed (Chromium killed the tab) - almost always a runaway ' +
          'render loop pegging it hard enough to hang or OOM, not an ordinary JS error',
      )
    })

    await loadSampleTopology(page)
    await goToTopologyCanvas(page)
    // Errors from the "Load sample"/navigation sequence itself aren't the point of this test - only what
    // happens once real interaction starts.
    errors.length = 0

    try {
      // A fast click through every node on the canvas - the original, simplest crash trigger.
      const nodes = page.locator('.react-flow__node')
      const nodeCount = await nodes.count()
      expect(nodeCount, 'sample topology should have rendered at least one node').toBeGreaterThan(0)
      for (let i = 0; i < Math.max(35, nodeCount); i++) {
        await nodes.nth(i % nodeCount).click({ force: true, timeout: 1000 }).catch(() => {})
        if (i % 5 === 0) await page.waitForTimeout(5)
      }

      await boxSelectDrag(page)
      await page.mouse.click(10, 10).catch(() => {}) // deselect
      await dragFirstNode(page)

      // Repeat the riskiest combination a few times - this is what actually caught the second bug, and it
      // was timing-sensitive even with the bug present, so one pass isn't a reliable enough guard on its own.
      for (let rep = 0; rep < 3; rep++) {
        await shiftSelectChurn(page)
        await viewToggleWhileClicking(page)
      }

      // One more click storm, then sit idle - a self-sustaining loop (the actual second bug) keeps producing
      // errors with zero further input, which is exactly what this catches that a single snapshot wouldn't.
      const finalNodes = page.locator('.react-flow__node')
      const finalCount = await finalNodes.count()
      if (finalCount > 0) {
        for (let i = 0; i < 40; i++) {
          await finalNodes.nth(i % finalCount).click({ force: true, timeout: 500 }).catch(() => {})
        }
      }
      await page.waitForTimeout(3000)
    } catch (err) {
      // A hard crash mid-sequence throws from whatever page.* call was in flight when the tab went down -
      // caught here so the failure reads as "here are the errors we saw" instead of an unrelated-looking
      // stack trace pointing at a random line deep in the interaction helpers above. Anything else (a
      // genuine test bug, not a page crash) still isn't ours to swallow, so it's rethrown unchanged.
      if (!crashed && !page.isClosed()) throw err
      errors.push(`[interaction aborted] the page closed mid-sequence: ${err instanceof Error ? err.message : String(err)}`)
    }

    expect(errors, `expected zero console errors/uncaught exceptions, got ${errors.length}:\n${errors.join('\n---\n')}`).toEqual([])
  })

  test('clicking empty canvas clears a multi-selection, not just the single-click Inspector', async ({ page }) => {
    // A real, reported gap: `onPaneClick` used to only close the Inspector (the single-click `selection`
    // state) via `select(null)`. A shift/ctrl-click or box-drag multi-selection lives in a separate piece
    // of state (`multiSelectedIds`, mirrored from React Flow's own selection) that `select(null)` never
    // touched, so clicking empty canvas after multi-selecting a few cards left them all still highlighted
    // and the floating scope toolbar (NodeToolbar/ScopeFromSelection) still open.
    await loadSampleTopology(page)
    await goToTopologyCanvas(page)

    // Two services on the same cluster (cl-edge-a), which - unlike the cloud cluster - has an
    // *approved* agent in the sample data. ScopeFromSelection only renders real toolbar content
    // once every selected service's cluster has an approved agent (see its own `targets` gate);
    // picking two arbitrary nodes can land on a cluster with no agent at all and leave the
    // toolbar an empty, zero-size element that toBeVisible() correctly reports as not visible.
    // That's a property of ScopeFromSelection's own business logic, not of the pane-click fix
    // this test guards, so the nodes are chosen deliberately rather than by DOM order.
    const nodeA = page.locator('.react-flow__node[data-id="c:w-mqtt-a"]')
    const nodeB = page.locator('.react-flow__node[data-id="c:w-infer-a"]')
    await expect(nodeA).toBeVisible()
    await expect(nodeB).toBeVisible()
    await nodeA.click({ force: true })
    await page.waitForTimeout(100)
    await nodeB.click({ modifiers: ['Shift'], force: true })
    await page.waitForTimeout(1000)

    const toolbar = page.locator('.react-flow__node-toolbar')
    await expect(toolbar).toBeVisible({ timeout: 5000 })
    await expect(page.locator('.react-flow__node.selected')).toHaveCount(2)

    // A spot of the pane with nothing on it. It is looked up, not fixed: how the cards, pods and cluster boxes fall on the
    // canvas changes with the layout, and a fixed corner can land on a cluster box (which is what a click there then selects).
    const pane = page.locator('.react-flow__pane').first()
    const box = (await pane.boundingBox())!
    const spot = await page.evaluate(({ x, y, w, h }) => {
      for (let dy = 24; dy < h - 24; dy += 24) {
        for (let dx = w - 24; dx > 24; dx -= 24) {
          if (document.elementFromPoint(x + dx, y + dy)?.classList.contains('react-flow__pane')) return { x: x + dx, y: y + dy }
        }
      }
      return null
    }, { x: box.x, y: box.y, w: box.width, h: box.height })
    expect(spot, 'there is a free spot on the canvas to click').not.toBeNull()
    await page.mouse.click(spot!.x, spot!.y)

    await expect(toolbar).toBeHidden()
    await expect(page.locator('.react-flow__node.selected')).toHaveCount(0)
    await expect(page.locator('aside.modal-pop')).toBeHidden()
  })

  test('plain-clicking one node after a multi-selection replaces it, rather than adding to it', async ({ page }) => {
    // A real, reported gap: a shift/ctrl-click or box-drag multi-selection populates `multiSelectedIds`
    // (mirrored from React Flow's own selection via onSelectionChange), but a later *plain* click only ever
    // drove the single-click Inspector `selection` - nothing cleared the leftover multiSelectedIds, so the
    // effect that paints `.selected` onto the canvas (which unions both pieces of state) kept re-asserting
    // the stale multi-selection alongside whatever was just plain-clicked. Two services stayed highlighted
    // when only one should have been.
    await loadSampleTopology(page)
    await goToTopologyCanvas(page)

    const nodeA = page.locator('.react-flow__node[data-id="c:w-mqtt-a"]')
    const nodeB = page.locator('.react-flow__node[data-id="c:w-infer-a"]')
    const nodeC = page.locator('.react-flow__node[data-id="c:w-gw"]')
    await expect(nodeA).toBeVisible()
    await expect(nodeB).toBeVisible()
    await expect(nodeC).toBeVisible()

    // Build a two-node multi-selection, same gesture as the pane-click test above.
    await nodeA.click({ force: true })
    await page.waitForTimeout(100)
    await nodeB.click({ modifiers: ['Shift'], force: true })
    await page.waitForTimeout(500)
    await expect(page.locator('.react-flow__node.selected')).toHaveCount(2)

    // A single plain click on a THIRD, not-yet-selected node - no modifier at all - should leave exactly
    // that one selected, not it plus the leftover multi-selection.
    await nodeC.click({ force: true })
    await page.waitForTimeout(500)
    await expect(page.locator('.react-flow__node.selected')).toHaveCount(1)
    await expect(nodeC).toHaveClass(/selected/)

    // The narrower variant of the same bug: re-clicking A, which was ALSO the single-click selection
    // before the shift-click ever happened - a case where selectedRfId's own VALUE never actually
    // changes, so the usual "selectedRfId changed" trigger for repainting .selected doesn't fire either.
    await nodeA.click({ force: true })
    await page.waitForTimeout(100)
    await nodeB.click({ modifiers: ['Shift'], force: true })
    await page.waitForTimeout(500)
    await expect(page.locator('.react-flow__node.selected')).toHaveCount(2)
    await nodeA.click({ force: true })
    await page.waitForTimeout(500)
    await expect(page.locator('.react-flow__node.selected')).toHaveCount(1)
    await expect(nodeA).toHaveClass(/selected/)
  })
})

/**
 * The "Export PNG" button (TopologyPage.tsx's exportPng) renders the graph's full extent via
 * html-to-image, not just whatever is currently on screen - the same reason this lives in e2e rather than
 * a jsdom component test: html-to-image reads real computed styles and serializes a real DOM subtree,
 * neither of which jsdom does faithfully enough to trust a unit-level assertion here.
 */
test('Export PNG downloads a PNG file from the graph canvas', async ({ page }) => {
  await loadSampleTopology(page)
  await goToTopologyCanvas(page)

  // Calm keeps the canvas actions under the "..." menu.
  await page.getByRole('button', { name: 'More canvas actions' }).click()
  const [download] = await Promise.all([
    page.waitForEvent('download', { timeout: 15_000 }),
    page.getByRole('menuitem', { name: 'Export PNG' }).click(),
  ])
  expect(download.suggestedFilename()).toMatch(/^topology-.*\.png$/)
  const path = await download.path()
  expect(path).toBeTruthy()
})
