# Security policy

## Reporting a vulnerability

Please do **not** open a public issue for a security problem.

Report it privately through GitHub: on the repository page, **Security → Report a vulnerability**. Include what you found, how to reproduce it, which version or commit, and what you think an attacker could do with it. If the finding involves a running system, please test only against your own.

You will get an acknowledgement and an assessment from the maintainer. Ikhnos is a small, pre-1.0 project, so no response time is promised here; if you have heard nothing after a week, a short note on the same report is welcome.

## Supported versions

Ikhnos is pre-1.0. Only the latest release and the `main` branch receive fixes.

## What is in scope

The server, the agent, the node probe and the traffic observer, the Helm charts and the images published from this repository, and the documentation where it could lead someone to an insecure setup. Findings about the trust model are especially welcome: enrollment and approval codes, certificate issuance and revocation, the access-tier chain, sign-in and two-factor, tenant isolation, and the server's outbound calls.

Out of scope: third-party software the chart can bundle (Neo4j, Prometheus, Loki, Tempo, Grafana) unless the way Ikhnos configures it creates the problem; findings that need an already-compromised cluster admin; denial of service by a user the server already trusts.

## How the design limits the damage

These are properties of the design, not guarantees; each is covered by tests and described in the [documentation](https://alexandrosst.github.io/ikhnos/architecture/trust-model):

- The agent is read-only and never reads Secrets or ConfigMaps. The server holds no kubeconfig and never connects into a cluster.
- Agents authenticate with 24-hour client certificates over mutual TLS 1.3, issued after a one-time token **and** an approval code that only appears in the cluster's own pod log.
- What an agent reports is a chain (Helm ceiling, approved tier, effective tier) that the server can only narrow.
- Sensitive values in the server's database (second-factor secrets, the mail password, the decider secret) are sealed with a key you provide.
- Releases are signed with cosign, keylessly; see [Signing and verifying releases](deploy/README.md#signing-and-verifying-releases).

The findings of the last internal security review are in [`SECURITY_REVIEW.md`](SECURITY_REVIEW.md).
