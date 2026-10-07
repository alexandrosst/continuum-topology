/**
 * What the command below will change on the install, in a few lines, shown before it can be copied: the person who runs it should
 * not find out from `helm diff` that it also moved the destination. Nothing is shown for a fresh install (everything is new) or when
 * the list is empty and nothing is installed; for an install it says so when the command changes nothing it is known to have.
 */
export default function ChangeSummary({ changes, installed, kept = [], testId }: { changes: string[]; installed: boolean; /** What the form shows but the install does not report: the command leaves it as installed. */ kept?: string[]; testId: string }) {
  if (changes.length === 0 && !installed) return null
  return (
    <div className="rounded-md border border-nb-850 bg-nb-930 px-3 py-2 text-xs leading-relaxed text-nb-400" data-testid={testId}>
      {changes.length === 0 ? (
        <p>This command changes nothing the install is known to have.</p>
      ) : (
        <>
          <p className="font-medium text-nb-300">This command will change:</p>
          <ul className="mt-1 list-disc space-y-0.5 pl-4" data-testid={`${testId}-list`}>
            {changes.map((c) => <li key={c}>{c}</li>)}
          </ul>
        </>
      )}
      {kept.length > 0 && (
        <p className="mt-1.5" data-testid={`${testId}-kept`}>
          Left exactly as installed, because this install does not report {kept.length === 1 ? 'it' : 'them'} and the form cannot show what they are: {kept.join('; ')}. Change one of these here and the command states it in full.
        </p>
      )}
    </div>
  )
}
