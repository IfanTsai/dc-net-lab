<script setup lang="ts">
// Renders the fabric with Cytoscape using a fixed tier layout:
// external → dc-edge → superspine → spine → leaf → server.
// Pure rendering: business actions live in the page, not here.
import { onBeforeUnmount, onMounted, ref, watch } from 'vue'
import cytoscape, { type Core } from 'cytoscape'
import type { Link, Node } from '../types/models'

const props = defineProps<{ nodes: Node[]; links: Link[] }>()
const emit = defineEmits<{
  selectNode: [node: Node]
  selectLink: [link: Link]
  selectNone: []
  openTerminal: [node: Node]
}>()

const container = ref<HTMLElement | null>(null)
let cy: Core | null = null

const tierOrder: Record<string, number> = {
  external: 0, 'dc-edge': 1, superspine: 2, spine: 3, leaf: 4, server: 5,
}
const roleColor: Record<string, string> = {
  external: '#909399', 'dc-edge': '#7952b3', superspine: '#409eff',
  spine: '#337ecc', leaf: '#67c23a', server: '#e6a23c',
}

// Role glyphs (48x48 viewBox, white line art on the role colour):
// external = cloud, dc-edge = router (circle + arrows), superspine =
// core layers, spine = switch arrows, leaf = ToR with downlinks,
// server = rack chassis.
const roleGlyph: Record<string, string> = {
  external:
    '<path d="M18 10h-1.26A8 8 0 1 0 9 20h9a5 5 0 0 0 0-10z" transform="translate(6 6) scale(1.5)"/>',
  'dc-edge':
    '<circle cx="24" cy="24" r="12"/>' +
    '<path d="M18 20h9m-3.2-3.2L27 20l-3.2 3.2"/>' +
    '<path d="M30 28h-9m3.2-3.2L21 28l3.2 3.2"/>',
  superspine:
    '<g transform="translate(6 6) scale(1.5)">' +
    '<path d="M12 2 2 7l10 5 10-5-10-5z"/>' +
    '<path d="M2 12l10 5 10-5"/><path d="M2 17l10 5 10-5"/></g>',
  spine:
    '<path d="M15 19h15m-4.5-4.5L30 19l-4.5 4.5"/>' +
    '<path d="M33 29H18m4.5-4.5L18 29l4.5 4.5"/>',
  leaf:
    '<rect x="14" y="15" width="20" height="9" rx="2"/>' +
    '<path d="M18 24v9M24 24v9M30 24v9"/>',
  server:
    '<rect x="14" y="13" width="20" height="9" rx="2"/>' +
    '<rect x="14" y="26" width="20" height="9" rx="2"/>' +
    '<path d="M18 17.5h.01M18 30.5h.01" stroke-width="3"/>',
}

// Observed-state badge colours drawn onto the icon's top-right corner.
const badgeColor: Record<string, string> = {
  ok: '#67c23a', // running, all BGP sessions established
  warn: '#e6a23c', // running but BGP not fully converged
  stopped: '#909399',
  failed: '#f56c6c',
}

function roleIcon(role: string, badge = ''): string {
  // Explicit width/height matter: without them the browser rasterises
  // the SVG at its default intrinsic size (300x150) and the icon ends
  // up scaled and offset inside the node.
  const dot = badge
    ? `<circle cx="40" cy="8" r="6.5" fill="${badgeColor[badge]}" stroke="#fff" stroke-width="2"/>`
    : ''
  const svg =
    `<svg xmlns="http://www.w3.org/2000/svg" width="48" height="48" viewBox="0 0 48 48">` +
    `<rect width="48" height="48" rx="10" fill="${roleColor[role] ?? '#999'}"/>` +
    `<g fill="none" stroke="#fff" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round">` +
    (roleGlyph[role] ?? roleGlyph.server) +
    `</g>${dot}</svg>`
  return `data:image/svg+xml;utf8,${encodeURIComponent(svg)}`
}

// Icons are cached per role and badge variant.
const iconCache = new Map<string, string>()
function iconFor(role: string, badge: string): string {
  const key = `${role}:${badge}`
  let icon = iconCache.get(key)
  if (!icon) {
    icon = roleIcon(role, badge)
    iconCache.set(key, icon)
  }
  return icon
}

// nodeBadge derives the badge from the node's observed state; nodes
// never observed (noop runtime, not yet swept) carry no badge.
function nodeBadge(n: Node): string {
  const s = n.status
  if (!s?.lastObserved) return ''
  if (s.runtimeState === 'Stopped') return 'stopped'
  if (s.runtimeState !== 'Running') return 'failed'
  if (s.bgpConfigured > 0 && s.bgpEstablished < s.bgpConfigured) return 'warn'
  // A running server whose node-agent is unreachable cannot run
  // programs — surface it as degraded, not healthy.
  if (s.agentState === 'Down') return 'warn'
  return 'ok'
}

const roleIcons: Record<string, string> = Object.fromEntries(
  Object.keys(roleGlyph).map((r) => [r, roleIcon(r)]),
)

const legendRoles = [
  { role: 'external', label: 'External' },
  { role: 'dc-edge', label: 'DC Edge' },
  { role: 'superspine', label: 'SuperSpine' },
  { role: 'spine', label: 'Spine' },
  { role: 'leaf', label: 'Leaf' },
  { role: 'server', label: 'Server' },
]

// Pod and rack membership are shown as frames around the nodes, so
// their prefixes are stripped from the visible label (the resource
// name keeps them for uniqueness across pods/racks).
function displayName(n: Node): string {
  let name = n.meta.name
  for (const prefix of [n.spec.podId, n.spec.rackId]) {
    if (prefix && name.startsWith(prefix + '-')) name = name.slice(prefix.length + 1)
  }
  return name
}

// The compound parent a device belongs to: its rack frame (nested in
// the pod frame) or, for spines, the pod frame itself.
function parentID(n: Node): string | undefined {
  if (n.spec.rackId && n.spec.podId) return `rack:${n.spec.podId}/${n.spec.rackId}`
  if (n.spec.podId) return `pod:${n.spec.podId}`
  return undefined
}

function render() {
  if (!cy) return
  cy.elements().remove()

  // Compound parents: one frame per pod, one nested frame per rack
  // (the MLAG leaf pair plus its servers). Cytoscape auto-fits each
  // box around its children.
  const podIds = [...new Set(props.nodes.map((n) => n.spec.podId).filter(Boolean))] as string[]
  for (const pod of podIds.sort()) {
    cy.add({
      group: 'nodes',
      data: { id: `pod:${pod}`, label: pod, frame: 'pod' },
      selectable: false,
      grabbable: false,
    })
  }
  const rackKeys = [
    ...new Set(
      props.nodes
        .filter((n) => n.spec.rackId && n.spec.podId)
        .map((n) => `${n.spec.podId}/${n.spec.rackId}`),
    ),
  ]
  for (const key of rackKeys.sort()) {
    const [pod, rack] = key.split('/')
    cy.add({
      group: 'nodes',
      data: { id: `rack:${key}`, label: rack, frame: 'rack', parent: `pod:${pod}` },
      selectable: false,
      grabbable: false,
    })
  }

  // Position nodes tier by tier.
  const tiers = new Map<number, Node[]>()
  for (const n of props.nodes) {
    const t = tierOrder[n.spec.role] ?? 5
    if (!tiers.has(t)) tiers.set(t, [])
    tiers.get(t)!.push(n)
  }
  const width = container.value?.clientWidth ?? 1200

  const stopped = downNodeIDs()
  const ifaceStates = interfaceStates()

  for (const [tier, nodes] of tiers) {
    nodes.sort((a, b) => a.meta.name.localeCompare(b.meta.name))
    nodes.forEach((n, i) => {
      const el = cy!.add({
        group: 'nodes',
        data: {
          id: n.meta.id,
          label: displayName(n),
          role: n.spec.role,
          icon: iconFor(n.spec.role, nodeBadge(n)),
          ...(parentID(n) ? { parent: parentID(n) } : {}),
        },
        position: {
          x: ((i + 1) * width) / (nodes.length + 1),
          y: 80 + tier * 110,
        },
      })
      if (stopped.has(n.meta.id)) el.addClass('down')
    })
  }
  for (const l of props.links) {
    const el = cy.add({
      group: 'edges',
      data: {
        id: l.meta.id,
        source: l.spec.endpointA.nodeId,
        target: l.spec.endpointB.nodeId,
        kind: l.spec.kind ?? 'fabric',
      },
    })
    // A link with a powered-off endpoint or a down interface goes
    // grey; colour is reserved for future link-quality signalling.
    if (linkDead(l, stopped, ifaceStates)) el.addClass('dead')
  }
  cy.fit(undefined, 40)
}

// downNodeIDs collects devices that are off (stopped) or broken
// (container exited/missing), both rendered dimmed.
function downNodeIDs(): Set<string> {
  return new Set(
    props.nodes
      .filter((n) => n.meta.phase === 'Stopped' || n.meta.phase === 'Failed')
      .map((n) => n.meta.id),
  )
}

// interfaceStates indexes each node's observed per-interface up/down
// state (node id -> interface name -> up), as already collected by
// the observer and shown in the node drawer's interface table.
function interfaceStates(): Map<string, Map<string, boolean>> {
  const m = new Map<string, Map<string, boolean>>()
  for (const n of props.nodes) {
    if (n.status.interfaces) m.set(n.meta.id, new Map(n.status.interfaces.map((i) => [i.name, i.up])))
  }
  return m
}

// linkDead reports whether a link should render as broken: either
// endpoint's node is fully down, or either endpoint's own interface
// was observed administratively/operationally down — which is what a
// link-down/interface-down fault (ip link set down) produces, with no
// need for this component to know fault scenarios exist.
function linkDead(l: Link, stopped: Set<string>, ifaceStates: Map<string, Map<string, boolean>>): boolean {
  if (stopped.has(l.spec.endpointA.nodeId) || stopped.has(l.spec.endpointB.nodeId)) return true

  const aUp = ifaceStates.get(l.spec.endpointA.nodeId)?.get(l.spec.endpointA.interface)
  const bUp = ifaceStates.get(l.spec.endpointB.nodeId)?.get(l.spec.endpointB.interface)

  return aUp === false || bUp === false
}

// sameTopology reports whether the rendered elements match the props
// structurally, in which case observation updates are applied in
// place instead of re-rendering (which would reset pan/zoom and any
// dragged positions every sweep).
function sameTopology(): boolean {
  if (!cy) return false

  const rendered = cy.nodes('[icon]')
  if (rendered.length !== props.nodes.length || cy.edges().length !== props.links.length) {
    return false
  }
  return props.nodes.every((n) => cy!.getElementById(n.meta.id).length > 0)
}

// refreshState updates badges and up/down styling without touching
// layout.
function refreshState() {
  if (!cy) return

  const down = downNodeIDs()
  const ifaceStates = interfaceStates()
  for (const n of props.nodes) {
    const el = cy.getElementById(n.meta.id)
    el.data('icon', iconFor(n.spec.role, nodeBadge(n)))
    el.toggleClass('down', down.has(n.meta.id))
  }
  for (const l of props.links) {
    cy.getElementById(l.meta.id).toggleClass('dead', linkDead(l, down, ifaceStates))
  }
}

onMounted(() => {
  cy = cytoscape({
    container: container.value!,
    autoungrabify: false,
    style: [
      {
        // Only device nodes carry an icon; pod parents get their own
        // frame style below.
        selector: 'node[icon]',
        style: {
          label: 'data(label)',
          'background-opacity': 0,
          'background-image': 'data(icon)',
          // Force the image to exactly fill the node shape so the
          // selection border hugs the icon; corner radius matches the
          // icon's own rounded corners (10/48 of the node size).
          'background-fit': 'none',
          'background-width': '100%',
          'background-height': '100%',
          'background-position-x': '50%',
          'background-position-y': '50%',
          'background-clip': 'node',
          'corner-radius': '8px',
          'font-size': 11,
          'text-valign': 'bottom',
          'text-margin-y': 6,
          width: 38,
          height: 38,
          shape: 'round-rectangle',
        },
      },
      {
        // Pod frame: dashed rounded box, name on the top border. The
        // label gets a background chip so crossing edges cannot make
        // it unreadable.
        selector: ':parent',
        style: {
          label: 'data(label)',
          shape: 'round-rectangle',
          'background-color': '#409eff',
          'background-opacity': 0.04,
          'border-width': 1.5,
          'border-style': 'dashed',
          'border-color': '#a8abb2',
          'text-valign': 'top',
          'text-halign': 'center',
          'font-size': 12,
          'font-weight': 'bold',
          color: '#909399',
          'text-margin-y': -6,
          'text-background-color': '#ffffff',
          'text-background-opacity': 0.95,
          'text-background-shape': 'roundrectangle',
          'text-background-padding': '3px',
          padding: '28px',
        },
      },
      {
        // Rack frame (nested in the pod frame): the MLAG leaf pair
        // and its servers. Label at the bottom, away from the dense
        // spine-leaf edge crossings at the top of the box.
        selector: 'node[frame = "rack"]',
        style: {
          'background-color': '#67c23a',
          'background-opacity': 0.05,
          'border-style': 'solid',
          'border-color': '#c0c4cc',
          'text-valign': 'bottom',
          'text-margin-y': 6,
          padding: '20px',
        },
      },
      {
        // One consistent treatment for every link regardless of kind
        // (fabric, server access, MLAG peer): a near-black neutral
        // reads as plain wiring, leaving colour free for later
        // link-quality signalling. Not pure #000 — a soft graphite
        // is easier on the eyes across a dense topology.
        selector: 'edge',
        style: { width: 1, 'line-color': '#303133', 'curve-style': 'straight' },
      },
      {
        // Powered-off device: dimmed icon and label.
        selector: 'node.down',
        style: { opacity: 0.35 },
      },
      {
        // Link with a powered-off endpoint: grey out. Colour (e.g.
        // red/green) is reserved for future link-quality signalling.
        selector: 'edge.dead',
        style: { 'line-style': 'dotted', 'line-color': '#c0c4cc', opacity: 0.5 },
      },
      {
        // Selection accent: a calm blue rather than red, which reads
        // as an error/danger state elsewhere in the UI.
        selector: 'node:selected',
        style: { 'border-width': 3, 'border-color': '#409eff' },
      },
      { selector: 'edge:selected', style: { width: 2 } },
      {
        // A selected device's own links, so its place in the fabric
        // is legible at a glance instead of just the node itself.
        selector: 'edge.link-highlight',
        style: { width: 2, 'line-color': '#409eff' },
      },
    ],
  })
  // Selecting a device highlights its own links; cytoscape's default
  // single-selection mode fires 'unselect' on the previous node
  // before 'select' on the next, so the highlight always moves with
  // the border rather than accumulating.
  cy.on('select', 'node[icon]', (ev) => {
    ev.target.connectedEdges().addClass('link-highlight')
  })
  cy.on('unselect', 'node[icon]', (ev) => {
    ev.target.connectedEdges().removeClass('link-highlight')
  })
  // 'onetap' fires only after the double-click window has elapsed, so
  // opening a terminal via dbltap does not flash the detail drawer.
  cy.on('onetap', 'node', (ev) => {
    const n = props.nodes.find((x) => x.meta.id === ev.target.id())
    if (n) emit('selectNode', n)
  })
  cy.on('tap', 'edge', (ev) => {
    const l = props.links.find((x) => x.meta.id === ev.target.id())
    if (l) emit('selectLink', l)
  })
  // Tapping the canvas background clears the selection (and closes
  // the detail drawer).
  cy.on('tap', (ev) => {
    if (ev.target === cy) emit('selectNone')
  })
  // Double-click a device (not a pod/rack frame) to open its terminal.
  cy.on('dbltap', 'node[icon]', (ev) => {
    const n = props.nodes.find((x) => x.meta.id === ev.target.id())
    if (n) emit('openTerminal', n)
  })
  render()

  // Keep the graph fitted when the page (or drawer) resizes the canvas.
  resizeObserver = new ResizeObserver(() => {
    cy?.resize()
    cy?.fit(undefined, 40)
  })
  resizeObserver.observe(container.value!)
})

let resizeObserver: ResizeObserver | null = null

watch(
  () => [props.nodes, props.links],
  () => (sameTopology() ? refreshState() : render()),
  { deep: false },
)
onBeforeUnmount(() => {
  resizeObserver?.disconnect()
  cy?.destroy()
})
</script>

<template>
  <div class="wrapper">
    <div ref="container" class="canvas" />
    <div class="legend">
      <span v-for="r in legendRoles" :key="r.role" class="legend-item">
        <img :src="roleIcons[r.role]" :alt="r.role" />
        {{ r.label }}
      </span>
    </div>
  </div>
</template>

<style scoped>
.wrapper { position: relative; height: 100%; min-height: 320px; }
.canvas { width: 100%; height: 100%; border: 1px solid var(--el-border-color); border-radius: 4px; }
.legend {
  position: absolute;
  left: 12px;
  bottom: 12px;
  display: flex;
  gap: 14px;
  padding: 6px 12px;
  border: 1px solid var(--el-border-color);
  border-radius: 4px;
  background: color-mix(in srgb, var(--el-bg-color) 88%, transparent);
  font-size: 12px;
  color: var(--el-text-color-secondary);
}
.legend-item { display: inline-flex; align-items: center; gap: 5px; }
.legend-item img { width: 18px; height: 18px; }
</style>
