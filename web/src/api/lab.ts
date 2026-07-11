import { api } from './client'
import type { Lab, Link, Node, NodeBGP, NodeBGPTable, NodeRoutes, NodeRuntime, Operation, Plan, ProfileInfo, TopologySpec } from '../types/models'

const base = '/api/v1'

// List endpoints return protobuf reply messages that wrap the items
// (e.g. { labs: [...] }); unwrap them here so stores keep plain arrays.
export const labApi = {
  list: () => api.get<{ labs: Lab[] }>(`${base}/labs`).then((r) => r.labs),
  get: (id: string) => api.get<Lab>(`${base}/labs/${id}`),
  create: (name: string, profile: string, topology?: TopologySpec) =>
    api.post<Lab>(`${base}/labs`, { name, profile, topology }),
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

  createPlan: (id: string) => api.post<Plan>(`${base}/labs/${id}/plans`),
  getPlan: (planId: string) => api.get<Plan>(`${base}/plans/${planId}`),
  applyPlan: (planId: string) => api.post<{ operationId: string }>(`${base}/plans/${planId}/apply`),

  getOperation: (opId: string) => api.get<Operation>(`${base}/operations/${opId}`),
  profiles: () => api.get<{ profiles: ProfileInfo[] }>(`${base}/profiles`).then((r) => r.profiles),
}
