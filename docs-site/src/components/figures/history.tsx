/*
 * The five design figures from the planning documents (October 2026), kept as they were drawn. They show a
 * TARGET, not the code as it stands: where a figure says "missing today", it was written before that work was done.
 * They are used on the Design history page, which says so, and nowhere else.
 */
import {accent, edge, ink, quiet, rule, tint, good, warn} from './kit';

export function TargetArchitecture() {
  const cbox = (y: number, main: boolean) => (
    <rect x="40" y={y} width="420" height="68" rx="8" fill={main ? accent : 'none'} fillOpacity={main ? 0.12 : 1} stroke={main ? accent : edge} strokeWidth={main ? 2 : 1.25} />
  );
  const dbox = (y: number) => <rect x="536" y={y} width="184" height="56" rx="8" fill="none" stroke={edge} strokeWidth="1.25" />;
  const band = (y: number) => <rect x="40" y={y} width="420" height="36" rx="8" fill={tint} stroke={edge} strokeWidth="1.25" />;
  return (
    <svg viewBox="0 0 760 728" role="img" aria-label="One loop runs through every feature: intent, desired, delivered, observed, status" fontSize="13" className="ikh-fig">
      <defs>
        <marker id="h1-arrow" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="6" markerHeight="6" orient="auto-start-reverse">
          <path d="M0 0L10 5L0 10z" fill={edge} />
        </marker>
      </defs>
      <text x="24" y="30" fontSize="15" fontWeight="600" fill={ink}>One loop runs through every feature: intent, desired, delivered, observed, status</text>
      <text x="24" y="48" fontSize="11.5" fill={quiet}>Highlighted: the two engine parts that did not exist yet. The others existed but were scattered.</text>
      <g>
        <rect x="24" y="64" width="452" height="640" rx="8" fill="none" stroke={rule} strokeWidth="1.25" />
        <text x="40" y="88" fontWeight="600" fill={ink}>Control plane: one process, an engine and entity modules</text>
      </g>
      <g>
        <rect x="520" y="64" width="216" height="640" rx="8" fill="none" stroke={rule} strokeWidth="1.25" />
        <text x="536" y="88" fontWeight="600" fill={ink}>Data plane</text>
      </g>
      <g>{cbox(104, false)}<text x="56" y="128" fontWeight="600" fill={ink}>1  Intents</text><text x="56" y="144" fontSize="11.5" fill={quiet}>Typed resources: observability, telemetry, link pairs,</text><text x="56" y="160" fontSize="11.5" fill={quiet}>operators, later objectives. Revisions, validation, audit.</text></g>
      <g>{cbox(188, true)}<text x="56" y="212" fontWeight="600" fill={ink}>2  Compiler</text><text x="56" y="228" fontSize="11.5" fill={ink}>Turns intent into typed desired state. The only place a</text><text x="56" y="244" fontSize="11.5" fill={ink}>command, values file or stream config becomes text.</text></g>
      <g>{cbox(272, false)}<text x="56" y="296" fontWeight="600" fill={ink}>3  Delivery</text><text x="56" y="312" fontSize="11.5" fill={quiet}>Three channels: stream config (narrows only), install</text><text x="56" y="328" fontSize="11.5" fill={quiet}>artifact (a person runs it), scoped Kubernetes writes.</text></g>
      <g>{cbox(356, false)}<text x="56" y="380" fontWeight="600" fill={ink}>4  Observation</text><text x="56" y="396" fontSize="11.5" fill={quiet}>Everything the data plane reports, with its age: facts,</text><text x="56" y="412" fontSize="11.5" fill={quiet}>flows, diagnostics, heartbeats, installed config, FUSION.</text></g>
      <g>{cbox(440, true)}<text x="56" y="464" fontWeight="600" fill={ink}>5  Status</text><text x="56" y="480" fontSize="11.5" fill={ink}>Desired against observed, as conditions in one vocabulary:</text><text x="56" y="496" fontSize="11.5" fill={ink}>ready, drifted, stale, not reported. Every screen reads it.</text></g>
      <g>{cbox(524, false)}<text x="56" y="548" fontWeight="600" fill={ink}>6  Advisors</text><text x="56" y="564" fontSize="11.5" fill={quiet}>Advice with evidence for people: placement, the decider,</text><text x="56" y="580" fontSize="11.5" fill={quiet}>later objectives. Reads status; never writes.</text></g>
      <g>{band(608)}<text x="56" y="630" fontSize="11.5" fill={ink}>Memory: audit chain, twin, history, events (append-only)</text></g>
      <g>{band(652)}<text x="56" y="674" fontSize="11.5" fill={ink}>Plug in here: entity modules (Agents, Telemetry, Operators, Links…)</text></g>
      <g fill="none" stroke={edge} strokeWidth="1.25">
        <path d="M250 172V188" markerEnd="url(#h1-arrow)" />
        <path d="M250 256V272" markerEnd="url(#h1-arrow)" />
        <path d="M250 424V440" markerEnd="url(#h1-arrow)" />
        <path d="M250 508V524" markerEnd="url(#h1-arrow)" />
        <path d="M460 306H520" markerEnd="url(#h1-arrow)" />
        <path d="M520 390H460" markerEnd="url(#h1-arrow)" />
      </g>
      <g>{dbox(136)}<text x="552" y="160" fontWeight="600" fill={ink}>Discovery agent</text><text x="552" y="176" fontSize="11.5" fill={quiet}>read-only, dials out</text></g>
      <g>{dbox(224)}<text x="552" y="248" fontWeight="600" fill={ink}>Local telemetry</text><text x="552" y="264" fontSize="11.5" fill={quiet}>collectors, Helm-only</text></g>
      <g>{dbox(312)}<text x="552" y="336" fontWeight="600" fill={ink}>Link prober</text><text x="552" y="352" fontSize="11.5" fill={quiet}>latency, loss, throughput</text></g>
      <g>{dbox(400)}<text x="552" y="424" fontWeight="600" fill={ink}>Regional operators</text><text x="552" y="440" fontSize="11.5" fill={quiet}>gateways, heartbeat</text></g>
      <g>{dbox(488)}<text x="552" y="512" fontWeight="600" fill={ink}>FUSION</text><text x="552" y="528" fontSize="11.5" fill={quiet}>Prometheus, Loki, Tempo</text></g>
      <g>
        <rect x="536" y="588" width="184" height="100" rx="8" fill="none" stroke={rule} strokeWidth="1.25" />
        <text x="552" y="614" fontSize="11.5" fill={quiet}>The cluster owner sets</text>
        <text x="552" y="630" fontSize="11.5" fill={quiet}>the ceiling in Helm values.</text>
        <text x="552" y="646" fontSize="11.5" fill={quiet}>The control plane can only</text>
        <text x="552" y="662" fontSize="11.5" fill={quiet}>narrow inside that ceiling.</text>
      </g>
    </svg>
  );
}

export function ChangeSequence() {
  const head = (x: number, w: number) => <rect x={x - w / 2} y="64" width={w} height="30" rx="8" fill="none" stroke={edge} strokeWidth="1.25" />;
  const call = (x1: number, x2: number, y: number, dashed: boolean) => (
    <path d={`M${x1} ${y}H${x2}`} fill="none" stroke={edge} strokeWidth="1.25" strokeDasharray={dashed ? '4 4' : undefined} markerEnd="url(#h2-arrow)" />
  );
  const lives: [number, number, string][] = [[74, 88, 'API'], [174, 88, 'Intents'], [274, 88, 'Delivery'], [374, 88, 'Compiler'], [474, 88, 'Agent'], [574, 96, 'Observation'], [674, 88, 'Status']];
  const steps: [number, number, number, boolean, number, string][] = [
    [74, 174, 134, false, 124, '1  Put'],
    [174, 274, 178, true, 224, '2  Changed'],
    [274, 374, 222, false, 324, '3  Compile'],
    [374, 274, 266, true, 324, '4  Desired'],
    [274, 474, 310, false, 374, '5  Config (narrow only)'],
    [474, 574, 354, false, 524, '6  Diagnostics'],
    [574, 674, 398, true, 624, '7  Changed'],
  ];
  return (
    <svg viewBox="0 0 760 468" role="img" aria-label="A change travels the same loop every time: intent, compile, deliver, observe, judge" fontSize="13" className="ikh-fig">
      <defs>
        <marker id="h2-arrow" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="6" markerHeight="6" orient="auto-start-reverse">
          <path d="M0 0L10 5L0 10z" fill={edge} />
        </marker>
      </defs>
      <text x="24" y="30" fontSize="15" fontWeight="600" fill={ink}>A change travels the same loop every time: intent, compile, deliver, observe, judge</text>
      <text x="24" y="48" fontSize="11.5" fill={quiet}>Example: an administrator pauses the flow collector on one agent. Dashed arrows are events and returns.</text>
      <g stroke={rule} strokeWidth="1.25" strokeDasharray="3 4">
        {lives.map(([x]) => <path key={x} d={`M${x} 94V444`} />)}
      </g>
      {lives.map(([x, w, name]) => (
        <g key={name}>{head(x, w)}<text x={x} y="84" textAnchor="middle" fontWeight="600" fill={ink}>{name}</text></g>
      ))}
      {steps.map(([x1, x2, y, dashed, lx, label]) => (
        <g key={label}>{call(x1, x2, y, dashed)}<text x={lx} y={y - 8} textAnchor="middle" fontSize="11.5" fill={ink}>{label}</text></g>
      ))}
      <g>
        <rect x="586" y="412" width="150" height="28" rx="8" fill={accent} fillOpacity="0.12" stroke={accent} strokeWidth="2" />
        <text x="661" y="430" textAnchor="middle" fontSize="11.5" fontWeight="600" fill={ink}>8  ConfigApplied: true</text>
      </g>
    </svg>
  );
}

export function MigrationRoadmap() {
  const phase = (y: number, main: boolean) => <rect x="24" y={y} width="430" height="64" rx="8" fill={main ? accent : 'none'} fillOpacity={main ? 0.12 : 1} stroke={main ? accent : edge} strokeWidth={main ? 2 : 1.25} />;
  const gate = (y: number) => (
    <g fill="none" stroke={edge} strokeWidth="1.25">
      <path d={`M454 ${y + 32}H460`} />
      <path d={`M460 ${y + 32}L468 ${y + 24}L476 ${y + 32}L468 ${y + 40}Z`} />
      <path d={`M476 ${y + 32}H486`} />
      <rect x="486" y={y + 8} width="250" height="48" rx="8" />
    </g>
  );
  const rows: [string, string, string, string, string, string, boolean][] = [
    ['0  Pin current behaviour', 'Characterization tests for configFor, every command builder', '(Go and TypeScript) and the status words', 'Gate: a field dropped from the hash', 'makes a test fail', '', true],
    ['1  Contracts, no behaviour change', 'Condition, resource fields, capability set, contributor and', 'renderer interfaces; import-rule tests, shrinking allowlist', 'Gate: nothing changes at runtime;', 'the rules run on the existing code', '', true],
    ['2  Config channel', 'configFor becomes contributors, one feature at a time: consent,', 'probe targets, link pairs, credentials; the digest is computed', 'Gate: identical Config on the test', 'matrix; no hash edit for a new field', '', true],
    ['3  Install rendering', 'A typed install spec and one server renderer; the UI edits the', 'spec; the string appenders are deleted', 'Gate: golden commands unchanged;', 'one package builds helm lines', '', false],
    ['4  Status', 'Component reports and conditions: operators first, then agents,', 'then FUSION; one frontend status module', 'Gate: four vocabularies become one;', 'every screen shows its reason', '', false],
    ['5  Split the server package', 'One entity module at a time, each with a narrow store port;', 'the hub split into session and observe; hooks become events', 'Gate: server holds only wiring; no', 'import cycles; empty allowlist', '', false],
    ['6  API schema and frontend features', 'Generated API types, feature folders, the shared status', 'component, an import rule for features', 'Gate: renaming a Go field breaks', 'the TypeScript build', '', false],
    ['7  First feature on the platform', 'Objectives: a new resource kind, compiler, channel and status', 'contributor, with no edits to shared functions', 'Gate: it adds no code to shared', 'functions such as configFor', '', false],
  ];
  return (
    <svg viewBox="0 0 760 684" role="img" aria-label="Start with the config channel; every phase ends at a gate you can test" fontSize="13" className="ikh-fig">
      <defs>
        <marker id="h3-arrow" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="6" markerHeight="6" orient="auto-start-reverse">
          <path d="M0 0L10 5L0 10z" fill={edge} />
        </marker>
      </defs>
      <text x="24" y="30" fontSize="15" fontWeight="600" fill={ink}>Start with the config channel; every phase ends at a gate you can test</text>
      <text x="24" y="48" fontSize="11.5" fill={quiet}>Highlighted: the first slice (phases 0 to 2). Phases run in order; a gate decides when the next starts.</text>
      {rows.map(([name, a, b, g1, g2, , main], i) => {
        const y = 64 + i * 76;
        return (
          <g key={name}>
            {phase(y, main)}
            <text x="40" y={y + 24} fontWeight="600" fill={ink}>{name}</text>
            <text x="40" y={y + 40} fontSize="11.5" fill={main ? ink : quiet}>{a}</text>
            <text x="40" y={y + 56} fontSize="11.5" fill={main ? ink : quiet}>{b}</text>
            {gate(y)}
            <text x="500" y={y + 28} fontSize="11.5" fill={ink}>{g1}</text>
            <text x="500" y={y + 44} fontSize="11.5" fill={ink}>{g2}</text>
          </g>
        );
      })}
      <g fill="none" stroke={edge} strokeWidth="1.25">
        {[128, 204, 280, 356, 432, 508, 584].map((y) => <path key={y} d={`M239 ${y}V${y + 12}`} markerEnd="url(#h3-arrow)" />)}
      </g>
    </svg>
  );
}

export function WorkingPlan() {
  const W = 164;
  const box = (x: number, y: number, h: number, k: 'good' | 'cur' | 'wait') => {
    const c = k === 'good' ? good : k === 'cur' ? accent : edge;
    return <rect x={x} y={y} width={W} height={h} rx="8" fill={k === 'wait' ? 'none' : c} fillOpacity={k === 'wait' ? 1 : 0.12} stroke={c} strokeWidth={k === 'wait' ? 1.25 : 2} />;
  };
  const stages: [number, 'good' | 'cur', string, string[], string, string[]][] = [
    [24, 'cur', '0 Pin behaviour', ['Tests before change', 'Hash, routes, Config', 'Open: cross-org, helm,', 'fake agent, status'], 'current', ['Dropped field, missing', 'guard, cross-org read', 'each fail a test']],
    [204, 'good', '1 Audit atomic', ['Row and change in', 'one transaction', '33 of 34 sites'], 'done', ['Chain format unchanged;', 'each site reverts alone']],
    [384, 'cur', '2 Config channel', ['Digest echo: done', 'Capabilities: open', 'Conditions: open', 'One additive change'], 'current', ['Server knows which', 'revision is in force']],
    [564, 'good', '3 Decision loop', ['Proposal and Outcome', 'Advise mode only', 'Auto: designed,', 'shelved'], 'done', ['Nothing changes unless', 'a person accepts it']],
  ];
  const after: [number, string, string[]][] = [
    [24, 'Agent channel', ['Contributors replace', 'configFor; the hash', 'is computed']],
    [204, 'Install spec, API', ['One typed spec and', 'renderer on server;', 'API types generated']],
    [384, 'Status everywhere', ['One condition view,', 'server-sent events,', 'one staleness rule']],
    [564, 'Split and folders', ['server split by', 'module; frontend', 'feature folders']],
  ];
  return (
    <svg viewBox="0 0 760 498" role="img" aria-label="Four stages, then a stop line: everything below waits" fontSize="13" className="ikh-fig">
      <defs>
        <marker id="h4-arrow" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="6" markerHeight="6" orient="auto-start-reverse">
          <path d="M0 0L10 5L0 10z" fill={edge} />
        </marker>
      </defs>
      <text x="24" y="34" fontSize="15" fontWeight="600" fill={ink}>Four stages, then a stop line: everything below waits</text>
      {stages.map(([x, k, name, lines, status, gate], i) => (
        <g key={name}>
          {box(x, 56, 116, k)}
          <text x={x + 12} y="80" fontWeight="600" fill={ink}>{name}</text>
          {lines.map((l, j) => <text key={l} x={x + 12} y={96 + j * 16} fontSize="11.5" fill={quiet}>{l}</text>)}
          <text x={x + 12} y="160" fontSize="11.5" fontWeight="600" fill={ink}>{status}</text>
          <line x1={x + 82} y1="172" x2={x + 82} y2="180" stroke={edge} strokeWidth="1.25" />
          <path d={`M${x + 82} 180l8 8l-8 8l-8 -8z`} fill="none" stroke={edge} strokeWidth="1.25" />
          {gate.map((l, j) => <text key={l} x={x + 4} y={218 + j * 16} fontSize="11.5" fill={quiet}>{l}</text>)}
          {i < 3 && <path d={`M${x + 164} 114H${x + 180}`} fill="none" stroke={edge} strokeWidth="1.25" markerEnd="url(#h4-arrow)" />}
        </g>
      ))}
      <line x1="24" y1="268" x2="728" y2="268" stroke={warn} strokeWidth="2" strokeDasharray="6 4" />
      <text x="376" y="292" fontSize="12.5" fontWeight="600" textAnchor="middle" fill={ink}>Stop line: nothing below starts until stage 3 has run in production</text>
      <text x="24" y="320" fontSize="11.5" fill={ink}>After the stop line, in the order of the architecture document</text>
      {after.map(([x, name, lines], i) => (
        <g key={name}>
          {box(x, 332, 84, 'wait')}
          <text x={x + 12} y="356" fontWeight="600" fill={ink}>{name}</text>
          {lines.map((l, j) => <text key={l} x={x + 12} y={372 + j * 16} fontSize="11.5" fill={quiet}>{l}</text>)}
          {i < 3 && <path d={`M${x + 164} 374H${x + 180}`} fill="none" stroke={edge} strokeWidth="1.25" markerEnd="url(#h4-arrow)" />}
        </g>
      ))}
      <text x="24" y="442" fontSize="11.5" fill={ink}>Parked or cut for now: Objectives, CRDs and the controller, decommission levels 2 and 3, Auto mode.</text>
      <rect x="24" y="470" width="12" height="12" rx="3" fill={good} fillOpacity="0.12" stroke={good} strokeWidth="2" />
      <text x="44" y="480" fontSize="11.5" fill={ink}>done</text>
      <rect x="92" y="470" width="12" height="12" rx="3" fill={accent} fillOpacity="0.12" stroke={accent} strokeWidth="2" />
      <text x="112" y="480" fontSize="11.5" fill={ink}>current work</text>
      <rect x="196" y="470" width="12" height="12" rx="3" fill="none" stroke={edge} strokeWidth="1.25" />
      <text x="216" y="480" fontSize="11.5" fill={ink}>waits</text>
      <path d="M262 470l6 6l-6 6l-6 -6z" fill="none" stroke={edge} strokeWidth="1.25" />
      <text x="282" y="480" fontSize="11.5" fill={ink}>gate: the test that lets the next stage start</text>
    </svg>
  );
}

export function PlanePlan() {
  const chip = (x: number, y: number, hi: boolean) => <rect x={x} y={y} width="100" height="40" rx="8" fill={hi ? accent : 'none'} fillOpacity={hi ? 0.12 : 1} stroke={hi ? accent : edge} strokeWidth={hi ? 2 : 1.25} />;
  const dbox = (y: number) => <rect x="536" y={y} width="184" height="72" rx="8" fill="none" stroke={edge} strokeWidth="1.25" />;
  const modules = ['Accounts', 'Agents', 'Estate', 'Telemetry', 'Operators', 'Connectivity', 'Fusion', 'Advice'];
  const engine: [string, boolean][] = [['Intent kit', true], ['Compiler', false], ['Delivery', false], ['Observation', false], ['Status', true], ['Advisors', false], ['Memory', true], ['Policy', true]];
  return (
    <svg viewBox="0 0 760 520" role="img" aria-label="Eight modules plug into one engine; each channel has its own receipt" fontSize="13" className="ikh-fig">
      <defs>
        <marker id="h5-arrow" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="6" markerHeight="6" orient="auto-start-reverse">
          <path d="M0 0L10 5L0 10z" fill={edge} />
        </marker>
      </defs>
      <text x="24" y="30" fontSize="15" fontWeight="600" fill={ink}>Eight modules plug into one engine; each channel has its own receipt</text>
      <text x="24" y="48" fontSize="11.5" fill={quiet}>Highlighted: missing or incomplete when this was drawn. Dashed: a person is in the path.</text>
      <g><rect x="24" y="64" width="460" height="432" rx="8" fill="none" stroke={rule} strokeWidth="1.25" /><text x="40" y="84" fontWeight="600" fill={ink}>Control plane: one process, one SQLite writer</text></g>
      <g><rect x="520" y="64" width="216" height="432" rx="8" fill="none" stroke={rule} strokeWidth="1.25" /><text x="536" y="84" fontWeight="600" fill={ink}>Data plane</text></g>
      <g><rect x="40" y="100" width="428" height="48" rx="8" fill="none" stroke={edge} strokeWidth="1.25" /><text x="56" y="120" fontWeight="600" fill={ink}>API: thin handlers</text><text x="56" y="138" fontSize="11.5" fill={quiet}>Parse, ask Policy, call one module. No domain logic.</text></g>
      <text x="40" y="176" fontSize="11.5" fontWeight="600" fill={ink}>Entity modules: each owns its data, routes, screens and tests</text>
      {modules.map((m, i) => {
        const x = 40 + (i % 4) * 109, y = 188 + Math.floor(i / 4) * 48;
        return <g key={m}>{chip(x, y, false)}<text x={x + 50} y={y + 24} textAnchor="middle" fontWeight="600" fill={ink}>{m}</text></g>;
      })}
      <line x1="40" x2="468" y1="292" y2="292" stroke={rule} strokeWidth="1.25" />
      <text x="40" y="312" fontSize="11.5" fontWeight="600" fill={ink}>Shared engine: built once, so no module grows its own plumbing</text>
      {engine.map(([m, hi], i) => {
        const x = 40 + (i % 4) * 109, y = 324 + Math.floor(i / 4) * 48;
        return <g key={m}>{chip(x, y, hi)}<text x={x + 50} y={y + 24} textAnchor="middle" fontWeight="600" fill={ink}>{m}</text></g>;
      })}
      <g><rect x="40" y="424" width="428" height="36" rx="8" fill={tint} stroke={edge} strokeWidth="1.25" /><text x="56" y="447" fontSize="11.5" fill={ink}>One SQLite writer; an outbox feeds Neo4j and events</text></g>
      <g fill="none" stroke={edge} strokeWidth="1.25">
        <path d="M484 136H536" markerStart="url(#h5-arrow)" markerEnd="url(#h5-arrow)" />
        <path d="M484 216H536" strokeDasharray="4 4" markerStart="url(#h5-arrow)" markerEnd="url(#h5-arrow)" />
        <path d="M484 296H536" strokeDasharray="4 4" markerStart="url(#h5-arrow)" markerEnd="url(#h5-arrow)" />
        <path d="M484 376H536" markerEnd="url(#h5-arrow)" />
      </g>
      <g>{dbox(100)}<text x="552" y="122" fontWeight="600" fill={ink}>Discovery agent</text><text x="552" y="140" fontSize="11.5" fill={quiet}>Config and revision down</text><text x="552" y="156" fontSize="11.5" fill={quiet}>status and facts up</text></g>
      <g>{dbox(180)}<text x="552" y="202" fontWeight="600" fill={ink}>Local telemetry</text><text x="552" y="220" fontSize="11.5" fill={quiet}>Command a person runs</text><text x="552" y="236" fontSize="11.5" fill={quiet}>Agent reports installed</text></g>
      <g>{dbox(260)}<text x="552" y="282" fontWeight="600" fill={ink}>Regional operators</text><text x="552" y="300" fontSize="11.5" fill={quiet}>Command, then heartbeat</text><text x="552" y="316" fontSize="11.5" fill={quiet}>Certificate renewal pull</text></g>
      <g>{dbox(340)}<text x="552" y="362" fontWeight="600" fill={ink}>FUSION</text><text x="552" y="380" fontSize="11.5" fill={quiet}>Writes to named objects</text><text x="552" y="396" fontSize="11.5" fill={quiet}>Read-back, no Secret read</text></g>
      <g>
        <rect x="536" y="420" width="184" height="60" rx="8" fill="none" stroke={rule} strokeWidth="1.25" />
        <text x="552" y="438" fontSize="11.5" fill={quiet}>The cluster owner sets</text>
        <text x="552" y="454" fontSize="11.5" fill={quiet}>the ceiling in Helm; the</text>
        <text x="552" y="470" fontSize="11.5" fill={quiet}>server narrows inside it.</text>
      </g>
    </svg>
  );
}

export function CrdFlow() {
  const box = (x: number, y: number, w: number, h: number, hi: boolean) => <rect x={x} y={y} width={w} height={h} rx="8" fill={hi ? accent : 'none'} fillOpacity={hi ? 0.12 : 1} stroke={hi ? accent : edge} strokeWidth={hi ? 2 : 1.25} />;
  return (
    <svg viewBox="0 0 760 484" role="img" aria-label="The server renders, people apply, a controller reconciles" fontSize="13" className="ikh-fig">
      <defs>
        <marker id="h6-arrow" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="6" markerHeight="6" orient="auto-start-reverse">
          <path d="M0 0L10 5L0 10z" fill={edge} />
        </marker>
      </defs>
      <text x="24" y="30" fontSize="15" fontWeight="600" fill={ink}>The server renders, people apply, a controller reconciles</text>
      <text x="24" y="48" fontSize="11.5" fill={quiet}>Solid: the existing stream. Dashed: a person or GitOps is in the path.</text>
      <g>
        {box(24, 100, 208, 328, false)}
        <text x="40" y="124" fontWeight="600" fill={ink}>Ikhnos server</text>
        {['The intent kit compiles one', 'InstallSpec. Renderers:', 'command, values file,', 'or CR manifest.'].map((l, i) => <text key={l} x="40" y={144 + i * 16} fontSize="11.5" fill={quiet}>{l}</text>)}
        <text x="40" y="228" fontSize="11.5" fontWeight="600" fill={ink}>Never writes a CR into</text>
        <text x="40" y="244" fontSize="11.5" fontWeight="600" fill={ink}>a cluster.</text>
        {['Learns what is in force', 'from the agent report,', 'never from the cluster API.'].map((l, i) => <text key={l} x="40" y={288 + i * 16} fontSize="11.5" fill={quiet}>{l}</text>)}
      </g>
      <g>{box(264, 120, 176, 72, false)}<text x="280" y="144" fontWeight="600" fill={ink}>Person or GitOps</text><text x="280" y="162" fontSize="11.5" fill={quiet}>Argo, Flux or kubectl</text><text x="280" y="178" fontSize="11.5" fill={quiet}>applies the manifest</text></g>
      <g><rect x="472" y="64" width="264" height="396" rx="8" fill="none" stroke={rule} strokeWidth="1.25" /><text x="488" y="84" fontWeight="600" fill={ink}>Cluster</text></g>
      <g>{box(488, 100, 232, 76, false)}<text x="504" y="122" fontWeight="600" fill={ink}>Custom resources</text><text x="504" y="140" fontSize="11.5" fill={quiet}>Agent, Telemetry,</text><text x="504" y="156" fontSize="11.5" fill={quiet}>Operator, Destination</text></g>
      <g>{box(488, 196, 232, 76, true)}<text x="504" y="218" fontWeight="600" fill={ink}>ikhnos-controller</text><text x="504" y="236" fontSize="11.5" fill={ink}>Writes status and owned</text><text x="504" y="252" fontSize="11.5" fill={ink}>objects, never the spec</text></g>
      <g>{box(488, 292, 232, 60, false)}<text x="504" y="314" fontWeight="600" fill={ink}>Owned workloads</text><text x="504" y="332" fontSize="11.5" fill={quiet}>agent, collectors, probers</text></g>
      <g>{box(488, 368, 232, 76, false)}<text x="504" y="390" fontWeight="600" fill={ink}>Discovery agent</text><text x="504" y="408" fontSize="11.5" fill={quiet}>Read-only, dials the server</text><text x="504" y="424" fontSize="11.5" fill={quiet}>and reports CR status</text></g>
      <g fill="none" stroke={edge} strokeWidth="1.25">
        <path d="M232 156H264" strokeDasharray="4 4" markerEnd="url(#h6-arrow)" />
        <path d="M440 156H488" strokeDasharray="4 4" markerEnd="url(#h6-arrow)" />
        <path d="M604 176V196" markerEnd="url(#h6-arrow)" />
        <path d="M604 272V292" markerEnd="url(#h6-arrow)" />
        <path d="M488 406H232" markerStart="url(#h6-arrow)" markerEnd="url(#h6-arrow)" />
      </g>
      <text x="360" y="396" textAnchor="middle" fontSize="11.5" fill={quiet}>Config down, status up</text>
    </svg>
  );
}
