import { Table, Td, Th } from '@/components/ui/primitives'
import { advisorSummary, type AdvisorRecord } from '@/lib/proposals'

/**
 * How each advisor has done so far: what it proposed, what people did with it, and whether what it predicted came true. Two things
 * are kept apart on purpose. A rejection says a person disagreed, not that the advisor was wrong. And "held" is about the agent's own
 * load, which also moves for reasons unrelated to the change, so it is a sign, not proof, that the advisor was right.
 */
export default function AdvisorRecords({ records }: { records: AdvisorRecord[] }) {
  if (records.length === 0) return null
  return (
    <section aria-label="Advisors" className="mt-8" data-testid="advisor-records">
      <h2 className="mb-1 text-sm font-medium text-nb-300">How each advisor has done</h2>
      <p className="mb-2 max-w-3xl text-xs text-nb-500">
        A rejected proposal means a person disagreed, not that the advisor was wrong. A prediction “held” when the agent’s own load moved the way the advisor
        said, by at least half as much; the load can also move for reasons that have nothing to do with the change.
      </p>
      <Table>
        <thead>
          <tr><Th>Advisor</Th><Th>Proposed</Th><Th>What people decided</Th><Th>Predictions</Th></tr>
        </thead>
        <tbody>
          {records.map((r) => {
            const s = advisorSummary(r)
            return (
              <tr key={r.advisor} data-testid="advisor-row">
                <Td className="font-medium text-nb-300">{r.advisor}</Td>
                <Td className="tabular-nums text-nb-400">{s.proposed}</Td>
                <Td className="text-nb-400">{s.decisions}</Td>
                <Td>
                  <div className="tabular-nums text-nb-200">{s.predictions}</div>
                  {s.predictionNote && <div className="text-xs text-nb-500">{s.predictionNote}</div>}
                </Td>
              </tr>
            )
          })}
        </tbody>
      </Table>
    </section>
  )
}
