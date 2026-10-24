/*
 * The figures for someone who works on Ikhnos, or wants to know how it is built: the layers of the server, how facts become
 * views, the agent and its stream, how a Config is known to be in force, the decision loop and the layers of the frontend.
 * They draw what the code is today. Where a figure names a package, a message or a test, it is the one in the repository.
 */
import type {ReactNode} from 'react';
import {Arrow, Box, Fig, Lines, Pill, Step, T, accent, bad, good, quiet, warn} from './kit';

/** A small package-name chip. */
function Chip({x, y, w, h = 24, children, hi, dashed}: {x: number; y: number; w: number; h?: number; children: ReactNode; hi?: boolean; dashed?: boolean}) {
  return (
    <g>
      <Box x={x} y={y} w={w} h={h} hi={hi} dashed={dashed} />
      <T x={x + w / 2} y={y + h / 2 + 4} small anchor="middle" mono>
        {children}
      </T>
    </g>
  );
}

/** The server cut into layers: each row may use the rows below it and never the rows above. */
export function ServerLayers() {
  const f = 'layers';
  const lib = ['model', 'facts', 'netid', 'probe', 'link', 'measure', 'pki', 'store', 'interpret', 'twin', 'history', 'graph', 'chart', 'install', 'geoip', 'approval', 'advice'];
  const row = (y: number, h: number, name: string, path: string, hi = false) => (
    <g>
      <Box x={24} y={y} w={712} h={h} hi={hi} />
      <T x={40} y={y + 24} bold>{name}</T>
      <T x={40} y={y + 41} small muted mono>{path}</T>
    </g>
  );
  return (
    <Fig id={f} height={704} label="The server's packages in six layers: the shell, the API packages, the domain modules, the vocabulary of who is calling, the kernel and the libraries" title="The server, in layers" subtitle="Each row may use the rows below it and never the rows above. The compiler holds that line, and so does a test.">
      {row(64, 58, 'The shell', 'internal/server', true)}
      <T x={190} y={88} small>Builds the Core, the hub and the services, and hands each API what it needs.</T>
      <T x={190} y={106} small muted>Holds what is cookies, origins, limiters and the reverse proxy. Nothing else lives here.</T>

      {row(134, 80, 'API packages', '*api, fusionhttp')}
      {['accountsapi', 'settingsapi', 'operatorsapi', 'fusionhttp', 'linksapi', 'fusionpages'].map((n, i) => (
        <Chip key={n} x={190 + i * 90} y={150} w={86}>{n}</Chip>
      ))}
      <T x={190} y={198} small muted>The routes of a module, each mounted through the kit’s router.</T>

      {row(226, 176, 'Domain modules', 'internal/…')}
      <Box x={190} y={242} w={262} h={150} frame />
      <T x={202} y={260} small bold>The hub cluster</T>
      <Chip x={202} y={270} w={238} hi>estate</Chip>
      <Chip x={202} y={302} w={115}>agents</Chip>
      <Chip x={325} y={302} w={115}>connectivity</Chip>
      <Chip x={202} y={334} w={238}>observed</Chip>
      <T x={202} y={376} small muted>The hub and the twin built from it.</T>
      <T x={202} y={390} small muted>observed is pure, with no state.</T>
      <Box x={464} y={242} w={134} h={150} frame />
      <T x={474} y={260} small bold>Telemetry</T>
      <Chip x={474} y={270} w={114}>fusion</Chip>
      <Chip x={474} y={302} w={114}>telemetry</Chip>
      <Chip x={474} y={334} w={114}>operators</Chip>
      <T x={474} y={376} small muted>fusion calls</T>
      <T x={474} y={390} small muted>operators.</T>
      <Box x={610} y={242} w={114} h={150} frame />
      <T x={620} y={260} small bold>Advice</T>
      <Chip x={620} y={270} w={94} hi>decisionloop</Chip>
      <Lines x={620} y={316} lines={['It owns the', 'ports it asks', 'through, and', 'imports none', 'of the rest.']} />

      {row(414, 66, 'Who is calling', 'accounts, httpkit')}
      <Chip x={190} y={430} w={86}>accounts</Chip>
      <Chip x={284} y={430} w={86}>passkey</Chip>
      <Chip x={378} y={430} w={86}>httpkit</Chip>
      <T x={190} y={470} small muted>The words for a caller and what it may do; the router that records what a route asks.</T>

      {row(492, 62, 'The kernel', 'internal/kernel')}
      <T x={190} y={514} small>The Core: store, certificate authority, organisation, audit trail, clock, rate limits.</T>
      <T x={190} y={532} small muted>Settings and secrets. It stands on no module, and every module may use it.</T>

      {row(566, 118, 'Libraries', 'internal/…')}
      {lib.map((n, i) => (
        <Chip key={n} x={190 + (i % 6) * 91} y={580 + Math.floor(i / 6) * 26} w={87} h={22}>{n}</Chip>
      ))}
      <T x={190} y={674} small muted>They know nothing of the server. Several are the agent’s as well.</T>
      <Arrow fig={f} d="M12 120V676" noHead />
    </Fig>
  );
}

/** How an agent's facts become the views, and where a person's edits come in. */
export function FactsToViews() {
  const f = 'views';
  return (
    <Fig id={f} height={520} label="Agent facts go through the hub and interpretation into an observed topology; a person's edits go into a workspace of declared intent; twin.Build combines the two into the effective model, which the API, advice, deciders and the UI read" title="From facts to what you see" subtitle="Observed and declared are kept apart all the way, and combined only when something asks.">
      <Box x={24} y={68} w={128} h={64} />
      <T x={88} y={94} anchor="middle" bold>Agents</T>
      <T x={88} y={112} anchor="middle" small muted>Sync, heartbeat,</T>
      <T x={88} y={126} anchor="middle" small muted>flows</T>
      <Box x={184} y={68} w={152} h={64} />
      <T x={260} y={94} anchor="middle" bold>The hub</T>
      <T x={260} y={112} anchor="middle" small muted>one view per agent,</T>
      <T x={260} y={126} anchor="middle" small muted>one lock</T>
      <Box x={368} y={68} w={128} h={64} />
      <T x={432} y={94} anchor="middle" bold mono>interpret</T>
      <T x={432} y={112} anchor="middle" small muted>raw facts become</T>
      <T x={432} y={126} anchor="middle" small muted>clusters, nodes, apps</T>
      <Box x={528} y={68} w={208} h={64} />
      <T x={544} y={94} bold>Observed topology</T>
      <T x={544} y={112} small muted>each record: value, state,</T>
      <T x={544} y={126} small muted>and when it was seen</T>
      <Arrow fig={f} d="M152 100H184" />
      <Arrow fig={f} d="M336 100H368" />
      <Arrow fig={f} d="M496 100H528" />

      <Box x={24} y={176} w={128} h={64} />
      <T x={88} y={202} anchor="middle" bold>People</T>
      <T x={88} y={220} anchor="middle" small muted>edits in the UI</T>
      <Box x={184} y={176} w={288} h={64} />
      <T x={200} y={202} bold>The workspace</T>
      <T x={200} y={220} small muted>Declared intent only: sites, devices, links,</T>
      <T x={200} y={234} small muted>applications, policies, refs to corrections.</T>
      <Box x={528} y={176} w={208} h={64} hi />
      <T x={544} y={202} bold mono color={accent}>twin.Build</T>
      <T x={544} y={220} small>combines the two,</T>
      <T x={544} y={234} small>on request</T>
      <Arrow fig={f} d="M152 208H184" />
      <Arrow fig={f} d="M472 208H528" accented />
      <Arrow fig={f} d="M632 132V176" accented />

      <Box x={24} y={284} w={712} h={72} hi />
      <T x={40} y={308} bold>The effective model</T>
      <T x={40} y={326} small>Every value says whether it was declared, observed or computed, how old it is, and how sure.</T>
      <T x={40} y={342} small>A person’s value wins over an observed one, and is flagged when what is observed has since moved.</T>
      <Arrow fig={f} d="M632 240V284" accented />

      {['The model API', 'Placement advice', 'Your decider', 'The web UI'].map((l, i) => (
        <g key={l}>
          <Box x={24 + i * 182} y={386} w={170} h={36} />
          <T x={24 + i * 182 + 85} y={409} anchor="middle" small bold>{l}</T>
          <Arrow fig={f} d={`M${24 + i * 182 + 85} 356V386`} />
        </g>
      ))}

      <Box x={24} y={444} w={712} h={62} band />
      <T x={40} y={464} small bold>Kept beside it, in SQLite</T>
      <T x={40} y={481} small muted>Tombstones for records that went away, stable identities and the model version. History: snapshots and events,</T>
      <T x={40} y={496} small muted>and a temporal graph in Neo4j if you run one.</T>
    </Fig>
  );
}

/** One stream, message by message. */
export function AgentStream() {
  const f = 'stream';
  const A = 140;
  const S = 620;
  const msgs: [number, 'a' | 's', string, string?][] = [
    [0, 'a', 'Hello: version, ceiling tier, diagnostics, protocol, capabilities'],
    [1, 's', 'Config: approved tier, overrides, link peers, digest, server time'],
    [2, 'a', 'Sync, full: the whole picture, in pieces of about 1 MiB if large'],
    [3, 's', 'Ack: the sequence number applied'],
    [4, 'a', 'Sync, changes: what changed and what was deleted'],
    [5, 'a', 'Heartbeat: modules, clock skew, self stats, applied Config digest'],
    [6, 'a', 'Observed connections, and timing rounds if they are on'],
    [7, 's', 'Config again, whenever it changes: narrower, never wider'],
    [8, 's', 'Revoked: the server no longer trusts this agent'],
  ];
  const y0 = 108;
  const gap = 40;
  return (
    <Fig id={f} height={y0 + msgs.length * gap + 128} label="One bidirectional gRPC stream: the agent says hello, the server sends a Config, the agent syncs the full picture then changes and heartbeats, the server acknowledges and may send a new Config or revoke" title="The stream, message by message" subtitle="The agent dials out once. After that, both sides talk on the same connection, over mutual TLS.">
      <Box x={A - 60} y={62} w={120} h={30} />
      <T x={A} y={82} anchor="middle" bold>Agent</T>
      <Box x={S - 70} y={62} w={140} h={30} hi />
      <T x={S} y={82} anchor="middle" bold>Server :8443</T>
      <g stroke="var(--cds-chart-grid)" strokeWidth="1.25" strokeDasharray="3 4">
        <path d={`M${A} 92V${y0 + msgs.length * gap - 6}`} />
        <path d={`M${S} 92V${y0 + msgs.length * gap - 6}`} />
      </g>
      {msgs.map(([i, dir, label]) => {
        const y = y0 + i * gap;
        const toServer = dir === 'a';
        return (
          <g key={i}>
            <Arrow fig={f} dashed={!toServer} d={toServer ? `M${A} ${y}H${S}` : `M${S} ${y}H${A}`} accented={i === 0} />
            <T x={(A + S) / 2} y={y - 8} small anchor="middle">{label}</T>
          </g>
        );
      })}
      <Box x={24} y={y0 + msgs.length * gap + 4} w={712} h={104} band />
      <T x={40} y={y0 + msgs.length * gap + 26} small bold>Beside the stream, short calls on the same port</T>
      <Lines x={40} y={y0 + msgs.length * gap + 46} lines={[
        'Enroll and PollEnrollment, once, to get a certificate. Renew, at half of its life.',
        'Rejoin, if the agent was away less than 7 days. RenewTelemetryCert, for the telemetry certificate.',
        'A new tier ends the stream on purpose, so the next one starts with the right watches.',
      ]} />
    </Fig>
  );
}

/** What is inside the agent pod, and what is beside it. */
export function AgentInside() {
  const f = 'agentin';
  return (
    <Fig id={f} height={460} label="Inside the agent: informers read the Kubernetes API, receivers take reports from node DaemonSets, a scope filter cuts to what is allowed, a picture builder prepares it, and one stream sends it to the server" title="Inside the agent" subtitle="One small pod. It reads, filters, and sends. It never writes to the cluster.">
      <Box x={24} y={64} w={156} h={96} />
      <T x={40} y={90} bold>Kubernetes API</T>
      <Lines x={40} y={110} lines={['list and watch only;', 'never Secrets or', 'ConfigMaps']} />

      <Box x={206} y={64} w={304} h={346} frame />
      <T x={222} y={86} bold>ikhnos-agent</T>
      <T x={222} y={102} small muted>one Deployment, cluster-wide read-only RBAC</T>
      {([
        [114, 'Informers', 'One watch per kind of object', false],
        [172, 'Receivers', 'Node probes and flow reports; timing rounds', false],
        [230, 'Scope and tier', 'Cut to install, approval, exclusions', false],
        [288, 'Picture builder', 'The full picture first, then only changes', false],
        [346, 'The stream', 'One gRPC stream, dialled out', true],
      ] as [number, string, string, boolean][]).map(([y, name, line, hi]) => (
        <g key={name}>
          <Box x={222} y={y} w={272} h={46} hi={hi} />
          <T x={236} y={y + 20} bold>{name}</T>
          <T x={236} y={y + 36} small muted>{line}</T>
        </g>
      ))}
      <Arrow fig={f} d="M358 160V172" />
      <Arrow fig={f} d="M358 218V230" />
      <Arrow fig={f} d="M358 276V288" />
      <Arrow fig={f} d="M358 334V346" />
      <Arrow fig={f} d="M180 137H222" />

      <Box x={540} y={64} w={196} h={142} frame />
      <T x={556} y={86} bold>Node DaemonSets</T>
      <T x={556} y={102} small muted>optional, one pod per node</T>
      <Box x={556} y={112} w={164} h={38} />
      <T x={638} y={136} small anchor="middle">Node probe: host facts</T>
      <Box x={556} y={158} w={164} h={38} />
      <T x={638} y={182} small anchor="middle">Flow observer: traffic</T>
      <Arrow fig={f} d="M540 195H494" />

      <Box x={24} y={316} w={156} h={94} />
      <T x={40} y={342} bold>Identity</T>
      <Lines x={40} y={362} lines={['A Secret: key and', 'certificate, renewed', 'at half-life']} />
      <Arrow fig={f} both d="M180 369H222" />

      <Box x={540} y={316} w={196} h={94} hi />
      <T x={556} y={342} bold>Ikhnos server</T>
      <Lines x={556} y={362} lines={['agent port, mutual TLS', 'Config comes back on', 'the same stream']} />
      <Arrow fig={f} both accented d="M494 369H540" />

      <T x={24} y={440} small muted>Receivers keep answering the node DaemonSets even when the server pauses a collector; they just drop what they hear.</T>
    </Fig>
  );
}

/** How the server knows a Config is in force. */
export function ConfigChannel() {
  const f = 'config';
  const col = [24, 268, 512];
  const box = (x: number, y: number, n: number, title: string, lines: string[], hi = false) => (
    <g>
      <Box x={x} y={y} w={224} h={108} hi={hi} />
      <Step x={x} y={y} n={n} />
      <T x={x + 20} y={y + 26} bold>{title}</T>
      <Lines x={x + 20} y={y + 46} lines={lines} />
    </g>
  );
  return (
    <Fig id={f} height={462} label="The server composes a Config and names it with a digest, sends it on the stream; the agent narrows it, applies it and echoes the digest in its heartbeat; the server compares and states three conditions" title="How the server knows what is in force" subtitle="It does not assume a Config arrived. It waits for the agent to name it.">
      {box(col[0], 76, 1, 'Compose', ['Approved tier, pauses,', 'excluded namespaces,', 'link peers, probe targets.'])}
      {box(col[1], 76, 2, 'Name and send', ['The digest is the server’s', 'own name for the Config.', 'It goes on the open stream.'])}
      {box(col[2], 76, 3, 'The agent narrows', ['A tier above its ceiling,', 'an unknown collector, a bad', 'namespace: ignored, and said.'], true)}
      <Arrow fig={f} d="M248 130H268" />
      <Arrow fig={f} d="M492 130H512" />
      <Arrow fig={f} d="M624 184V212" />
      {box(col[2], 212, 4, 'Apply', ['Collectors change. A changed', 'effective tier makes the stream', 'reconnect on purpose.'])}
      {box(col[1], 212, 5, 'Echo', ['The next heartbeat carries', 'applied_config_digest: the', 'digest of what is now in force.'])}
      {box(col[0], 212, 6, 'Compare', ['Sent digest against echoed', 'digest, and a time since.', 'Never a guess.'], true)}
      <Arrow fig={f} d="M512 266H492" />
      <Arrow fig={f} d="M268 266H248" />

      <T x={24} y={352} bold>The server states three conditions about every agent</T>
      {([
        ['Connected', 'True: a stream is open', 'False: none is'],
        ['ConfigInForce', 'True: the agent echoed the digest sent', 'False: it names an earlier one. Unknown: not connected, or it does not echo'],
        ['Compatible', 'True: the protocol is one the server serves', 'False: too new or too old. Unknown: not seen since start'],
      ] as [string, string, string][]).map(([name, a, b], i) => (
        <g key={name}>
          <Box x={24 + i * 242} y={366} w={230} h={80} />
          <T x={38 + i * 242} y={386} small bold mono>{name}</T>
          <foreignObject x={36 + i * 242} y={392} width={206} height={50}>
            <div style={{fontSize: 10.5, lineHeight: '13px', color: 'var(--cds-text-secondary)'}}>
              {a}
              <br />
              {b}
            </div>
          </foreignObject>
        </g>
      ))}
    </Fig>
  );
}

/** The decision loop: an advisor proposes, policy decides what it needs, a person decides. */
export function DecisionLoop() {
  const f = 'loop';
  const steps: [number, string, string[], boolean][] = [
    [24, 'An advisor', ['Any program: rules,', 'a model, a script. It', 'holds an editor’s token.'], false],
    [210, 'A proposal', ['A typed change, up to', '20 facts of evidence,', 'and a predicted effect.'], false],
    [396, 'A person decides', ['Accepting a widening', 'takes an administrator;', 'narrowing, an editor.'], true],
    [582, 'The same path', ['The code a manual', 'change uses: same', 'checks and audit.'], false],
  ];
  return (
    <Fig id={f} height={384} label="An advisor submits a typed proposal; the server works out whether it widens access; a person accepts or rejects; an accepted change goes through the same code as a manual one; later an outcome records what was predicted beside what was observed" title="The decision loop for agent access" subtitle="An advisor proposes. The server's policy decides what approval is needed. A person decides. Mode: advise.">
      {steps.map(([x, name, lines, hi], i) => (
        <g key={name}>
          <Box x={x} y={68} w={154} h={110} hi={hi} />
          <Step x={x} y={68} n={i + 1} />
          <T x={x + 18} y={94} bold>{name}</T>
          <Lines x={x + 18} y={114} lines={lines} />
          {i < steps.length - 1 && <Arrow fig={f} d={`M${x + 154} 123H${x + 186 - 0}`} />}
        </g>
      ))}
      <T x={24} y={216} small bold>What a proposal can be in</T>
      {([
        ['pending', quiet], ['accepted', warn], ['applied', good], ['rejected', bad], ['failed', bad], ['expired', quiet],
      ] as [string, string][]).map(([s, c], i) => (
        <Pill key={s} x={24 + i * 96} y={226} w={86} color={c}>{s}</Pill>
      ))}
      <Box x={24} y={270} w={712} h={90} band />
      <T x={40} y={294} bold>Later, an outcome</T>
      <T x={40} y={312} small muted>The server records what was predicted (for example, link traffic halves) beside what was observed,</T>
      <T x={40} y={328} small muted>keyed to the advisor, so a person can see which advisor to trust. Policy is the server’s, never</T>
      <T x={40} y={344} small muted>the advisor’s: whether a change widens what is shared is worked out here.</T>
      <Arrow fig={f} d="M659 178V270" />
    </Fig>
  );
}

/** The frontend's layers, and how the wire types reach it. */
export function FrontendLayers() {
  const f = 'front';
  const row = (y: number, h: number, hi = false) => <Box x={24} y={y} w={452} h={h} hi={hi} />;
  const feats = (names: string[], y: number, w: number) =>
    names.map((n, i) => (
      <g key={n}>
        <Box x={36 + i * (w + 4)} y={y} w={w} h={22} />
        <T x={36 + i * (w + 4) + w / 2} y={y + 15} small anchor="middle">{n}</T>
      </g>
    ));
  return (
    <Fig id={f} height={452} label="The frontend in layers: the shell on top, features in five layers each using only layers below, shared code at the bottom; and the way the server's Go types become a TypeScript file the frontend imports" title="The frontend, in layers" subtitle="A feature uses shared code and the layers below it, never one beside it or above. A test holds that.">
      {row(64, 40, true)}
      <T x={40} y={88} bold>The shell</T><T x={120} y={88} small muted mono>src/app</T>
      <T x={206} y={88} small muted>routes, layout, what loads which page</T>

      <T x={24} y={124} small bold>Features, highest first</T>
      {row(134, 36)}
      {feats(['topology'], 142, 100)}
      <T x={150} y={157} small muted>the map and its inspector</T>
      {row(176, 36)}
      {feats(['agents'], 184, 100)}
      <T x={150} y={199} small muted>clusters, approvals, diagnostics</T>
      {row(218, 36)}
      {feats(['pipeline'], 226, 100)}
      <T x={150} y={241} small muted>telemetry pipelines</T>
      {row(260, 36)}
      {feats(['discovery', 'health', 'placement'], 268, 100)}
      {row(302, 36)}
      {feats(['accounts', 'activity', 'fusion', 'history', 'links', 'proposals', 'settings'], 310, 58)}

      {row(352, 56, true)}
      <T x={40} y={376} bold>Shared</T>
      <T x={96} y={376} small muted mono>components, lib, store</T>
      <T x={40} y={394} small muted>primitives, the API client, the model, and whatever more than one feature needs.</T>
      <T x={24} y={434} small muted>Shared code never imports a feature. Nothing imports the shell.</T>

      <T x={520} y={88} bold>How a Go type reaches the UI</T>
      {([
        ['A Go type', 'in an API package'],
        ['WireTypes()', 'lists the shapes it writes'],
        ['internal/wire', 'writes them as TypeScript'],
        ['wire.ts', 'committed, imported by the UI'],
        ['A test', 'fails if wire.ts is stale'],
      ] as [string, string][]).map(([a, b], i) => (
        <g key={a}>
          <Box x={520} y={104 + i * 62} w={216} h={44} hi={i === 4} />
          <T x={534} y={104 + i * 62 + 20} small bold mono>{a}</T>
          <T x={534} y={104 + i * 62 + 36} small muted>{b}</T>
          {i < 4 && <Arrow fig={f} d={`M628 ${104 + i * 62 + 44}V${104 + (i + 1) * 62}`} />}
        </g>
      ))}
      <T x={520} y={424} small muted>A renamed field shows as a changed line</T>
      <T x={520} y={440} small muted>in the review of the change that made it.</T>
    </Fig>
  );
}
