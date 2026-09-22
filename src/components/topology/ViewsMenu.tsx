import { Bookmark, BookmarkPlus, Check, Trash2 } from 'lucide-react'
import { useState } from 'react'
import { ConfirmModal } from '@/components/forms'
import { Button, Input, MenuPanel } from '@/components/ui/primitives'
import { activeView, describeView, viewParams } from '@/lib/views'
import { useTopology } from '@/store/topology'

/**
 * Saved views: named sets of Topology options (view, grouping, what is shown). One click brings one back;
 * they are kept in the workspace, so a team shares them. A view holds options only, never a selection.
 *
 * Controlled (`open`/`onOpenChange`) like its sibling popovers in the Topology toolbar: a page-level switch
 * keeps only one of them open at a time, so opening this one always closes any other that was already open.
 */
export default function ViewsMenu({
  open,
  onOpenChange,
  sp,
  onApply,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  sp: URLSearchParams
  onApply: (params: string) => void
}) {
  const { savedViews, saveView, deleteView } = useTopology()
  const [name, setName] = useState('')
  // A saved view is gone for good the moment its trash icon is clicked - a stray click aimed at the row just
  // above or below it (they sit close together in a scrollable list) must not delete one silently.
  const [toDelete, setToDelete] = useState<string | null>(null)
  const current = activeView(savedViews, sp)
  const now = viewParams(sp)

  const close = () => {
    onOpenChange(false)
    setName('')
  }

  return (
    <div className="relative">
      <Button onClick={() => onOpenChange(!open)} aria-haspopup="dialog" aria-expanded={open} aria-label="Saved views" data-testid="views-button">
        <Bookmark size={15} className={current ? 'fill-accent text-accent' : ''} />
        <span className="max-w-32 truncate">{current ? current.name : 'Views'}</span>
      </Button>
      <MenuPanel open={open} onClose={close} className="w-80 overflow-hidden" role="dialog" aria-label="Saved views">
        <div className="max-h-64 overflow-y-auto p-1">
          {savedViews.length === 0 && <p className="px-3 py-3 text-sm text-nb-500">No saved views yet. Set the view up the way you want it, then save it below.</p>}
          {savedViews.map((v) => (
            <div key={v.id} className="group flex items-center gap-1 rounded-md hover:bg-nb-940">
              <button
                className="flex min-w-0 flex-1 items-start gap-2 px-3 py-2 text-left"
                onClick={() => {
                  onApply(v.params)
                  close()
                }}
                data-testid="saved-view"
              >
                <span className="mt-0.5 w-3.5 shrink-0">{current?.id === v.id && <Check size={14} className="text-accent" aria-label="current view" />}</span>
                <span className="min-w-0">
                  <span className="block truncate text-sm text-nb-300">{v.name}</span>
                  <span className="block truncate text-xs text-nb-500">{describeView(v.params)}</span>
                </span>
              </button>
              <button
                className="mr-1 rounded p-1.5 text-nb-600 opacity-0 hover:bg-nb-850 hover:text-red-300 focus-visible:opacity-100 group-hover:opacity-100"
                aria-label={`Delete view ${v.name}`}
                onClick={() => setToDelete(v.id)}
              >
                <Trash2 size={13} />
              </button>
            </div>
          ))}
        </div>
        <form
          className="border-t border-nb-850 p-3"
          onSubmit={(e) => {
            e.preventDefault()
            if (!name.trim()) return
            saveView(name, now)
            close()
          }}
        >
          <div className="mb-2 text-xs text-nb-500">Save what you see now: <span className="text-nb-400">{describeView(now)}</span></div>
          <div className="flex gap-2">
            <Input value={name} onChange={(e) => setName(e.target.value)} placeholder="Name this view" aria-label="View name" maxLength={60} />
            <Button variant="primary" type="submit" disabled={!name.trim()} aria-label="Save view">
              <BookmarkPlus size={15} /> Save
            </Button>
          </div>
        </form>
      </MenuPanel>
      {toDelete && (
        <ConfirmModal
          title="Delete this saved view?"
          message={`"${savedViews.find((v) => v.id === toDelete)?.name ?? ''}" will be gone for everyone in the workspace. This can't be undone.`}
          onConfirm={() => deleteView(toDelete)}
          onClose={() => setToDelete(null)}
        />
      )}
    </div>
  )
}
