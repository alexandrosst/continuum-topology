/** The host:port a regional operator's own OTLP receiver answers on, in-cluster - the exact same string
 *  the backend's own operatorDestinationCommand builds (admin_operators.go) for pointing an agent's
 *  export at it. Always plain gRPC: the operator chart's receiver has no HTTP listener. */
export function operatorReceiverEndpoint(op: { id: string }): string {
  return `${op.id}.ikhnos-system.svc:4317`
}
