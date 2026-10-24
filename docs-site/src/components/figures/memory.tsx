/*
 * Figures for how history is kept, how an exposure is chosen and how a release is made.
 */
import {Arrow, Box, Fig, Lines, Pill, T, accent, bad, edge, good} from './kit';

/** One entity over time: versions that are closed, never edited, and events that explain them. */
export function EntityVersions() {
  const f = 'ver';
  const v: [number, string, string, string, boolean][] = [
    [24, '09:00', '09:14', 'first seen', false],
    [274, '09:14', '10:02', 'renamed', false],
    [524, '10:02', 'null', 'current', true],
  ];
  return (
    <Fig id={f} height={392} label="One entity with three versions in a row, each with a validFrom and validTo, the last one open, and two events that explain the second version" title="Nothing is edited. A change closes a version and opens the next." subtitle="An entity is an identity. A version is one period during which it looked a certain way.">
      <Box x={24} y={64} w={712} h={46} />
      <T x={40} y={84} bold>Entity</T>
      <T x={92} y={84} mono small>service / checkout-api</T>
      <T x={40} y={101} small muted>One identity, (org, kind, id), for as long as anything has ever been true about it.</T>

      {v.map(([x, from, to, note, open], i) => (
        <g key={from}>
          <Box x={x} y={150} w={212} h={96} hi={open} />
          <T x={x + 14} y={174} bold>Version</T>
          {open && <Pill x={x + 100} y={160} w={96} color={good}>the open one</Pill>}
          <T x={x + 14} y={196} small mono>validFrom  {from}</T>
          <T x={x + 14} y={212} small mono>validTo    {to}</T>
          <T x={x + 14} y={232} small muted>hash, name, status, document</T>
          <Arrow fig={f} d={`M${x + 106} 110V150`} />
          {i < 2 && <Arrow fig={f} d={`M${x + 212} 198H${x + 250}`} />}
        </g>
      ))}
      <T x={118} y={140} small muted anchor="middle">HAS_VERSION</T>

      <Box x={274} y={296} w={212} h={44} frame />
      <T x={290} y={316} small bold mono>application-renamed</T>
      <T x={290} y={332} small muted>checkout → checkout-api</T>
      <Box x={24} y={296} w={236} h={44} frame />
      <T x={40} y={316} small bold mono>application-membership</T>
      <T x={40} y={332} small muted>two services added</T>
      <Arrow fig={f} accented d="M142 296V262H350V246" />
      <Arrow fig={f} accented d="M380 296V246" />
      <T x={500} y={314} small color={accent}>EXPLAINS: why this version looks as it does</T>
      <T x={500} y={332} small muted>One save can explain itself more than once.</T>

      <T x={24} y={372} small muted>“What did this look like an hour ago” is a filter on a timestamp: validFrom at or before it, and validTo empty or after it.</T>
    </Fig>
  );
}

/** A walk over dependents that is bounded three ways. */
export function BoundedWalk() {
  const f = 'walk';
  return (
    <Fig id={f} height={388} label="A breadth-first walk from one service: hop one reaches callers, a cap of four thousand results stops the walk, hop two is never queried, and the whole call has a time limit" title="What depends on what, with three bounds" subtitle="Level by level, over only what the last level reached. The estate cannot make one request run away.">
      <g transform="translate(0 14)">
      <Box x={24} y={64} w={152} h={60} hi />
      <T x={100} y={90} anchor="middle" bold>service · hub</T>
      <T x={100} y={108} anchor="middle" small muted>hop 0: where it starts</T>
      {[0, 1, 2, 3, 4, 5].map((i) => (
        <g key={i}>
          <Box x={236} y={64 + i * 28} w={152} h={22} />
          <T x={312} y={79 + i * 28} anchor="middle" small>caller-{i}</T>
          <Arrow fig={f} d={`M176 94L236 ${75 + i * 28}`} />
        </g>
      ))}
      <Box x={236} y={232} w={152} h={22} dashed />
      <T x={312} y={247} anchor="middle" small muted>and 3,993 more</T>
      <T x={312} y={56} anchor="middle" small bold>hop 1</T>

      <path d="M424 56V262" stroke={bad} strokeWidth="2" strokeDasharray="6 4" />
      <T x={436} y={74} small bold color={bad}>4,000 results: stop</T>
      <Lines x={436} y={92} lines={['The walk stops opening', 'levels once the cap fires,', 'even if hops would allow', 'more.']} />
      <Box x={600} y={64} w={136} h={74} dashed />
      <T x={668} y={90} anchor="middle" small muted>hop 2, 3, …</T>
      <T x={668} y={108} anchor="middle" small muted>never queried</T>
      </g>

      <T x={24} y={306} bold>Three bounds, each on its own</T>
      {([
        ['Hops', 'Clamped to 8, whatever is asked. Nonsense (empty, negative, text) falls back to the default.'],
        ['Results', 'The walk stops growing past 4,000 entities and returns what it has.'],
        ['Time', 'One deadline for the whole call, not for each round trip.'],
      ] as [string, string][]).map(([a, b], i) => (
        <g key={a}>
          <T x={24 + i * 242} y={328} small bold>{a}</T>
          <foreignObject x={24 + i * 242} y={334} width={226} height={50}>
            <div style={{fontSize: 10.5, lineHeight: '13px', color: 'var(--cds-text-secondary)'}}>{b}</div>
          </foreignObject>
        </g>
      ))}
    </Fig>
  );
}

/** Three ways to put the agent port on the network, and the one thing none may do. */
export function ExposureOptions() {
  const f = 'expose';
  const cols: [number, string, string, string, string[], boolean][] = [
    [24, 'LoadBalancer', 'agent.service.type', 'The default', ['Your cloud gives it an', 'address. Best on managed', 'clusters.'], true],
    [268, 'NodePort', 'agent.service.type', 'Works anywhere', ['A fixed port on every', 'node. Best for a trial', 'on k3s, kind, minikube.'], false],
    [512, 'TLSRoute', 'agent.tlsRoute', 'Advanced', ['Shares port 443 with', 'other apps, by name.', 'Listener must be Passthrough.'], false],
  ];
  return (
    <Fig id={f} height={436} label="Three ways to expose the agent port, LoadBalancer, NodePort and a Gateway TLSRoute, each leaving the TLS handshake untouched on its way to the server pod, and one thing that must never be in the path" title="Exposing the agent port" subtitle="The TLS handshake has to reach the pod exactly as the agent sent it. Never end TLS in front of this port.">
      {cols.map(([x, name, key, tag, lines, hi]) => (
        <g key={name}>
          <Box x={x} y={68} w={224} h={284} hi={hi} />
          <T x={x + 16} y={94} bold>{name}</T>
          <T x={x + 16} y={112} small muted mono>{key}</T>
          <Pill x={x + 16} y={122} w={tag.length * 7 + 20} color={hi ? accent : edge}>{tag}</Pill>
          <Box x={x + 16} y={160} w={192} h={26} />
          <T x={x + 112} y={177} small anchor="middle">agent</T>
          <Arrow fig={f} d={`M${x + 112} 186V204`} />
          <Box x={x + 16} y={204} w={192} h={26} band />
          <T x={x + 112} y={221} small anchor="middle" muted>{name === 'TLSRoute' ? 'Gateway, TLS passthrough' : name === 'NodePort' ? 'a port on every node' : 'cloud load balancer'}</T>
          <Arrow fig={f} d={`M${x + 112} 230V248`} />
          <Box x={x + 16} y={248} w={192} h={26} />
          <T x={x + 112} y={265} small anchor="middle" mono>server pod :8443</T>
        </g>
      ))}
      {cols.map(([x, , , , lines]) => (
        <Lines key={x} x={x + 16} y={298} lines={lines} />
      ))}
      <Box x={24} y={370} w={712} h={50} color={bad} />
      <T x={40} y={392} bold>Never in the path: anything that ends TLS</T>
      <T x={40} y={409} small muted>An HTTP ingress in its default mode, an HTTPS-to-HTTP load balancer, a mesh sidecar. The admin port is the opposite.</T>
    </Fig>
  );
}

/** What each workflow does, from a push to a signed image. */
export function ReleasePipeline() {
  const f = 'rel';
  return (
    <Fig id={f} height={388} label="A push runs CI; a push to main publishes an edge build; a version tag publishes a release; everything published is signed with cosign; a change to the docs publishes the site" title="From a push to a signed release" subtitle="Three workflows, each with one job.">
      <Box x={24} y={64} w={176} h={64} />
      <T x={40} y={90} bold>Every push</T>
      <T x={40} y={108} small muted>and pull request</T>
      <Arrow fig={f} d="M200 96H236" />
      <Box x={236} y={64} w={500} h={64} />
      <T x={252} y={88} bold mono>ci.yml</T>
      <T x={252} y={106} small muted>Build, vet and race-test the backend. Lint, type-check, build and test the frontend.</T>
      <T x={252} y={120} small muted>Scan dependencies on both. Nothing is published.</T>

      <Box x={24} y={152} w={176} h={64} />
      <T x={40} y={178} bold>Push to main</T>
      <T x={40} y={196} small muted>or a tag v*.*.*</T>
      <Arrow fig={f} d="M200 184H236" />
      <Box x={236} y={152} w={500} h={64} hi />
      <T x={252} y={176} bold mono>release.yml</T>
      <T x={252} y={194} small muted>Build the server and agent images and the four Helm charts, push them to GHCR,</T>
      <T x={252} y={208} small muted>sign them with cosign, and bake the GHCR namespace into the chart defaults.</T>

      <Box x={236} y={240} w={242} h={60} />
      <T x={252} y={262} small bold>main: the edge build</T>
      <T x={252} y={278} small muted mono>edge, chart 0.0.0-edge.g&lt;sha&gt;</T>
      <T x={252} y={292} small muted>never to be depended on</T>
      <Box x={494} y={240} w={242} h={60} hi />
      <T x={510} y={262} small bold>A tag: a release</T>
      <T x={510} y={278} small muted mono>that version, and latest</T>
      <T x={510} y={292} small muted>a pre-release leaves latest alone</T>
      <Arrow fig={f} d="M357 216V240" />
      <Arrow fig={f} d="M615 216V240" accented />

      <Box x={24} y={324} w={176} h={46} />
      <T x={40} y={344} bold>Docs change</T>
      <T x={40} y={360} small muted>under docs-site/</T>
      <Arrow fig={f} d="M200 347H236" />
      <Box x={236} y={324} w={500} h={46} />
      <T x={252} y={344} bold mono>docs.yml</T>
      <T x={252} y={360} small muted>Build this site and publish it to GitHub Pages.</T>
    </Fig>
  );
}
