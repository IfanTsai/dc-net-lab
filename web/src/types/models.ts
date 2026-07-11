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
    lastObserved?: string
  }
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
