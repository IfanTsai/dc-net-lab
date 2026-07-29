import type { Link, Node } from '../types/models'

export interface CaptureInterfaceOption {
  value: string
  label: string
}

// nodeCaptureInterfaces lists a node's capture targets, mirroring the
// backend's simulation-view scope: its topology link endpoints (with
// the far end in the label) plus the modelled logical interfaces —
// the gateway vlanif on leaves, bond0 on servers. Implementation
// plumbing (eth0, br0, VRRP macvlan) is deliberately absent.
export function nodeCaptureInterfaces(node: Node, links: Link[]): CaptureInterfaceOption[] {
  const out: CaptureInterfaceOption[] = []
  for (const l of links) {
    for (const [ep, far] of [
      [l.spec.endpointA, l.spec.endpointB],
      [l.spec.endpointB, l.spec.endpointA],
    ] as const) {
      if (ep.nodeId === node.meta.id) {
        out.push({ value: ep.interface, label: `${ep.interface} → ${far.nodeName}:${far.interface}` })
      }
    }
  }

  out.sort((a, b) => a.value.localeCompare(b.value, undefined, { numeric: true }))

  if (node.spec.vlanId) {
    out.push({ value: `vlan${node.spec.vlanId}`, label: `vlan${node.spec.vlanId} (SVI)` })
  } else if (node.spec.role === 'server' && node.spec.address) {
    out.push({ value: 'bond0', label: 'bond0' })
  }

  return out
}
