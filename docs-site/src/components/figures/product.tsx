/*
 * The figures that explain the product to someone who has not used it yet: the problem, the parts, the first hour,
 * how trust is built, how advice is made, and where telemetry goes. Every claim in them is on a page of this site.
 */
import {Arrow, Box, Fig, Lines, Pill, Step, T, accent, cloud, edgeTier, far, rule} from './kit';

/** The problem: an estate that spreads from a cloud region to far-edge sites. */
export function ContinuumOverview() {
  const f = 'continuum';
  const cols: {x: number; tier: string; color: string; name: string; lines: string[]}[] = [
    {x: 24, tier: 'Cloud', color: cloud, name: 'Managed Kubernetes', lines: ['Plenty of capacity', 'Fast, steady links', 'Many teams, many namespaces']},
    {x: 268, tier: 'Edge', color: edgeTier, name: 'Regional racks and sites', lines: ['Limited capacity', 'Good links to the cloud', 'Close to people and data']},
    {x: 512, tier: 'Far edge', color: far, name: 'Boards, plants, shops, vehicles', lines: ['Scarce capacity', 'Links that can be slow or drop', 'Beside sensors, cameras, PLCs']},
  ];
  const questions = ['Where is everything?', 'Is it healthy?', 'What talks to what?', 'Where should it run?'];
  return (
    <Fig id={f} height={392} label="An estate spreads from the cloud to the edge to far-edge sites, and four questions get harder as it does" title="An estate is wider than any one cluster" subtitle="From a cloud region to a far-edge site: more places, weaker links, less certainty.">
      {cols.map((c) => (
        <g key={c.tier}>
          <Box x={c.x} y={68} w={224} h={150} color={c.color} />
          <Pill x={c.x + 16} y={82} w={c.tier === 'Far edge' ? 72 : 62} color={c.color}>{c.tier}</Pill>
          <T x={c.x + 16} y={128} bold>{c.name}</T>
          <Lines x={c.x + 16} y={150} lines={c.lines} />
        </g>
      ))}
      <Arrow fig={f} both d="M248 143H268" />
      <Arrow fig={f} both dashed d="M492 143H512" />
      <T x={258} y={62} small muted anchor="middle">steady</T>
      <T x={502} y={62} small muted anchor="middle">fragile</T>
      <T x={24} y={252} bold>Four questions get harder as it spreads</T>
      {questions.map((q, i) => (
        <g key={q}>
          <Box x={24 + i * 182} y={266} w={170} h={44} />
          <T x={24 + i * 182 + 85} y={293} anchor="middle" bold>{q}</T>
        </g>
      ))}
      <Box x={24} y={330} w={712} h={44} hi />
      <T x={380} y={357} anchor="middle" bold>Ikhnos answers all four, and never changes anything.</T>
    </Fig>
  );
}

/** The parts and the three ways they talk. */
export function SystemAtAGlance() {
  const f = 'glance';
  const clusters: [number, string, string][] = [
    [92, 'Cloud', cloud],
    [214, 'Edge', edgeTier],
    [336, 'Far edge', far],
  ];
  const fusion = ['Grafana', 'Prometheus', 'Loki', 'Tempo', 'Central operator'];
  return (
    <Fig id={f} height={524} label="Agents in clusters dial out to one Ikhnos server; a browser reads the server; optional FUSION stores telemetry; an optional decider answers placement questions" title="The parts of Ikhnos, and who starts each connection" subtitle="Solid arrows start where they begin. Dashed parts are optional.">
      <Box x={24} y={64} w={200} h={420} frame />
      <T x={40} y={84} bold>Your clusters</T>
      {clusters.map(([y, name, color]) => (
        <g key={name}>
          <Box x={40} y={y} w={168} h={110} color={color} />
          <Pill x={52} y={y + 12} w={name === 'Far edge' ? 72 : 62} color={color}>{name}</Pill>
          <Box x={52} y={y + 42} w={144} h={30} />
          <T x={124} y={y + 61} anchor="middle" bold mono>ikhnos-agent</T>
          <T x={52} y={y + 94} small muted>read-only, one per cluster</T>
        </g>
      ))}

      <Box x={288} y={64} w={248} h={336} hi />
      <T x={304} y={90} bold>Ikhnos server</T>
      <T x={304} y={107} small muted>one replica, one SQLite writer</T>
      <Box x={304} y={122} w={216} h={44} />
      <T x={316} y={142} bold mono color={accent}>:8443</T><T x={366} y={142} bold>agent port</T>
      <T x={316} y={158} small muted>gRPC, mutual TLS</T>
      <Box x={304} y={174} w={216} h={44} />
      <T x={316} y={194} bold mono color={accent}>:8080</T><T x={366} y={194} bold>admin port</T>
      <T x={316} y={210} small muted>web UI and JSON API</T>
      <T x={304} y={246} small bold>Inside</T>
      {['Agent hub and per-cluster views', 'Digital twin: declared plus observed', 'History and audit trail', 'Placement advice', 'Accounts, roles, two-factor'].map((l, i) => (
        <T key={l} x={304} y={268 + i * 24} small muted>{l}</T>
      ))}
      <Box x={288} y={416} w={248} h={68} frame dashed />
      <T x={304} y={438} bold>Regional operator</T>
      <T x={304} y={456} small muted>optional fan-in for telemetry</T>
      <T x={304} y={472} small muted>sits in a cluster you choose</T>

      <Box x={576} y={64} w={160} h={60} />
      <T x={592} y={90} bold>You</T>
      <T x={592} y={108} small muted>browser, HTTPS</T>
      <Box x={576} y={140} w={160} h={70} frame dashed />
      <T x={592} y={164} bold>Your decider</T>
      <T x={592} y={182} small muted>optional webhook</T>
      <T x={592} y={198} small muted>your placement policy</T>
      <Box x={576} y={232} w={160} h={252} frame dashed />
      <T x={592} y={254} bold>FUSION</T>
      <T x={592} y={270} small muted>optional, same release</T>
      {fusion.map((l, i) => (
        <g key={l}>
          <Box x={592} y={286 + i * 38} w={128} h={28} />
          <T x={656} y={305 + i * 38} small anchor="middle">{l}</T>
        </g>
      ))}

      <Arrow fig={f} accented d="M208 147H288" />
      <Arrow fig={f} accented d="M208 269H288" />
      <Arrow fig={f} accented d="M208 391H288" />
      <T x={248} y={137} small anchor="middle" color={accent}>dial out</T>
      <Arrow fig={f} both d="M576 94H536" />
      <Arrow fig={f} dashed d="M536 175H576" />
      <Arrow fig={f} d="M536 338H576" />
      <T x={556} y={330} small muted anchor="middle">reads</T>
      <Arrow fig={f} dashed d="M224 450H288" />
      <Arrow fig={f} dashed d="M536 451H592" />
      <T x={412} y={508} small muted anchor="middle">telemetry over OTLP, only if you turn it on</T>
    </Fig>
  );
}

/** The first hour, step by step, from nothing to advice. */
export function OnboardingJourney() {
  const f = 'journey';
  const steps: [string, string, string][] = [
    ['Prepare', 'A Kubernetes cluster, kubectl and Helm', 'kubectl get nodes lists a node'],
    ['Install the server', 'Two Helm commands', 'The ikhnos-server pod, Running and 1/1'],
    ['Sign in', 'Read the first password from the log', 'The sign-in page, then a new password'],
    ['Connect a cluster', 'Connect a cluster, then run the printed helm install', 'An agent waiting for approval'],
    ['Approve it', 'Read the code from the agent’s own pod log', 'Nodes, namespaces and services appear'],
    ['Explore', 'Open Topology, Clusters and Agents', 'Two views of one model, health, history'],
    ['Ask for advice', 'Open Placement', 'Recommendations with evidence, or an honest can’t tell'],
  ];
  return (
    <Fig id={f} height={64 + steps.length * 72 + 8} label="Seven steps from an empty cluster to placement advice" title="Your first hour, in seven steps" subtitle="Each step says what you do and what you should see before you go on.">
      <T x={84} y={68} small bold>Step</T>
      <T x={290} y={68} small bold>You do</T>
      <T x={520} y={68} small bold>You see</T>
      {steps.map(([name, doBit, seeBit], i) => {
        const y = 78 + i * 72;
        return (
          <g key={name}>
            <Box x={64} y={y} w={672} h={58} hi={i === 3 || i === 4} />
            <Step x={42} y={y + 29} n={i + 1} />
            <T x={84} y={y + 34} bold>{name}</T>
            <foreignObject x={290} y={y} width={216} height={58}>
              <div style={{fontSize: 11.5, lineHeight: '15px', color: 'var(--cds-text-primary)', display: 'flex', alignItems: 'center', height: 58}}>{doBit}</div>
            </foreignObject>
            <foreignObject x={520} y={y} width={208} height={58}>
              <div style={{fontSize: 11.5, lineHeight: '15px', color: 'var(--cds-text-secondary)', display: 'flex', alignItems: 'center', height: 58}}>{seeBit}</div>
            </foreignObject>
            {i < steps.length - 1 && <Arrow fig={f} d={`M42 ${y + 39}V${y + 91}`} noHead />}
          </g>
        );
      })}
    </Fig>
  );
}

/** How a cluster goes from nothing to a certificate: two secrets that never travel together. */
export function EnrollmentSequence() {
  const f = 'enroll';
  const lives: [number, number, string][] = [[84, 100, 'You'], [264, 120, 'Ikhnos server'], [450, 150, 'Agent, in the cluster'], [662, 120, 'Agent pod log']];
  const calls: [number, number, number, boolean, string, string][] = [
    [84, 264, 130, false, '1  Connect a cluster', ''],
    [264, 84, 170, true, '2  Command with a one-time token and the CA pin', ''],
    [84, 450, 210, false, '3  Run the helm install in the cluster', ''],
    [450, 264, 250, false, '4  Dial :8443, check the pin, send token and key request', ''],
    [450, 662, 290, false, '5  Print the approval code, here only', ''],
    [662, 84, 330, true, '6  You read the code from the log', ''],
    [84, 264, 370, false, '7  Type the code into the Approve dialog', ''],
    [264, 450, 410, true, '8  A certificate for 24 hours, renewed at half-life', ''],
  ];
  return (
    <Fig id={f} height={488} label="Enrollment in eight steps: a token in the command, an approval code only in the agent's log, then a certificate" title="Enrollment: two secrets that never travel together" subtitle="The token is in the command you run. The approval code is only in the pod log. Dashed arrows are returns.">
      <g stroke={rule} strokeWidth="1.25" strokeDasharray="3 4">
        {lives.map(([x]) => <path key={x} d={`M${x} 94V470`} />)}
      </g>
      {lives.map(([x, w, name]) => (
        <g key={name}>
          <Box x={x - w / 2} y={64} w={w} h={30} />
          <T x={x} y={84} anchor="middle" bold>{name}</T>
        </g>
      ))}
      {calls.map(([x1, x2, y, dashed, label]) => {
        const mid = (x1 + x2) / 2;
        return (
          <g key={label}>
            <Arrow fig={f} dashed={dashed} d={`M${x1} ${y}H${x2}`} />
            <T x={mid} y={y - 8} small anchor="middle">{label}</T>
          </g>
        );
      })}
      <Box x={414} y={436} w={322} h={32} hi />
      <T x={575} y={456} small bold anchor="middle">After step 8, every connection is mutual TLS</T>
    </Fig>
  );
}

/** What an agent may report: a chain that only narrows, and what each tier can see. */
export function TrustChain() {
  const f = 'chain';
  const rows: [string, string, number, string, string][] = [
    ['Ceiling', 'what the agent’s RBAC lets it read', 444, 'Set by the cluster’s owner', 'helm install, access.tier'],
    ['Approved', 'what an administrator agreed to receive', 336, 'Set by an administrator', 'the UI; never above the ceiling'],
    ['Effective', 'what the agent is sending right now', 228, 'Narrowed by the agent', 'collectors paused, namespaces left out'],
  ];
  const tiers: [string, string, string[]][] = [
    ['Tier 0', 'registered only', ['The UID of the kube-system', 'namespace, to tell clusters apart.', 'Nothing else.']],
    ['Tier 1', 'infrastructure', ['Tier 0, plus nodes, storage', 'classes and ingress classes.', 'No namespaces or workloads.']],
    ['Tier 2', 'services (the default)', ['Tier 1, plus namespaces,', 'workloads, pods, services', 'and ingresses.']],
  ];
  return (
    <Fig id={f} height={424} label="Ceiling, approved and effective: each at or below the one before, and what tiers zero, one and two can see" title="What an agent reports is a chain that only narrows" subtitle="Checked in this order, by the agent as well as by the server. The server can narrow it, never widen it.">
      {rows.map(([name, what, w, who, how], i) => {
        const y = 66 + i * 64;
        return (
          <g key={name}>
            <Box x={24} y={y} w={w} h={52} hi={i === 0} />
            <T x={40} y={y + 22} bold>{i + 1}  {name}</T>
            <T x={40} y={y + 40} small muted>{what}</T>
            <T x={24 + w + 18} y={y + 22} small bold>{who}</T>
            <T x={24 + w + 18} y={y + 40} small muted>{how}</T>
          </g>
        );
      })}
      <T x={24} y={284} bold>What each access tier can see</T>
      {tiers.map(([name, sub, lines], i) => (
        <g key={name}>
          <Box x={24 + i * 242} y={298} w={228} h={100} />
          <T x={40 + i * 242} y={320} bold>{name}</T>
          <T x={92 + i * 242} y={320} small muted>{sub}</T>
          <Lines x={40 + i * 242} y={342} lines={lines} />
        </g>
      ))}
      <T x={24} y={414} small muted>Read-only at every tier. Secrets and ConfigMaps are never read. Tiers 3 and 4 are reserved and refused.</T>
    </Fig>
  );
}

/** How advice is made, from facts to a recommendation a person can argue with. */
export function PlacementPipeline() {
  const f = 'place';
  return (
    <Fig id={f} height={548} label="Facts go through hard constraints, a three-valued fit, a cost in points, a confidence taken from the weakest fact, and evidence; a person decides" title="How a recommendation is made" subtitle="Read-only. Every step keeps the source, the confidence and the age of what it used.">
      <Box x={24} y={64} w={712} h={50} band />
      <T x={40} y={86} bold>The estate</T>
      <T x={40} y={103} small muted>services and what they need, clusters and what is free, links and their round trips, each fact with its source, class and age</T>

      <Box x={24} y={140} w={220} h={112} />
      <Step x={24} y={140} n={1} />
      <T x={44} y={164} bold>Hard constraints</T>
      <Lines x={44} y={184} lines={['Local volumes, node pinning,', 'data residency, trust zones,', 'tier policy, room for CPU and memory.', 'Fail one and it is not a candidate.']} />

      <Box x={270} y={140} w={220} h={112} />
      <Step x={270} y={140} n={2} />
      <T x={290} y={164} bold>Three-valued fit</T>
      <Lines x={290} y={184} lines={['fits, does not fit, can’t tell.', 'Free capacity is an interval.', 'An unknown is not a zero.', 'A guess is not a fact.']} />

      <Box x={516} y={140} w={220} h={112} />
      <Step x={516} y={140} n={3} />
      <T x={536} y={164} bold>Cost, in points</T>
      <Lines x={536} y={184} lines={['Round trips, weighted by traffic;', 'traffic that newly crosses sites;', 'a penalty near full; data to copy.', '1 point is 1 ms of round trip.']} />

      <Arrow fig={f} d="M244 196H270" />
      <Arrow fig={f} d="M490 196H516" />

      <Box x={24} y={284} w={220} h={112} />
      <Step x={24} y={284} n={4} />
      <T x={44} y={308} bold>Confidence</T>
      <Lines x={44} y={328} lines={['measured, reported, inferred,', 'guess, unknown. The weakest', 'deciding fact sets the level,', 'never an average.']} />

      <Box x={270} y={284} w={220} h={112} />
      <Step x={270} y={284} n={5} />
      <T x={290} y={308} bold>Evidence</T>
      <Lines x={290} y={328} lines={['Each input with its source and', 'age, and the smallest change', 'that would flip the answer,', 'checked by the tests.']} />

      <Box x={516} y={284} w={220} h={112} hi />
      <Step x={516} y={284} n={6} />
      <T x={536} y={308} bold>Recommendation</T>
      <Lines x={536} y={328} lines={['Where, what it would save,', 'how sure, and why. Or: not', 'enough evidence to recommend,', 'with the missing fact named.']} muted={false} />

      <Arrow fig={f} d="M134 252V284" />
      <Arrow fig={f} d="M380 252V284" />
      <Arrow fig={f} d="M626 252V284" />
      <Arrow fig={f} d="M244 340H270" />
      <Arrow fig={f} d="M490 340H516" />

      <Box x={24} y={420} w={712} h={50} frame dashed />
      <T x={40} y={442} bold>Deciders, side by side</T>
      <T x={40} y={460} small muted>Baseline, Follow the heaviest talker, and your own webhook all see the same estate and are scored on the same answer.</T>
      <Arrow fig={f} dashed d="M560 420V396" />

      <Box x={24} y={490} w={712} h={40} hi />
      <T x={380} y={515} anchor="middle" bold>A person decides. Nothing is moved by Ikhnos.</T>
    </Fig>
  );
}

/** Where telemetry goes, and who may see it. */
export function TelemetryFlow() {
  const f = 'telem';
  return (
    <Fig id={f} height={504} label="Collectors in each cluster export over OTLP to an optional regional operator, then to the central operator of FUSION, which writes to Prometheus, Loki and Tempo; Grafana and a token-protected API read them back" title="Where telemetry goes" subtitle="The server grants permission and reads results back. The data itself never passes through it.">
      <Box x={24} y={64} w={206} h={188} frame />
      <T x={40} y={86} bold>Each cluster</T>
      <Box x={40} y={100} w={174} h={58} />
      <T x={52} y={120} small bold>Node collectors</T>
      <T x={52} y={136} small muted>one per node, DaemonSets:</T>
      <T x={52} y={150} small muted>host, energy, GPU</T>
      <Box x={40} y={174} w={174} h={58} />
      <T x={52} y={194} small bold>Cluster collector</T>
      <T x={52} y={210} small muted>one Deployment: objects,</T>
      <T x={52} y={224} small muted>app signals, scraped metrics</T>
      <Arrow fig={f} d="M127 158V174" />

      <Box x={24} y={274} w={206} h={78} frame dashed />
      <T x={40} y={296} bold>Another cluster</T>
      <T x={40} y={314} small muted>its own release and its own</T>
      <T x={40} y={330} small muted>settings, the same shape</T>

      <Box x={282} y={110} w={184} h={92} hi />
      <T x={298} y={134} bold>Regional operator</T>
      <Lines x={298} y={154} lines={['Receives what clusters', 'already export, over OTLP', 'with a client certificate.']} />
      <Arrow fig={f} accented d="M230 156H282" />
      <Arrow fig={f} accented d="M230 290H252V188H282" />

      <Box x={282} y={246} w={184} h={106} frame dashed />
      <T x={298} y={268} bold>Or a backend you run</T>
      <Lines x={298} y={288} lines={['Any OTLP endpoint. A cluster', 'can export straight to it,', 'or through an operator.']} />
      <Arrow fig={f} dashed d="M230 336H282" />

      <Box x={518} y={64} w={218} h={272} frame />
      <T x={534} y={86} bold>FUSION</T>
      <T x={534} y={102} small muted>part of the server’s release</T>
      <Box x={534} y={116} w={186} h={34} />
      <T x={627} y={137} small bold anchor="middle">Central operator</T>
      {['Prometheus  (metrics)', 'Loki  (logs)', 'Tempo  (traces)'].map((l, i) => (
        <g key={l}>
          <Box x={534} y={172 + i * 36} w={186} h={28} />
          <T x={627} y={190 + i * 36} small anchor="middle">{l}</T>
        </g>
      ))}
      <Arrow fig={f} d="M627 150V172" />
      <Arrow fig={f} accented d="M466 156H492V133H534" />
      <Box x={534} y={282} w={186} h={40} />
      <T x={627} y={299} small bold anchor="middle">Grafana</T>
      <T x={627} y={314} small muted anchor="middle">nine dashboards, read-only data</T>

      <Box x={282} y={384} w={454} h={96} hi />
      <T x={298} y={408} bold>Ikhnos server</T>
      <Lines x={298} y={428} lines={['Decides what may leave a cluster, and where it may go.', 'Opens Grafana and Prometheus for you, behind your sign-in.', 'Reads the stores through one token-protected API.']} muted={false} />
      <Arrow fig={f} dashed d="M590 384V336" />
      <T x={24} y={420} small bold>Four commitments</T>
      <Lines x={24} y={440} lines={['Named consent, not raw configuration.', 'Safe by default: memory limit, redaction.', 'Declarative only: a command you run.', 'Additive permissions, never speculative.']} />
    </Fig>
  );
}
