import {
  DEFAULT_ORG,
  type Agent,
  type Application,
  type AuditEvent,
  type Cluster,
  type Confidence,
  type Dependency,
  type DependencySource,
  type Device,
  type EndpointKind,
  type MachineNode,
  type Model,
  type Namespace,
  type Service,
  type Site,
  type SiteLink,
  type Suggestion,
} from './types'

const SEEN = '2026-09-19T08:00:00.000Z'

const c = (p: Partial<Cluster> & Pick<Cluster, 'id' | 'name' | 'tier'>): Cluster => ({
  distribution: 'kubeadm',
  version: 'v1.30.2',
  provider: 'On-prem',
  region: '',
  status: 'healthy',
  labels: {},
  orgId: DEFAULT_ORG,
  source: 'manual',
  ...p,
})

const n = (
  p: Partial<MachineNode> & Pick<MachineNode, 'id' | 'name' | 'clusterId'>,
): MachineNode => ({
  role: 'worker',
  kind: 'vm',
  ip: '',
  os: 'Ubuntu 22.04',
  cpu: 4,
  memoryGb: 16,
  status: 'healthy',
  labels: {},
  orgId: DEFAULT_ORG,
  source: 'manual',
  ...p,
})

const w = (
  p: Partial<Service> & Pick<Service, 'id' | 'name' | 'clusterId' | 'nodeIds'>,
): Service => ({
  namespace: 'default',
  kind: 'Deployment',
  image: '',
  replicas: 1,
  status: 'healthy',
  labels: {},
  orgId: DEFAULT_ORG,
  source: 'manual',
  ...p,
})

const dev = (p: Partial<Device> & Pick<Device, 'id' | 'name' | 'kind'>): Device => ({
  count: 1,
  protocol: 'MQTT',
  connectivity: 'ethernet',
  status: 'healthy',
  labels: {},
  orgId: DEFAULT_ORG,
  source: 'manual',
  ...p,
})

const dd = (
  id: string,
  from: string,
  fromKind: EndpointKind,
  to: string,
  toKind: EndpointKind,
  protocol: string,
  port?: number,
  sources: DependencySource[] = ['manual'],
  confidence: Confidence = 'high',
  extra: Partial<Dependency> = {},
): Dependency => ({
  id,
  orgId: DEFAULT_ORG,
  from,
  fromKind,
  to,
  toKind,
  protocol,
  port,
  sources,
  confidence,
  ...(sources.includes('observed') || sources.includes('declared') ? { firstSeen: SEEN, lastSeen: SEEN } : {}),
  ...extra,
})

/** Service → service dependency (the common case). */
const d = (
  id: string,
  from: string,
  to: string,
  protocol: string,
  port?: number,
  sources: DependencySource[] = ['manual'],
  confidence: Confidence = 'high',
  extra: Partial<Dependency> = {},
): Dependency => dd(id, from, 'service', to, 'service', protocol, port, sources, confidence, extra)

const site = (
  id: string,
  name: string,
  kind: Site['kind'],
  lat: number,
  lng: number,
  country: string,
  extra: Partial<Site> = {},
): Site => ({
  id,
  orgId: DEFAULT_ORG,
  name,
  kind,
  lat,
  lng,
  country,
  dataResidency: 'EU',
  ...extra,
})

const link = (id: string, a: string, b: string, rttMs: number, lossPct: number, mbps: number): SiteLink => ({
  id,
  orgId: DEFAULT_ORG,
  a,
  b,
  rttMs,
  lossPct,
  mbps,
  measuredAt: SEEN,
  source: 'measured',
})

const app = (id: string, name: string, description: string, origin = 'explicit'): Application => ({
  id,
  orgId: DEFAULT_ORG,
  name,
  description,
  origin,
  confidence: 'high',
  source: 'manual',
})

/** A small cloud → edge → far-edge continuum for an AI-driven IoT analytics pipeline. */
export function seedTopology(): Model {
  const clusters: Cluster[] = [
    c({
      id: 'cl-cloud', siteId: 'site-fra', name: 'aws-eu-central', tier: 'cloud', distribution: 'EKS', provider: 'AWS', region: 'eu-central-1',
      cni: 'aws-vpc-cni', ingress: 'aws-load-balancer-controller', podCidr: '10.0.0.0/16', serviceCidr: '172.20.0.0/16', cloudAccount: '1234-5678-9012',
      apiEndpoint: 'ABCD1234.gr7.eu-central-1.eks.amazonaws.com', trustZone: 'private', dataResidency: 'EU',
    }),
    c({
      id: 'cl-region', siteId: 'site-ath', name: 'athens-regional', tier: 'edge', distribution: 'kubeadm', provider: 'On-prem', region: 'Athens DC',
      cni: 'calico', ingress: 'ingress-nginx', podCidr: '192.168.0.0/16', serviceCidr: '10.96.0.0/12', storageClasses: ['local-path', 'nfs'], trustZone: 'private', dataResidency: 'EU',
    }),
    c({
      id: 'cl-edge-a', siteId: 'site-pat', source: 'discovered', lastSeen: SEEN, detectedAt: SEEN, agentId: 'ag-patras', revision: 42,
      key: 'kube-system:0d5c8b1e-6f3a-4c0e-9b52-7a1f0e2d4c11',
      evidence: { distribution: { signal: 'server version v1.29.6+k3s1', confidence: 'high', detail: '+k3s suffix' } },
      name: 'edge-patras', tier: 'far-edge', distribution: 'k3s', version: 'v1.29.6+k3s1', provider: 'On-prem', region: 'Patras',
      cni: 'flannel', ingress: 'traefik', podCidr: '10.42.0.0/16', serviceCidr: '10.43.0.0/16', storageClasses: ['local-path'], egressIp: '203.0.113.24', createdAt: '2025-11-03T09:00:00.000Z', trustZone: 'restricted', dataResidency: 'EU',
    }),
    c({
      id: 'cl-edge-b', siteId: 'site-thess', source: 'discovered', lastSeen: SEEN, detectedAt: SEEN, agentId: 'ag-thess', revision: 17,
      key: 'kube-system:9a4be7d0-31c8-4f5a-8e26-c5d0b7a93f02',
      evidence: { distribution: { signal: 'server version v1.29.6+k3s1', confidence: 'high', detail: '+k3s suffix' } },
      overrides: { region: 'Thessaloniki lab' },
      name: 'edge-thessaloniki', tier: 'far-edge', distribution: 'k3s', version: 'v1.29.6+k3s1', provider: 'On-prem', region: 'Thessaloniki', status: 'degraded',
      cni: 'flannel', ingress: 'traefik', podCidr: '10.42.0.0/16', serviceCidr: '10.43.0.0/16', storageClasses: ['local-path'], egressIp: '198.51.100.9', trustZone: 'restricted', dataResidency: 'EU',
    }),
  ]

  const nodes: MachineNode[] = [
    n({ id: 'n-c1', name: 'eks-cp-1', clusterId: 'cl-cloud', role: 'control-plane', ip: '10.0.1.10', cpu: 4, memoryGb: 16, arch: 'amd64', instanceType: 'm6i.xlarge', zone: 'eu-central-1a' }),
    n({
      id: 'n-c2', name: 'eks-worker-1', clusterId: 'cl-cloud', ip: '10.0.2.11', cpu: 16, memoryGb: 64, arch: 'amd64', instanceType: 'm6i.4xlarge', zone: 'eu-central-1a', runtime: 'containerd 1.7.13', kernel: '5.10.215',
      allocatable: { cpu: 15.8, memoryGb: 61 }, requested: { cpu: 6.2, memoryGb: 22 }, podCount: 38, podCapacity: 234, createdAt: '2026-02-10T08:00:00.000Z',
    }),
    n({ id: 'n-c3', name: 'eks-worker-2', clusterId: 'cl-cloud', ip: '10.0.2.12', cpu: 16, memoryGb: 64, arch: 'amd64', instanceType: 'm6i.4xlarge', zone: 'eu-central-1b' }),
    n({
      id: 'n-c4', name: 'eks-gpu-1', clusterId: 'cl-cloud', ip: '10.0.3.20', cpu: 32, memoryGb: 128, labels: { accelerator: 'nvidia-a10' }, arch: 'amd64', instanceType: 'g5.8xlarge', zone: 'eu-central-1b',
      accelerators: [{ vendor: 'NVIDIA', model: 'A10G', count: 1 }], taints: ['nvidia.com/gpu=present:NoSchedule'],
    }),

    n({ id: 'n-r1', name: 'ath-cp-1', clusterId: 'cl-region', role: 'control-plane', kind: 'bare-metal', ip: '192.168.10.2', cpu: 8, memoryGb: 32, arch: 'amd64' }),
    n({
      id: 'n-r2', name: 'ath-worker-1', clusterId: 'cl-region', kind: 'bare-metal', ip: '192.168.10.11', cpu: 16, memoryGb: 64, arch: 'amd64', hardwareModel: 'Dell PowerEdge R650', kernel: '5.15.0-118', runtime: 'containerd 1.7.13',
      evidence: { kind: { signal: 'DMI vendor Dell Inc., no hypervisor flag', confidence: 'high' } },
    }),
    n({ id: 'n-r3', name: 'ath-worker-2', clusterId: 'cl-region', kind: 'bare-metal', ip: '192.168.10.12', cpu: 16, memoryGb: 64, arch: 'amd64', hardwareModel: 'Dell PowerEdge R650' }),

    n({ id: 'n-a1', name: 'patras-gw-1', clusterId: 'cl-edge-a', role: 'control-plane', kind: 'edge-device', ip: '172.16.1.2', os: 'Debian 12', cpu: 4, memoryGb: 8, arch: 'amd64', connectivity: 'cellular' }),
    n({
      id: 'n-a2', source: 'discovered', lastSeen: SEEN, detectedAt: SEEN, agentId: 'ag-patras', revision: 42, key: 'machine-id:6c1f0a52d3e94b7d8a90b1c2e3f40516',
      evidence: { kind: { signal: 'device-tree model: NVIDIA Jetson Orin Nano', confidence: 'high' } },
      name: 'patras-jetson-1', clusterId: 'cl-edge-a', kind: 'edge-device', ip: '172.16.1.11', os: 'JetPack 6', cpu: 6, memoryGb: 8, labels: { accelerator: 'jetson-orin' },
      arch: 'arm64', hardwareModel: 'NVIDIA Jetson Orin Nano', kernel: '5.15.136-tegra', runtime: 'containerd 1.7.13', connectivity: 'ethernet',
      accelerators: [{ vendor: 'NVIDIA', model: 'Orin (integrated GPU)', count: 1 }], allocatable: { cpu: 5.9, memoryGb: 7.2 }, requested: { cpu: 3.1, memoryGb: 4.4 }, podCount: 9, podCapacity: 110, createdAt: '2025-11-03T09:30:00.000Z',
    }),

    n({ id: 'n-b1', name: 'thess-gw-1', clusterId: 'cl-edge-b', role: 'control-plane', kind: 'edge-device', ip: '172.16.2.2', os: 'Debian 12', cpu: 4, memoryGb: 8, arch: 'amd64', connectivity: 'cellular' }),
    n({
      id: 'n-b2', source: 'discovered', lastSeen: SEEN, detectedAt: SEEN, agentId: 'ag-thess', revision: 17, key: 'machine-id:b7e2c9d14a0f4e6a9c3d5e7f81920a3b',
      evidence: { kind: { signal: 'device-tree model: Raspberry Pi 4 Model B', confidence: 'high' } },
      name: 'thess-rpi-1', clusterId: 'cl-edge-b', kind: 'edge-device', ip: '172.16.2.11', os: 'Raspberry Pi OS', cpu: 4, memoryGb: 4, status: 'degraded',
      arch: 'arm64', hardwareModel: 'Raspberry Pi 4 Model B', connectivity: 'wifi', conditions: ['MemoryPressure'], allocatable: { cpu: 3.9, memoryGb: 3.6 }, requested: { cpu: 2.2, memoryGb: 3.4 }, podCount: 28, podCapacity: 30, createdAt: '2026-03-21T14:10:00.000Z',
    }),
  ]

  const services: Service[] = [
    // cloud
    w({ id: 'w-gw', applicationId: 'app-api', name: 'api-gateway', clusterId: 'cl-cloud', namespace: 'platform', autoscaler: { min: 2, max: 6, current: 2, targets: ['cpu 80%'] }, disruption: { minAvailable: '1', allowed: 1 }, image: 'envoy:1.30', replicas: 2, readyReplicas: 2, nodeIds: ['n-c2', 'n-c3'], exposure: 'ingress', hosts: ['api.example.org'], ports: [443], managedBy: 'helm', sensitivity: 'public' }),
    w({ id: 'w-orch', applicationId: 'app-api', name: 'orchestrator', clusterId: 'cl-cloud', namespace: 'platform', image: 'ghcr.io/acme/orchestrator:0.4', replicas: 2, readyReplicas: 2, nodeIds: ['n-c2', 'n-c3'], managedBy: 'argo', ports: [9000], cpuRequestM: 500, memRequestMi: 512, sensitivity: 'internal' }),
    w({ id: 'w-train', applicationId: 'app-ml', name: 'model-training', clusterId: 'cl-cloud', namespace: 'ml', kind: 'Job', image: 'ghcr.io/acme/trainer:1.2', nodeIds: ['n-c4'], nodeSelector: { accelerator: 'nvidia-a10' }, tolerations: ['nvidia.com/gpu=present:NoSchedule'], cpuRequestM: 8000, memRequestMi: 32768, sensitivity: 'confidential' }),
    w({ id: 'w-registry', applicationId: 'app-ml', name: 'model-registry', clusterId: 'cl-cloud', namespace: 'ml', kind: 'StatefulSet', image: 'mlflow:2.14', nodeIds: ['n-c3'], volumes: [{ name: 'artifacts', storageClass: 'gp3', sizeGb: 200, accessModes: ['ReadWriteOnce'], phase: 'Bound' }], ports: [5000], managedBy: 'helm', sensitivity: 'confidential' }),
    // regional
    w({ id: 'w-kafka', applicationId: 'app-ingest', name: 'kafka', clusterId: 'cl-region', namespace: 'streaming', kind: 'StatefulSet', image: 'bitnami/kafka:3.7', replicas: 3, readyReplicas: 3, nodeIds: ['n-r2', 'n-r3'], volumes: [{ name: 'data-kafka-0', storageClass: 'local-nvme', sizeGb: 500, accessModes: ['ReadWriteOnce'], phase: 'Bound', pinnedNodeIds: ['n-r2'] }, { name: 'data-kafka-1', storageClass: 'local-nvme', sizeGb: 500, accessModes: ['ReadWriteOnce'], phase: 'Bound', pinnedNodeIds: ['n-r3'] }], disruption: { maxUnavailable: '1', allowed: 1 }, ports: [9092], managedBy: 'helm' }),
    w({ id: 'w-agg', applicationId: 'app-ingest', name: 'stream-aggregator', clusterId: 'cl-region', namespace: 'streaming', image: 'ghcr.io/acme/aggregator:2.0', replicas: 2, readyReplicas: 2, nodeIds: ['n-r2', 'n-r3'], ports: [8883] }),
    w({ id: 'w-infer-r', applicationId: 'app-ml', name: 'inference-regional', clusterId: 'cl-region', namespace: 'ml', image: 'ghcr.io/acme/infer:1.2', nodeIds: ['n-r3'] }),
    // edge A
    w({
      id: 'w-mqtt-a', applicationId: 'app-ingest', name: 'mqtt-broker', clusterId: 'cl-edge-a', namespace: 'iot', image: 'eclipse-mosquitto:2', nodeIds: ['n-a1'], volumes: [{ name: 'mosquitto-data', storageClass: 'local-path', sizeGb: 1, accessModes: ['ReadWriteOnce'], phase: 'Bound', pinnedNodeIds: ['n-a1'] }],
      readyReplicas: 1, exposure: 'node-port', ports: [1883, 8883], managedBy: 'helm', imageDigest: 'sha256:4f1c9a7be2d84c36a0b5e79d1f2a63c8', sensitivity: 'internal',
    }),
    w({
      id: 'w-infer-a', applicationId: 'app-ml', name: 'inference-edge', clusterId: 'cl-edge-a', namespace: 'ml', image: 'ghcr.io/acme/infer-lite:1.2', nodeIds: ['n-a2'],
      readyReplicas: 1, managedBy: 'argo', nodeSelector: { accelerator: 'jetson-orin' }, tolerations: ['edge=true:NoSchedule'], cpuRequestM: 2000, memRequestMi: 2048, cpuLimitM: 3000, memLimitMi: 3072,
    }),
    w({ id: 'w-sensor-a', applicationId: 'app-ingest', name: 'sensor-ingest', clusterId: 'cl-edge-a', namespace: 'iot', kind: 'DaemonSet', image: 'ghcr.io/acme/ingest:0.9', replicas: 2, readyReplicas: 2, nodeIds: ['n-a1', 'n-a2'], managedBy: 'helm' }),
    // edge B
    w({ id: 'w-mqtt-b', applicationId: 'app-ingest', name: 'mqtt-broker', clusterId: 'cl-edge-b', namespace: 'iot', image: 'eclipse-mosquitto:2', nodeIds: ['n-b1'], readyReplicas: 1, exposure: 'node-port', ports: [1883, 8883] }),
    w({ id: 'w-sensor-b', applicationId: 'app-ingest', name: 'sensor-ingest', clusterId: 'cl-edge-b', namespace: 'iot', kind: 'DaemonSet', image: 'ghcr.io/acme/ingest:0.9', replicas: 2, readyReplicas: 1, restarts: 14, nodeIds: ['n-b1', 'n-b2'], status: 'degraded' }),
    w({ id: 'w-infer-b', applicationId: 'app-ml', name: 'inference-edge', clusterId: 'cl-edge-b', namespace: 'ml', image: 'ghcr.io/acme/infer-lite:1.2', nodeIds: ['n-b2'], status: 'degraded', readyReplicas: 0, restarts: 9, nodeSelector: { 'kubernetes.io/arch': 'arm64' } }),
  ]

  // Namespaces: one per (cluster, namespace) in use. The namespace → application mapping is the default grouping rule.
  const nsApp: Record<string, string> = { platform: 'app-api', ml: 'app-ml', streaming: 'app-ingest', iot: 'app-ingest' }
  const namespaces: Namespace[] = [...new Map(services.map((s) => [`${s.clusterId}/${s.namespace}`, s])).values()].map((s) => ({
    id: `ns-${s.clusterId}-${s.namespace}`,
    orgId: DEFAULT_ORG,
    clusterId: s.clusterId,
    name: s.namespace,
    labels: {},
    applicationId: nsApp[s.namespace],
    source: 'manual',
  }))

  const devices: Device[] = [
    dev({
      id: 'dev-temp-a', name: 'Temperature sensors', kind: 'sensor', count: 48, applicationId: 'app-ingest', siteId: 'site-pat', gatewayNodeId: 'n-a1', protocol: 'MQTT', connectivity: 'lora', hardwareModel: 'LoRa temperature node',
      source: 'discovered', lastSeen: SEEN, detectedAt: SEEN, agentId: 'ag-patras', revision: 42, key: 'mqtt-clients:edge-patras/temp-*',
      evidence: { kind: { signal: '48 MQTT client IDs match temp-*', confidence: 'medium', detail: 'Read from the broker; the units themselves are not scanned.' } },
    }),
    dev({ id: 'dev-cam-a', name: 'Line cameras', kind: 'camera', count: 6, applicationId: 'app-ml', siteId: 'site-pat', gatewayNodeId: 'n-a2', protocol: 'RTSP', connectivity: 'ethernet', hardwareModel: 'IP camera, 1080p' }),
    dev({ id: 'dev-act-a', name: 'Valve actuators', kind: 'actuator', count: 12, applicationId: 'app-ml', siteId: 'site-pat', protocol: 'MQTT', connectivity: 'zigbee' }),
    dev({ id: 'dev-vib-b', name: 'Vibration sensors', kind: 'sensor', count: 24, applicationId: 'app-ingest', siteId: 'site-thess', protocol: 'MQTT', connectivity: 'wifi' }),
    dev({ id: 'dev-plc-b', name: 'Packaging PLCs', kind: 'plc', count: 2, applicationId: 'app-ingest', siteId: 'site-thess', gatewayNodeId: 'n-b1', protocol: 'OPC UA', connectivity: 'ethernet' }),
  ]

  const dependencies: Dependency[] = [
    d('d1', 'w-gw', 'w-orch', 'gRPC', 9000, ['declared'], 'medium'),
    d('d2', 'w-orch', 'w-registry', 'HTTP', 5000, ['declared'], 'medium'),
    d('d3', 'w-train', 'w-registry', 'HTTP', 5000),
    d('d4', 'w-agg', 'w-kafka', 'Kafka', 9092),
    d('d5', 'w-train', 'w-kafka', 'Kafka', 9092),
    d('d6', 'w-infer-r', 'w-registry', 'HTTP', 5000),
    d('d7', 'w-infer-a', 'w-registry', 'HTTP', 5000, ['declared'], 'medium'),
    d('d8', 'w-infer-b', 'w-registry', 'HTTP', 5000, ['declared'], 'medium'),
    d('d9', 'w-sensor-a', 'w-mqtt-a', 'MQTT', 1883, ['observed', 'declared'], 'high', { stats: { reqPerSec: 42, errorRate: 0.002, p95Ms: 8, windowSec: 300 } }),
    d('d10', 'w-infer-a', 'w-mqtt-a', 'MQTT', 1883, ['observed', 'declared']),
    d('d11', 'w-mqtt-a', 'w-agg', 'MQTT', 8883, ['observed'], 'high', { stats: { bytesPerSec: 180000, errorRate: 0, p95Ms: 31, windowSec: 300 } }),
    // edge-thessaloniki's agent has no access to dependencies yet, so these are declared, not observed
    d('d12', 'w-sensor-b', 'w-mqtt-b', 'MQTT', 1883, ['declared'], 'medium'),
    d('d13', 'w-infer-b', 'w-mqtt-b', 'MQTT', 1883, ['declared'], 'medium'),
    d('d14', 'w-mqtt-b', 'w-agg', 'MQTT', 8883),
    d('d15', 'w-orch', 'w-infer-a', 'gRPC', 9001),
    d('d16', 'w-orch', 'w-infer-b', 'gRPC', 9001),
    d('d17', 'w-orch', 'w-infer-r', 'gRPC', 9001),
    // devices talk to services (and services command devices)
    dd('d18', 'dev-temp-a', 'device', 'w-mqtt-a', 'service', 'MQTT', 1883, ['observed'], 'medium'),
    dd('d19', 'dev-cam-a', 'device', 'w-infer-a', 'service', 'RTSP', 8554),
    dd('d20', 'w-infer-a', 'service', 'dev-act-a', 'device', 'MQTT', 1883),
    dd('d21', 'dev-vib-b', 'device', 'w-mqtt-b', 'service', 'MQTT', 1883),
    dd('d22', 'dev-plc-b', 'device', 'w-sensor-b', 'service', 'OPC UA', 4840),
  ]

  const sites: Site[] = [
    site('site-fra', 'Frankfurt (eu-central-1)', 'cloud-region', 50.11, 8.68, 'DE', { city: 'Frankfurt' }),
    site('site-ath', 'Athens DC', 'data-center', 37.98, 23.73, 'GR', { city: 'Athens' }),
    site('site-pat', 'Patras edge site', 'edge-site', 38.25, 21.73, 'GR', { city: 'Patras', trustZone: 'restricted' }),
    site('site-thess', 'Thessaloniki edge site', 'edge-site', 40.64, 22.94, 'GR', { city: 'Thessaloniki', trustZone: 'restricted' }),
  ]

  const siteLinks: SiteLink[] = [
    link('sl1', 'site-fra', 'site-ath', 38, 0.1, 200),
    link('sl2', 'site-ath', 'site-pat', 9, 0.4, 100),
    link('sl3', 'site-ath', 'site-thess', 14, 0.3, 100),
  ]

  const applications: Application[] = [
    app('app-api', 'Platform API', 'Public entry point and orchestration.'),
    app('app-ml', 'ML platform', 'Training, registry and inference across the continuum.'),
    app('app-ingest', 'Sensor ingestion', 'Edge brokers, sensors and the streaming pipeline.', 'part-of'),
  ]

  const agents: Agent[] = [
    {
      id: 'ag-patras', orgId: DEFAULT_ORG, name: 'agent-edge-patras', clusterId: 'cl-edge-a', version: '0.1.0', accessTier: 3, status: 'approved',
      fingerprint: 'kube-system:0d5c8b1e-6f3a-4c0e-9b52-7a1f0e2d4c11', connectingIp: '203.0.113.24', certExpiresAt: '2026-09-20T08:00:00.000Z', lastHeartbeat: SEEN,
      modules: [
        { name: 'infrastructure', status: 'ok' },
        { name: 'services', status: 'ok' },
        { name: 'dependencies', status: 'ok' },
        { name: 'geo', status: 'ok' },
      ],
    },
    {
      id: 'ag-thess', orgId: DEFAULT_ORG, name: 'agent-edge-thessaloniki', clusterId: 'cl-edge-b', version: '0.1.0', accessTier: 2, status: 'approved',
      fingerprint: 'kube-system:9a4be7d0-31c8-4f5a-8e26-c5d0b7a93f02', connectingIp: '198.51.100.9', certExpiresAt: '2026-09-20T08:00:00.000Z', lastHeartbeat: SEEN,
      modules: [
        { name: 'infrastructure', status: 'ok' },
        { name: 'services', status: 'ok' },
        { name: 'dependencies', status: 'skipped', reason: 'Needs access tier 3 (dependencies)' },
        { name: 'geo', status: 'ok' },
      ],
    },
    {
      id: 'ag-volos', orgId: DEFAULT_ORG, name: 'agent-volos', version: '0.1.0', accessTier: 0, status: 'pending',
      fingerprint: 'kube-system:5e9a3c72-b8d1-47f0-a6c4-1d2e3f405968', connectingIp: '198.51.100.77', modules: [],
    },
  ]

  const suggestions: Suggestion[] = [
    {
      id: 'sg-door', orgId: DEFAULT_ORG, kind: 'device', agentId: 'ag-patras', createdAt: SEEN, status: 'open',
      title: 'Add “Door contacts” as a device group?',
      detail: '17 MQTT client IDs matching door-* connected to mqtt-broker on edge-patras in the last 24 h. They are not in the model yet.',
      apply: {
        type: 'add-device',
        device: dev({
          id: 'dev-door-a', name: 'Door contacts', kind: 'sensor', count: 17, applicationId: 'app-ingest', siteId: 'site-pat', protocol: 'MQTT', connectivity: 'zigbee',
          source: 'discovered', lastSeen: SEEN, detectedAt: SEEN, agentId: 'ag-patras', key: 'mqtt-clients:edge-patras/door-*',
          evidence: { kind: { signal: '17 MQTT client IDs match door-*', confidence: 'medium' } },
        }),
      },
    },
    {
      id: 'sg-weather', orgId: DEFAULT_ORG, kind: 'external-endpoint', agentId: 'ag-patras', createdAt: SEEN, status: 'open',
      title: 'orchestrator calls api.weather.example:443',
      detail: 'Seen on the wire 212 times in 24 h. No service or cluster in the model owns this address, so it would be added as an external endpoint.',
      apply: {
        type: 'add-external',
        endpoint: { id: 'ext-weather', orgId: DEFAULT_ORG, host: 'api.weather.example', port: 443, kind: 'saas', source: 'discovered', lastSeen: SEEN },
        dependency: dd('d-ext-weather', 'w-orch', 'service', 'ext-weather', 'external', 'HTTPS', 443, ['observed'], 'medium'),
      },
    },
    {
      id: 'sg-volos', orgId: DEFAULT_ORG, kind: 'site', agentId: 'ag-volos', createdAt: SEEN, status: 'open',
      title: 'Place agent-volos at Volos?',
      detail: 'GeoIP of the connecting address suggests Volos, GR (39.36, 22.94). City-level accuracy only. Approve the agent first; the site is applied with the cluster.',
    },
  ]

  const auditLog: AuditEvent[] = [
    { id: 'ev1', orgId: DEFAULT_ORG, at: '2026-09-18T09:12:00.000Z', actor: 'you', action: 'approve-agent', targetKind: 'agent', targetId: 'ag-patras', detail: 'Fingerprint confirmed' },
    { id: 'ev2', orgId: DEFAULT_ORG, at: '2026-09-18T09:40:00.000Z', actor: 'you', action: 'approve-agent', targetKind: 'agent', targetId: 'ag-thess', detail: 'Fingerprint confirmed, tier 2' },
  ]

  return {
    clusters,
    nodes,
    namespaces,
    services,
    devices,
    dependencies,
    applications,
    sites,
    siteLinks,
    externalEndpoints: [],
    agents,
    suggestions,
    auditLog,
    savedViews: [
      { id: 'view-where', orgId: DEFAULT_ORG, name: 'Where everything is', params: 'view=map', createdAt: SEEN },
      { id: 'view-tiers', orgId: DEFAULT_ORG, name: 'Machines by tier', params: 'view=infrastructure&group=tier', createdAt: SEEN },
    ],
    refs: {},
  }
}


/** True while the workspace still holds only the built-in example, so the UI can say so instead of passing it off as real. */
export function isSampleData(clusters: { id: string }[]): boolean {
  if (clusters.length === 0) return false
  const ids = new Set(seedTopology().clusters.map((c) => c.id))
  return clusters.every((c) => ids.has(c.id))
}
