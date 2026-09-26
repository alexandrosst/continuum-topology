import { Modal } from '@/components/ui/primitives'

const IS_MAC = typeof navigator !== 'undefined' && /mac|iphone|ipad/i.test(navigator.platform || navigator.userAgent)
const MOD = IS_MAC ? '⌘' : 'Ctrl'

/** One row: keys shown as individual <kbd>s, and what they do. */
function Row({ keys, children }: { keys: string[]; children: string }) {
  return (
    <div className="flex items-center justify-between gap-4 py-1.5 text-sm">
      <span className="text-nb-300">{children}</span>
      <span className="flex shrink-0 items-center gap-1">
        {keys.map((k, i) => (
          <kbd key={i} className="rounded border border-nb-800 bg-nb-930 px-1.5 py-0.5 text-[11px] text-nb-300">{k}</kbd>
        ))}
      </span>
    </div>
  )
}

/**
 * Every keyboard shortcut this app actually has, in one place - deliberately only what is real: this is a
 * small app with a search palette and a handful of dropdowns/dialogs, not a canvas editor with dozens of
 * bindings, so the list below is short on purpose rather than padded out to look complete.
 */
export default function KeyboardShortcutsModal({ onClose }: { onClose: () => void }) {
  return (
    <Modal open onClose={onClose} title="Keyboard shortcuts" width="max-w-sm">
      <div className="divide-y divide-nb-850">
        <Row keys={[MOD, 'K']}>Open search</Row>
        <Row keys={['?']}>Show this panel</Row>
        <Row keys={['Esc']}>Close a dialog, menu or search</Row>
        <Row keys={['↑', '↓']}>Move through a menu or the search results</Row>
        <Row keys={['Enter']}>Choose the highlighted item</Row>
      </div>
    </Modal>
  )
}
