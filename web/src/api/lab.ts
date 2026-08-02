import { api } from './client'
import type { CaptureFilter, CapturePacketDetail, CaptureSession, FaultImpairment, FaultScenario, FaultTarget, HealthCheck, Lab, Link, MetricsPoint, MTRPathScan, Node, NodeBGP, NodeBGPTable, NodeInventory, NodeMetrics, NodeMTR, NodeRoutes, NodeRuntime, Operation, Package, Plan, ProfileInfo, Program, ProgramOpResult, ServerInstallResult, TopologySpec, TrafficAssertion, TrafficPoint, TrafficScenario } from '../types/models'

const base = '/api/v1'

function mtrParams(opts: { targetNodeId?: string; target?: string; protocol?: string; port?: number; samples?: number; cycles?: number }): string {
  const params = new URLSearchParams()
  if (opts.targetNodeId) params.set('targetNodeId', opts.targetNodeId)
  if (opts.target) params.set('target', opts.target)
  if (opts.protocol) params.set('protocol', opts.protocol)
  if (opts.port) params.set('port', String(opts.port))
  if (opts.samples) params.set('samples', String(opts.samples))
  if (opts.cycles) params.set('cycles', String(opts.cycles))

  return params.toString()
}

// List endpoints return protobuf reply messages that wrap the items
// (e.g. { labs: [...] }); unwrap them here so stores keep plain arrays.
export const labApi = {
  list: () => api.get<{ labs: Lab[] }>(`${base}/labs`).then((r) => r.labs),
  get: (id: string) => api.get<Lab>(`${base}/labs/${id}`),
  create: (name: string, profile: string, internetAccess = false, topology?: TopologySpec) =>
    api.post<Lab>(`${base}/labs`, { name, profile, internetAccess, topology }),
  remove: (id: string) => api.del<{ operationId: string }>(`${base}/labs/${id}`),

  nodes: (id: string) => api.get<{ nodes: Node[] }>(`${base}/labs/${id}/nodes`).then((r) => r.nodes),
  links: (id: string) => api.get<{ links: Link[] }>(`${base}/labs/${id}/links`).then((r) => r.links),
  operations: (id: string) =>
    api.get<{ operations: Operation[] }>(`${base}/labs/${id}/operations`).then((r) => r.operations),
  generations: (id: string) =>
    // int64 fields are strings in protobuf JSON.
    api.get<{ generations: string[] }>(`${base}/labs/${id}/generations`).then((r) => r.generations),

  start: (id: string) => api.post<{ operationId: string }>(`${base}/labs/${id}/start`),
  stop: (id: string) => api.post<{ operationId: string }>(`${base}/labs/${id}/stop`),
  repair: (id: string) => api.post<{ operationId: string }>(`${base}/labs/${id}/repair`),
  startNode: (labId: string, nodeId: string) =>
    api.post<Node>(`${base}/labs/${labId}/nodes/${nodeId}/start`),
  stopNode: (labId: string, nodeId: string) =>
    api.post<Node>(`${base}/labs/${labId}/nodes/${nodeId}/stop`),
  nodeRuntime: (labId: string, nodeId: string) =>
    api.get<NodeRuntime>(`${base}/labs/${labId}/nodes/${nodeId}/runtime`),
  nodeBGP: (labId: string, nodeId: string) =>
    api.get<NodeBGP>(`${base}/labs/${labId}/nodes/${nodeId}/bgp`),
  nodeRoutes: (labId: string, nodeId: string) =>
    api.get<NodeRoutes>(`${base}/labs/${labId}/nodes/${nodeId}/routes`),
  nodeBGPTable: (labId: string, nodeId: string) =>
    api.get<NodeBGPTable>(`${base}/labs/${labId}/nodes/${nodeId}/bgp-table`),
  nodeFIB: (labId: string, nodeId: string) =>
    api.get<NodeRoutes>(`${base}/labs/${labId}/nodes/${nodeId}/fib`),
  nodeMTR: (labId: string, nodeId: string, opts: { targetNodeId?: string; target?: string; protocol?: string; port?: number; cycles?: number }) =>
    api.get<NodeMTR>(`${base}/labs/${labId}/nodes/${nodeId}/mtr?${mtrParams(opts)}`),
  nodeMTRScan: (labId: string, nodeId: string, opts: { targetNodeId?: string; target?: string; protocol?: string; port?: number; samples?: number; cycles?: number }) =>
    api.get<MTRPathScan>(`${base}/labs/${labId}/nodes/${nodeId}/mtr/scan?${mtrParams(opts)}`),

  packages: () => api.get<{ packages: Package[] }>(`${base}/packages`).then((r) => r.packages),
  uploadPackage: (payloadBase64: string) =>
    api.post<Package>(`${base}/packages`, { payload: payloadBase64 }),
  deletePackage: (name: string, version: string) =>
    api.del<Record<string, never>>(`${base}/packages/${name}/${version}`),
  installPackage: (labId: string, name: string, version: string, serverIds: string[]) =>
    api.post<{ results: ServerInstallResult[] }>(
      `${base}/labs/${labId}/packages/${name}/${version}/install`,
      { serverIds },
    ).then((r) => r.results ?? []),

  programs: (labId: string) =>
    api.get<{ programs: Program[] }>(`${base}/labs/${labId}/programs`).then((r) => r.programs),
  createProgram: (labId: string, body: { name: string; serverIds: string[]; packageName: string; packageVersion: string; args?: string[]; type?: string; autoStart?: boolean; restartPolicy: string; livenessCheck?: HealthCheck; readinessCheck?: HealthCheck; startupOrder?: number; start?: boolean }) =>
    api.post<{ programs?: Program[]; failures?: ServerInstallResult[] }>(`${base}/labs/${labId}/programs`, body)
      .then((r) => ({ programs: r.programs ?? [], failures: r.failures ?? [] })),
  batchProgramOp: (labId: string, op: 'start' | 'stop' | 'delete', ids: string[]) =>
    api.post<{ results?: ProgramOpResult[] }>(`${base}/labs/${labId}/programs/batch`, { op, ids })
      .then((r) => r.results ?? []),
  nodeInventory: (labId: string, nodeId: string) =>
    api.get<NodeInventory>(`${base}/labs/${labId}/nodes/${nodeId}/inventory`),
  nodeMetrics: (labId: string, nodeId: string) =>
    api.get<NodeMetrics>(`${base}/labs/${labId}/nodes/${nodeId}/metrics`),
  nodeMetricsHistory: (labId: string, nodeId: string, startSec: number, endSec: number) =>
    api.get<{ points?: MetricsPoint[] }>(
      `${base}/labs/${labId}/nodes/${nodeId}/metrics/history?start=${startSec}&end=${endSec}`,
    ).then((r) => r.points ?? []),
  startProgram: (labId: string, id: string) =>
    api.post<Program>(`${base}/labs/${labId}/programs/${id}/start`),
  stopProgram: (labId: string, id: string) =>
    api.post<Program>(`${base}/labs/${labId}/programs/${id}/stop`),
  upgradeProgram: (labId: string, id: string, version: string) =>
    api.post<Program>(`${base}/labs/${labId}/programs/${id}/upgrade`, { version }),
  deleteProgram: (labId: string, id: string) =>
    api.del<Record<string, never>>(`${base}/labs/${labId}/programs/${id}`),
  programLogs: (labId: string, id: string, tail = 200) =>
    api.get<{ content: string }>(`${base}/labs/${labId}/programs/${id}/logs?tail=${tail}`).then((r) => r.content),

  trafficScenarios: (labId: string) =>
    api.get<{ scenarios: TrafficScenario[] }>(`${base}/labs/${labId}/traffic-scenarios`).then((r) => r.scenarios ?? []),
  createTrafficScenario: (labId: string, body: { name: string; sourceServerId: string; destServerId: string; protocol: string; port?: number; rate: number; concurrency: number; payloadBytes?: number; durationSeconds?: number; assertions?: TrafficAssertion[] }) =>
    api.post<TrafficScenario>(`${base}/labs/${labId}/traffic-scenarios`, body),
  startTrafficScenario: (labId: string, id: string) =>
    api.post<TrafficScenario>(`${base}/labs/${labId}/traffic-scenarios/${id}/start`),
  stopTrafficScenario: (labId: string, id: string) =>
    api.post<TrafficScenario>(`${base}/labs/${labId}/traffic-scenarios/${id}/stop`),
  deleteTrafficScenario: (labId: string, id: string) =>
    api.del<Record<string, never>>(`${base}/labs/${labId}/traffic-scenarios/${id}`),
  trafficScenarioHistory: (labId: string, id: string, startSec: number, endSec: number) =>
    api.get<{ points?: TrafficPoint[] }>(
      `${base}/labs/${labId}/traffic-scenarios/${id}/history?start=${startSec}&end=${endSec}`,
    ).then((r) => r.points ?? []),

  faultScenarios: (labId: string) =>
    api.get<{ scenarios: FaultScenario[] }>(`${base}/labs/${labId}/fault-scenarios`).then((r) => r.scenarios ?? []),
  createFaultScenario: (labId: string, body: { name: string; target: FaultTarget; type: string; impairment?: FaultImpairment }) =>
    api.post<FaultScenario>(`${base}/labs/${labId}/fault-scenarios`, body),
  applyFaultScenario: (labId: string, id: string) =>
    api.post<FaultScenario>(`${base}/labs/${labId}/fault-scenarios/${id}/apply`),
  recoverFaultScenario: (labId: string, id: string) =>
    api.post<FaultScenario>(`${base}/labs/${labId}/fault-scenarios/${id}/recover`),
  deleteFaultScenario: (labId: string, id: string) =>
    api.del<Record<string, never>>(`${base}/labs/${labId}/fault-scenarios/${id}`),

  captureSessions: (labId: string) =>
    api.get<{ sessions: CaptureSession[] }>(`${base}/labs/${labId}/captures`).then((r) => r.sessions ?? []),
  createCaptureSession: (labId: string, body: { name: string; nodeId: string; interface: string; direction?: string; snapLength?: number; durationSeconds?: number; maxPackets?: number; maxBytes?: number; filter?: CaptureFilter }) =>
    api.post<CaptureSession>(`${base}/labs/${labId}/captures`, body),
  getCaptureSession: (labId: string, id: string) =>
    api.get<CaptureSession>(`${base}/labs/${labId}/captures/${id}`),
  stopCaptureSession: (labId: string, id: string) =>
    api.post<CaptureSession>(`${base}/labs/${labId}/captures/${id}/stop`),
  deleteCaptureSession: (labId: string, id: string) =>
    api.del<Record<string, never>>(`${base}/labs/${labId}/captures/${id}`),
  capturePackets: (labId: string, id: string, offset: number, limit: number) =>
    api.get<{ packets?: Record<string, unknown>[]; total?: string; firstAvailable?: string }>(
      `${base}/labs/${labId}/captures/${id}/packets?offset=${offset}&limit=${limit}`,
    ),
  capturePacket: (labId: string, id: string, index: number) =>
    api.get<CapturePacketDetail>(`${base}/labs/${labId}/captures/${id}/packets/${index}`),
  // Plain HTTP download (Wireshark-ready pcapng), not a JSON endpoint.
  capturePcapUrl: (labId: string, id: string) => `${base}/labs/${labId}/captures/${id}/pcap`,

  createPlan: (id: string) => api.post<Plan>(`${base}/labs/${id}/plans`),
  getPlan: (planId: string) => api.get<Plan>(`${base}/plans/${planId}`),
  applyPlan: (planId: string) => api.post<{ operationId: string }>(`${base}/plans/${planId}/apply`),

  getOperation: (opId: string) => api.get<Operation>(`${base}/operations/${opId}`),
  profiles: () => api.get<{ profiles: ProfileInfo[] }>(`${base}/profiles`).then((r) => r.profiles),
}
