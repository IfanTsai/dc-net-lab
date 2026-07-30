// Shared node colouring: the topology canvas paints these on the
// device icons, and the detail drawer reuses them for its header
// chips so both views tell the same story.
import type { Node } from '../types/models'

export const roleColor: Record<string, string> = {
  external: '#909399', 'dc-edge': '#7952b3', superspine: '#409eff',
  spine: '#337ecc', leaf: '#67c23a', server: '#e6a23c',
}

// Observed-state badge colours drawn onto the icon's top-right corner.
export const badgeColor: Record<string, string> = {
  ok: '#67c23a', // running, all BGP sessions established
  warn: '#e6a23c', // running but BGP not fully converged
  agent: '#9c6ade', // server only: running but the node-agent is unreachable
  stopped: '#909399',
  failed: '#f56c6c',
}

// nodeBadge derives the badge from the node's observed state; nodes
// never observed (noop runtime, not yet swept) carry no badge.
export function nodeBadge(n: Node): string {
  const s = n.status
  if (!s?.lastObserved) return ''
  if (s.runtimeState === 'Stopped') return 'stopped'
  if (s.runtimeState !== 'Running') return 'failed'
  if (s.bgpConfigured > 0 && s.bgpEstablished < s.bgpConfigured) return 'warn'
  // A running server whose node-agent is unreachable cannot run
  // programs; the dataplane is fine, so it gets its own badge
  // (management plane lost) instead of the network warn.
  if (s.agentState === 'Down') return 'agent'
  return 'ok'
}
