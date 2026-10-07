/**
 * Putting text on the clipboard, for every copy button and every shown-once secret.
 *
 * `navigator.clipboard` only exists in a secure context: on a server reached over plain http (a LAN address, an IP and a port) it is simply
 * undefined, and where it does exist it still rejects when the page is not focused or permission was refused. A copy button that did
 * nothing in those cases reads as working, and for a token that is shown once that means a lost token. So this tries the Clipboard API, then
 * the older select-and-`execCommand('copy')` route through a hidden text area, and only then reports that it could not, so the caller can
 * say so and leave the text selected for the person's own Ctrl+C.
 */
export type CopyOutcome = 'copied' | 'manual'

/** The text selected on the page, the way a drag over it would: what Ctrl+C then copies. */
export function selectContents(el: Element | null | undefined): void {
  const sel = window.getSelection?.()
  if (!el || !sel) return
  const range = document.createRange()
  range.selectNodeContents(el)
  sel.removeAllRanges()
  sel.addRange(range)
}

function execCopy(text: string): boolean {
  if (typeof document.execCommand !== 'function') return false
  const opener = document.activeElement instanceof HTMLElement ? document.activeElement : null
  const area = document.createElement('textarea')
  area.value = text
  area.setAttribute('readonly', '')
  area.setAttribute('aria-hidden', 'true')
  // Off screen rather than display:none (a hidden element cannot be selected), and not scrolling the page to it.
  area.style.cssText = 'position:fixed;top:0;left:-9999px;opacity:0;pointer-events:none'
  document.body.appendChild(area)
  try {
    area.select()
    area.setSelectionRange(0, text.length)
    return document.execCommand('copy')
  } catch {
    return false
  } finally {
    area.remove()
    opener?.focus?.()
  }
}

/**
 * Copies `text`. 'copied' when the browser took it; 'manual' when neither route worked - then `select`, the element that shows the text
 * on the page, is left selected, so the caller only has to tell the person to press Ctrl+C.
 */
export async function copyToClipboard(text: string, select?: Element | null): Promise<CopyOutcome> {
  if (typeof navigator !== 'undefined' && navigator.clipboard?.writeText) {
    try {
      await navigator.clipboard.writeText(text)
      return 'copied'
    } catch {
      /* refused or unfocused: try the older route */
    }
  }
  if (execCopy(text)) return 'copied'
  selectContents(select)
  return 'manual'
}
