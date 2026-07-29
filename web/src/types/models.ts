// Mirrors of the protobuf API (api/dcnetlab/v1/dcnetlab.proto) in its
// JSON mapping: int64 fields arrive as strings, unset message fields
// as null and unset scalars as '' / 0.

export type ResourcePhase =
  | 'Pending' | 'Planning' | 'Applying' | 'Running' | 'Degraded'
  | 'Failed' | 'Stopping' | 'Stopped' | 'Deleting' | 'Deleted'

export interface ResourceMeta {
  id: string
  name: string
  generation: string
  observedGeneration: string
  phase: ResourcePhase
  lastError?: { code: string; message: string; time: string } | null
  createdAt: string
  updatedAt: string
}

export type NodeRole = 'external' | 'dc-edge' | 'superspine' | 'spine' | 'leaf' | 'server'

export interface PodSpec {
  name: string
  spines: number
  racks: number
  serversPerRack: number
}

export interface TopologySpec {
  externalRouters: number
  dcEdges: number
  superSpines: number
  pods: PodSpec[]
  internetAccess?: boolean
}

export interface Lab {
  meta: ResourceMeta
  spec: {
    profile: string
    topology: TopologySpec
  }
  status: { nodeCount: number; linkCount: number }
}

export interface Node {
  meta: ResourceMeta
  spec: {
    labId: string
    role: NodeRole
    podId?: string
    rackId?: string
    asn?: number
    loopback?: string
    runtimeType: string
    // Leaf: MLAG pair member acting as VRRP gateway of the rack VLAN.
    mlagPeer?: string
    vlanId?: number
    vlanIp?: string
    gatewayIp?: string
    gatewayMac?: string
    vrrpGroup?: number
    vrrpPriority?: number
    // Server: bond0 address, virtual gateway and leaf physical peers.
    address?: string
    defaultGateway?: string
    bgpPeers?: string[]
  }
  status: {
    runtimeState: string
    routeCount: number
    bgpEstablished: number
    bgpConfigured: number
    interfacesUp?: number
    interfacesTotal?: number
    // Simulated interfaces only: topology link endpoints plus modeled
    // logical interfaces (leaf vlanif, server bond0).
    interfaces?: InterfaceStatus[]
    // VRRP gateway role of a leaf: Master / Backup / '' (no VRRP).
    vrrpState?: string
    // Node-agent reachability of a running server: Up / Down / ''.
    agentState?: string
    lastObserved?: string
  }
}

// missing is true when the kernel has no such interface at all (e.g.
// its veth pair was lost to a host Docker restart), as opposed to
// being present but administratively/operationally down — a fault
// injection never produces missing, only down.
export interface InterfaceStatus {
  name: string
  up: boolean
  missing?: boolean
}

// RuntimeInterface is one kernel interface of a node's container, as
// returned by the management view (GET .../nodes/{id}/runtime).
export interface RuntimeInterface {
  name: string
  state: string
  mac?: string
  addresses?: string[]
}

export interface NodeRuntime {
  containerState: string
  interfaces?: RuntimeInterface[]
}

// NodeRoutes is the live FRR routing table of a deployed node
// (GET .../nodes/{id}/routes); routes are empty unless running.
export interface RouteNexthop {
  via?: string
  interface?: string
  active?: boolean
}

export interface Route {
  prefix: string
  protocol: string
  // FIB only: 'local' marks a host entry (traffic addressed to the
  // device itself); empty means a forwarding (LPM) entry.
  kind?: string
  selected?: boolean
  distance?: number
  metric?: number
  nexthops?: RouteNexthop[]
}

export interface NodeRoutes {
  containerState: string
  routes?: Route[]
}

// NodeBGPTable is the live BGP Loc-RIB (GET .../nodes/{id}/bgp-table):
// every candidate path per prefix, before best-path selection.
export interface BGPPath {
  prefix: string
  best?: boolean
  multipath?: boolean
  valid?: boolean
  internal?: boolean
  asPath?: string
  origin?: string
  localPref?: number
  peer?: string
  nexthop?: string
  nexthopName?: string
}

export interface NodeBGPTable {
  containerState: string
  routerId?: string
  localAs?: number
  paths?: BGPPath[]
}

// NodeBGP is the node's BGP configuration as derived by the FRR
// compiler (GET .../nodes/{id}/bgp); empty for nodes without BGP.
export interface BGPNeighbor {
  address: string
  remoteAs: number
  description?: string
}

export interface NodeBGP {
  asn: number
  routerId?: string
  neighbors?: BGPNeighbor[]
  serverGroup?: { remoteAs: number; listenRange: string } | null
}

// Observation is one node's observed state as pushed on the topology
// WebSocket (/ws/v1/labs/{id}/topology).
export interface Observation {
  nodeId: string
  name: string
  phase: string
  runtimeState: string
  bgpEstablished: number
  bgpConfigured: number
  routeCount: number
  interfacesUp: number
  interfacesTotal: number
  interfaces?: InterfaceStatus[]
  vrrpState?: string
  agentState?: string
  lastObserved: string
}

export interface LinkEndpoint {
  nodeId: string
  nodeName: string
  interface: string
  address?: string
}

export type LinkKind = 'fabric' | 'server-access' | 'mlag-peer'

export interface Link {
  meta: ResourceMeta
  spec: {
    labId: string
    kind?: LinkKind
    vlanId?: number
    endpointA: LinkEndpoint
    endpointB: LinkEndpoint
    mtu: number
  }
  status: { adminUp: boolean; operUp: boolean }
}

// FaultTarget identifies what a fault acts on: a node (nodeId) or a
// link (linkId). side selects which endpoint an interface-down/
// impairment fault acts on ("a", "b" or "both"); link-down always
// acts on both regardless of side.
export interface FaultTarget {
  kind: 'node' | 'link' | string
  nodeId?: string
  nodeName?: string
  linkId?: string
  linkName?: string
  side?: 'a' | 'b' | 'both' | string
}

// FaultImpairment shapes egress traffic with one netem qdisc; unset
// fields are omitted, so at least one must be set.
export interface FaultImpairment {
  delayMs?: number
  jitterMs?: number
  lossPercent?: number
  rateKbit?: number
}

// FaultScenario injects and recovers a controlled failure against a
// node or a link. node-restart is a point-in-time event: applied
// stays false even right after a successful apply.
export interface FaultScenario {
  meta: ResourceMeta
  spec: {
    labId: string
    target: FaultTarget
    type: 'node-stop' | 'node-restart' | 'link-down' | 'interface-down' | 'impairment' | string
    impairment?: FaultImpairment
  }
  status: {
    applied: boolean
    appliedAt?: string
    lastError?: string
  }
}

export interface PlanOperation {
  type: string
  target: string
  summary: string
}

export interface Plan {
  id: string
  labId: string
  baseGeneration: string
  newGeneration: string
  state: 'Pending' | 'Applied' | 'Expired'
  operations: PlanOperation[]
  allocations: { pool: string; value: string; owner: string }[]
  warnings: { code: string; message: string }[] | null
  createdAt: string
}

export interface OperationStep {
  name: string
  state: string
  message?: string
  startedAt?: string
  finishedAt?: string
}

export interface Operation {
  id: string
  labId: string
  type: string
  resource: { type: string; id: string }
  state: 'Queued' | 'Running' | 'Succeeded' | 'Failed' | 'Cancelled'
  progress: number
  steps: OperationStep[]
  error?: { code: string; message: string } | null
  createdAt: string
  updatedAt: string
}

export interface ProfileInfo {
  name: string
  topology: TopologySpec
}

// Package is one versioned program artifact (tar.gz + manifest) in
// the controller's repository; the bundled trafficgen is the builtin.
export interface Package {
  meta: ResourceMeta
  version: string
  format: string
  entrypoint: string
  description?: string
  sha256: string
  // int64 fields are strings in protobuf JSON.
  sizeBytes?: string
  builtin?: boolean
}

// ServerInstallResult is the per-server outcome of delivering a
// package onto lab servers; error is empty on success.
export interface ServerInstallResult {
  serverId: string
  serverName: string
  error?: string
}

// NodeInventory is what is actually on one server, live from its
// agent; managed=false marks node-local programs created via the
// in-container CLI.
export interface NodeInventory {
  packages?: { name: string; version: string; sha256: string }[]
  programs?: {
    name: string
    packageName: string
    packageVersion: string
    entrypoint?: string
    args?: string[]
    restartPolicy: string
    type?: string
    autoStart?: boolean
    state: string
    pid?: number
    restarts?: number
    lastError?: string
    managed?: boolean
  }[]
}

// NodeMetrics is one resource-usage sample of a server, live from its
// agent (node-exporter style). CPU, memory, disk and process count
// are container-scoped, network is netns-scoped; the load average is
// the shared host kernel's. int64 fields are strings in protobuf
// JSON; doubles stay numbers.
export interface NodeMetrics {
  sampledAt?: string
  uptimeSeconds?: string
  procs?: string
  cpu?: {
    usagePercent?: number
    userPercent?: number
    systemPercent?: number
    limitCores?: number
  }
  memory?: {
    usedBytes?: string
    cacheBytes?: string
    limitBytes?: string
    swapUsedBytes?: string
  }
  load?: { load1?: number; load5?: number; load15?: number }
  filesystem?: {
    mount?: string
    sizeBytes?: string
    usedBytes?: string
    availBytes?: string
  }
  disk?: {
    readBytesPerSec?: number
    writeBytesPerSec?: number
    readOpsPerSec?: number
    writeOpsPerSec?: number
    readBytesTotal?: string
    writeBytesTotal?: string
  }
  interfaces?: {
    name: string
    rxBytesPerSec?: number
    txBytesPerSec?: number
    rxPacketsPerSec?: number
    txPacketsPerSec?: number
    rxBytesTotal?: string
    txBytesTotal?: string
    rxErrors?: string
    txErrors?: string
    rxDropped?: string
    txDropped?: string
  }[]
}

// MetricsPoint is one collected sample of a server's resource series
// (15 s collector sweeps); rates are averages over the sweep interval.
// Shares the group shapes with NodeMetrics.
export interface MetricsPoint {
  ts?: string
  procs?: string
  cpu?: NodeMetrics['cpu']
  memory?: NodeMetrics['memory']
  load?: NodeMetrics['load']
  filesystem?: NodeMetrics['filesystem']
  disk?: NodeMetrics['disk']
  interfaces?: NodeMetrics['interfaces']
}

// TrafficgenMode is a subcommand of the builtin trafficgen package; the UI
// offers them as presets that become the first program argument.
export type TrafficgenMode =
  | 'http-server' | 'http-client' | 'tcp-server' | 'tcp-client' | 'udp-server' | 'udp-client'

// Program is a supervised process on a lab server, managed by the
// per-server dcnetlab-node-agent; it runs the entrypoint of an
// installed Package version.
export interface Program {
  meta: ResourceMeta
  spec: {
    labId: string
    serverId: string
    serverName: string
    packageName: string
    packageVersion: string
    entrypoint?: string
    args?: string[]
    restartPolicy: string
    // type follows systemd service semantics: simple (daemon) or
    // oneshot (runs to completion); autoStart mirrors systemctl
    // enable (start on every server boot and redeploy).
    type?: 'simple' | 'oneshot' | string
    autoStart?: boolean
    desiredState: string
    // livenessCheck, when set, restarts the program (per restartPolicy)
    // once it stops passing its probe.
    livenessCheck?: HealthCheck
    // readinessCheck, when set, reports whether the program is serving;
    // it never restarts, it only gates ready.
    readinessCheck?: HealthCheck
    // startupOrder sequences boot: programs start in ascending order.
    startupOrder?: number
  }
  status: {
    state: 'Configured' | 'Running' | 'Stopped' | 'Failed' | 'Exited' | 'Unknown' | string
    pid?: number
    restarts?: number
    lastError?: string
    // health is the last liveness verdict: Unknown, Healthy or Unhealthy.
    health?: 'Unknown' | 'Healthy' | 'Unhealthy' | string
    // ready reflects the readiness check.
    ready?: boolean
    startedAt?: string
    lastObserved?: string
  }
}

// ProgramOpResult is the per-program outcome of a batch start/stop/
// delete; error is empty on success.
export interface ProgramOpResult {
  id: string
  name: string
  error?: string
}

// HealthCheck probes a running program periodically. type is process
// (the supervised process exists), tcp (a local port accepts a
// connection) or http (a GET returns 2xx/3xx); the probe targets
// loopback inside the server. port/path apply to tcp/http.
export interface HealthCheck {
  type: 'process' | 'tcp' | 'http' | string
  port?: number
  path?: string
  intervalSeconds?: number
  timeoutSeconds?: number
  failureThreshold?: number
}

// TrafficAssertion is one pass/fail threshold evaluated against the
// scenario's latest metrics window; metric is rate/successRate/p50/
// p95/p99, comparator is gte/lte.
export interface TrafficAssertion {
  metric: 'rate' | 'successRate' | 'p50' | 'p95' | 'p99' | string
  comparator: 'gte' | 'lte' | string
  threshold: number
}

export interface TrafficAssertionResult extends TrafficAssertion {
  value: number
  pass: boolean
}

// TrafficPoint is one collected sample: the client's windowed rate,
// success rate (percent) and latency percentiles (microseconds).
export interface TrafficPoint {
  ts?: string
  rate?: number
  successRate?: number
  p50Us?: string
  p95Us?: string
  p99Us?: string
}

// TrafficScenario drives measurable traffic between two lab servers
// through a pair of trafficgen Programs (server role on the
// destination, client role on the source).
export interface TrafficScenario {
  meta: ResourceMeta
  spec: {
    labId: string
    sourceServerId: string
    sourceServerName: string
    destServerId: string
    destServerName: string
    protocol: 'http' | 'tcp' | 'udp' | string
    port?: number
    rate: number
    concurrency: number
    payloadBytes?: number
    durationSeconds?: number
    assertions?: TrafficAssertion[]
  }
  status: {
    phase: 'Stopped' | 'Running' | 'Failed' | string
    serverProgramId?: string
    serverProgramName?: string
    clientProgramId?: string
    clientProgramName?: string
    startedAt?: string
    lastPoint?: TrafficPoint
    assertions?: TrafficAssertionResult[]
    lastObserved?: string
    lastError?: string
  }
}

// --- Capture ---

export interface CaptureFilter {
  protocol?: string
  srcPrefix?: string
  dstPrefix?: string
  port?: number
}

// CaptureSession records packets on one modelled interface of a lab
// node into a pcapng file. int64 counters arrive as strings on the
// protobuf JSON wire.
export interface CaptureSession {
  meta: ResourceMeta
  spec: {
    labId: string
    nodeId: string
    nodeName?: string
    interface: string
    direction?: 'both' | 'rx' | 'tx' | string
    snapLength?: number
    durationSeconds?: number
    maxPackets?: string
    maxBytes?: string
    filter?: CaptureFilter
  }
  status: {
    state: 'Running' | 'Completed' | 'Stopped' | 'Failed' | string
    packets?: string
    bytes?: string
    startedAt?: string
    endedAt?: string
    lastError?: string
  }
}

// CapturePacketRow is a normalized packet list row. REST rows carry
// string indices (protobuf int64 JSON) while WebSocket rows carry
// numbers; normalizeCapturePacket folds both into this shape.
export interface CapturePacketRow {
  index: number
  ts?: string
  direction?: string
  captureLength?: number
  wireLength?: number
  source?: string
  destination?: string
  sourcePort?: number
  destinationPort?: number
  protocol?: string
  info?: string
}

// offset/length locate the field's bytes in the raw frame (for the hex
// pane highlight sync); length 0 means the field has no fixed range.
// children nest sub-fields (e.g. each path attribute under the BGP
// UPDATE "Path Attributes" line).
export interface CapturePacketField {
  name: string
  value: string
  offset?: number
  length?: number
  children?: CapturePacketField[]
}

export interface CapturePacketLayer {
  name: string
  fields?: CapturePacketField[]
  offset?: number
  length?: number
}

export interface CapturePacketDetail {
  packet?: Record<string, unknown>
  layers?: CapturePacketLayer[]
  // Snapped raw bytes, base64-encoded (protobuf bytes JSON mapping).
  data?: string
}

// CaptureWsEvent is one frame on /ws/v1/labs/{labId}/captures/{id}:
// a packets batch (the first one is the window snapshot) or the end
// notice. Encoded by the Go backend with encoding/json, so integers
// are numbers here, not strings.
export interface CaptureWsEvent {
  type: 'packets' | 'end' | string
  packets?: {
    index: number
    ts?: string
    direction?: string
    captureLength?: number
    wireLength?: number
    source?: string
    destination?: string
    sourcePort?: number
    destinationPort?: number
    protocol?: string
    info?: string
  }[]
  total?: number
  firstAvailable?: number
  state?: string
}
