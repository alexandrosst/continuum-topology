import { useEffect, useState } from 'react'
import { Button, ErrorBanner, Modal } from '@/components/ui/primitives'
import { api, ApiError, type IssuedCertificate } from '@/lib/api'
import type { RegionalOperator } from '@/lib/types'
import { useServer } from '@/store/server'

const STATE_WORD = { ok: 'Valid', expiring: 'Ends soon', expired: 'Ended' } as const

/** The holder in words a person recognises: the cluster's name when it is known, otherwise what the server recorded. */
export function holderOf(c: IssuedCertificate, clusterName: (id: string) => string): string {
  return c.kind === 'receiver' ? 'The operator (its receiver)' : clusterName(c.sender ?? '') || c.sender || c.subject
}

/**
 * Which certificates exist for an operator, who holds each and until when. Each source cluster has its own, so this answers
 * "which of my clusters can still send here" without opening a cluster. It is a record: a certificate cannot be withdrawn from
 * here (the receiver trusts the operator's CA), so one that is no longer wanted is dealt with by removing the Secret in that
 * cluster, or by revoking the operator.
 */
export default function OperatorCertificatesModal({ operator, clusterName, onClose }: { operator: RegionalOperator; clusterName: (id: string) => string; onClose: () => void }) {
  const conn = useServer((s) => s.conn)
  const [certs, setCerts] = useState<IssuedCertificate[] | null>(null)
  const [error, setError] = useState('')
  useEffect(() => {
    const c = conn()
    if (!c) return
    let live = true
    api
      .listOperatorCertificates(c, operator.id)
      .then((r) => live && setCerts(r.certificates))
      .catch((e) => live && setError(e instanceof ApiError ? e.message : 'Could not read the certificates.'))
    return () => {
      live = false
    }
  }, [conn, operator.id])

  return (
    <Modal open onClose={onClose} title={`Certificates of ${operator.name}`} width="max-w-2xl" footer={<Button variant="primary" onClick={onClose} data-testid="operator-certs-done">Done</Button>}>
      {error && <ErrorBanner>{error}</ErrorBanner>}
      {!error && certs === null && <p className="text-sm text-nb-400">Reading…</p>}
      {certs !== null && certs.length === 0 && (
        <p className="text-sm text-nb-400" data-testid="operator-certs-empty">Nothing recorded yet. Certificates issued before this list existed are not in it; renewing the certificates starts it.</p>
      )}
      {certs !== null && certs.length > 0 && (
        <table className="w-full text-left text-sm" data-testid="operator-certs-table">
          <thead className="text-xs text-nb-400">
            <tr><th className="py-1 pr-3 font-normal">Held by</th><th className="py-1 pr-3 font-normal">Issued</th><th className="py-1 pr-3 font-normal">Ends</th><th className="py-1 font-normal">State</th></tr>
          </thead>
          <tbody>
            {certs.map((c) => (
              <tr key={c.serial} className="border-t border-nb-800" data-testid={`operator-cert-${c.serial}`}>
                <td className="py-1.5 pr-3">{holderOf(c, clusterName)}</td>
                <td className="py-1.5 pr-3 text-nb-400">{c.issuedAt.slice(0, 10)}<span className="text-nb-500"> by {c.issuedBy}</span></td>
                <td className="py-1.5 pr-3 text-nb-300">{c.notAfter.slice(0, 10)}</td>
                <td className={c.state === 'ok' ? 'py-1.5 text-nb-300' : c.state === 'expiring' ? 'py-1.5 text-warn' : 'py-1.5 text-bad'}>{STATE_WORD[c.state]}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </Modal>
  )
}
