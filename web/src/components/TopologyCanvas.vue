<script setup lang="ts">
// Renders the fabric with Cytoscape using a fixed tier layout:
// external → dc-edge → superspine → spine → leaf → server.
// Pure rendering: business actions live in the page, not here.
import { onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { QuestionFilled } from '@element-plus/icons-vue'
import cytoscape, { type Core } from 'cytoscape'
import type { Link, Node } from '../types/models'
import { badgeColor, nodeBadge, roleColor } from '../utils/health'

const { t } = useI18n()

// DiagnosePath is one path a diagnostic probe (mtr) measured, drawn
// on top of the plain wiring so the operator sees the real path a
// probe took through the fabric. Several can be shown at once — an
// ECMP scan's distinct branches, or a "previous" run muted behind the
// current one for a before/after comparison — so colour and line
// style are supplied by the caller rather than baked into one CSS
// class the way the single-path version worked.
export interface DiagnosePath {
  id: string
  linkIds: string[]
  color: string
  dashed?: boolean
}

// GhostElements previews a pending scale draft directly on the
// canvas: planned devices render as dashed placeholders (inside
// planned pod/rack frames where needed) with schematic wiring, and
// devices about to be removed are tinted red. Purely visual — the
// authoritative names and diff come from the change plan.
export interface GhostElements {
  // Frames are planned pod/rack boxes; parent references another
  // frame id (a planned rack inside a planned pod).
  frames: { id: string; label: string; frame: 'pod' | 'rack'; parent?: string }[]
  // Nodes are planned devices; parent is a frame id — either a
  // planned one or an existing compound id (pod:<pod> / rack:<pod>/<rack>).
  nodes: { id: string; label: string; role: string; parent?: string }[]
  // Links wire planned devices to each other or to existing node ids.
  links: { id: string; source: string; target: string }[]
}

// ScaleMenuTarget identifies what was right-clicked for the scale
// context menu.
export interface ScaleMenuTarget {
  kind: 'background' | 'pod' | 'rack'
  podId?: string
  rackId?: string
}

const props = defineProps<{
  nodes: Node[]
  links: Link[]
  diagnosePaths?: DiagnosePath[]
  // diagnoseFocusNodeId highlights and pans to one specific node,
  // e.g. the device behind a clicked hop-table row — independent of
  // any path highlighting, since a hop can matter on its own even
  // when its neighbouring links didn't resolve to a path segment.
  diagnoseFocusNodeId?: string
  ghost?: GhostElements
  // removedNodeIds marks existing devices a pending scale draft would
  // delete; their frames and links are tinted with them.
  removedNodeIds?: string[]
}>()
const emit = defineEmits<{
  selectNode: [node: Node]
  selectLink: [link: Link]
  selectNone: []
  openTerminal: [node: Node]
  scaleMenu: [target: ScaleMenuTarget, pos: { x: number; y: number }]
}>()

const container = ref<HTMLElement | null>(null)
let cy: Core | null = null

const tierOrder: Record<string, number> = {
  external: 0, 'dc-edge': 1, superspine: 2, spine: 3, leaf: 4, server: 5,
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

const roleIcons: Record<string, string> = Object.fromEntries(
  Object.keys(roleGlyph).map((r) => [r, roleIcon(r)]),
)

// Legend entries for the observed-state badges; keys resolve to the
// short label and its hover tooltip in the topology i18n namespace.
const legendBadges = [
  { badge: 'ok', key: 'badgeOk' },
  { badge: 'warn', key: 'badgeWarn' },
  { badge: 'agent', key: 'badgeAgent' },
  { badge: 'stopped', key: 'badgeStopped' },
  { badge: 'failed', key: 'badgeFailed' },
]

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
  // box around its children, and grabbable lets the whole box (with
  // its children) be dragged as a unit — core compound-drag behaviour,
  // no extra handler needed.
  const podIds = [...new Set(props.nodes.map((n) => n.spec.podId).filter(Boolean))] as string[]
  for (const pod of podIds.sort()) {
    cy.add({
      group: 'nodes',
      data: { id: `pod:${pod}`, label: pod, frame: 'pod' },
      selectable: false,
      grabbable: true,
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
      grabbable: true,
    })
  }

  // Position nodes tier by tier. A tier's x-space is not shared
  // globally: pods get disjoint horizontal bands (and racks disjoint
  // sub-bands inside their pod), so a pod's spines always sit above
  // its own racks and the auto-fitted compound frames cannot overlap
  // even when pods have different rack counts. Global tiers
  // (external / dc-edge / superspine) spread over the full range.
  const stopped = downNodeIDs()
  const ifaceStates = interfaceStates()

  const byName = (a: Node, b: Node) =>
    a.meta.name.localeCompare(b.meta.name, undefined, { numeric: true })
  const tierY = (role: string) => 80 + (tierOrder[role] ?? 5) * 110
  const addNode = (n: Node, x: number, y: number) => {
    const el = cy!.add({
      group: 'nodes',
      data: {
        id: n.meta.id,
        label: displayName(n),
        role: n.spec.role,
        icon: iconFor(n.spec.role, nodeBadge(n)),
        ...(parentID(n) ? { parent: parentID(n) } : {}),
      },
      position: { x, y },
    })
    if (stopped.has(n.meta.id)) el.addClass('down')
  }

  const SLOT = 110 // width one device occupies in a row
  const RACK_GAP = 40
  const POD_GAP = 80

  const globals: Node[] = []
  const pods = new Map<string, { top: Node[]; racks: Map<string, { leaves: Node[]; servers: Node[] }> }>()
  for (const n of props.nodes) {
    const pod = n.spec.podId
    if (!pod) {
      globals.push(n)
      continue
    }

    if (!pods.has(pod)) pods.set(pod, { top: [], racks: new Map() })
    const p = pods.get(pod)!
    const rack = n.spec.rackId
    if (!rack) {
      p.top.push(n)
      continue
    }

    if (!p.racks.has(rack)) p.racks.set(rack, { leaves: [], servers: [] })
    const r = p.racks.get(rack)!
    ;(n.spec.role === 'leaf' ? r.leaves : r.servers).push(n)
  }

  const rackNum = (id: string) => Number(id.match(/(\d+)$/)?.[1] ?? 0)

  // Measure each pod's band, then lay pods out left to right.
  let x = 0
  interface RackBand { leaves: Node[]; servers: Node[]; width: number }
  for (const podId of [...pods.keys()].sort()) {
    const p = pods.get(podId)!
    const racks: RackBand[] = [...p.racks.keys()]
      .sort((a, b) => rackNum(a) - rackNum(b))
      .map((rid) => {
        const r = p.racks.get(rid)!
        return {
          leaves: r.leaves.sort(byName),
          servers: r.servers.sort(byName),
          width: Math.max(r.leaves.length, r.servers.length, 2) * SLOT,
        }
      })
    const racksWidth = racks.reduce((s, r) => s + r.width, 0) + RACK_GAP * Math.max(0, racks.length - 1)
    const podWidth = Math.max(p.top.length * SLOT, racksWidth, SLOT)

    p.top.sort(byName).forEach((n, i) => {
      addNode(n, x + ((i + 1) * podWidth) / (p.top.length + 1), tierY(n.spec.role))
    })
    let rx = x + (podWidth - racksWidth) / 2
    for (const r of racks) {
      r.leaves.forEach((n, j) => addNode(n, rx + ((j + 1) * r.width) / (r.leaves.length + 1), tierY('leaf')))
      r.servers.forEach((n, j) => addNode(n, rx + ((j + 1) * r.width) / (r.servers.length + 1), tierY(n.spec.role)))
      rx += r.width + RACK_GAP
    }

    x += podWidth + POD_GAP
  }

  const totalWidth = Math.max(x - POD_GAP, SLOT)
  const globalTiers = new Map<string, Node[]>()
  for (const n of globals) {
    if (!globalTiers.has(n.spec.role)) globalTiers.set(n.spec.role, [])
    globalTiers.get(n.spec.role)!.push(n)
  }

  for (const [role, nodes] of globalTiers) {
    nodes.sort(byName).forEach((n, i) => {
      addNode(n, ((i + 1) * totalWidth) / (nodes.length + 1), tierY(role))
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
  renderGhosts()
  applyRemovedMarks()
  applyDiagnosePaths()
  applyDiagnoseFocus(false)
  cy.fit(undefined, 40)
}

// renderGhosts draws the pending scale draft: planned frames and
// devices as dashed green placeholders, clustered to the right of the
// pod (or rack) they extend, with schematic wiring. Positions are
// cosmetic — cytoscape auto-fits the compound frames around them.
function renderGhosts() {
  if (!cy) return
  cy.remove('.ghost')

  const g = props.ghost
  if (!g || g.frames.length + g.nodes.length === 0) return

  for (const f of g.frames) {
    cy.add({
      group: 'nodes',
      data: { id: f.id, label: f.label, frame: f.frame, ...(f.parent ? { parent: f.parent } : {}) },
      selectable: false,
      grabbable: true,
      classes: 'ghost ghost-frame',
    })
  }

  // Anchoring: additions to an existing pod/rack cluster BELOW that
  // frame (the compound auto-expands around them, and the space to
  // the right belongs to the neighbouring pod), while a whole planned
  // pod goes right of the graph with natural tier rows. Sibling ghost
  // frames under one parent fan out horizontally.
  interface Anchor { x: number; y?: number } // y unset → natural tier rows
  const siblingIndex = new Map<string, number>()
  {
    const perParent = new Map<string, number>()
    for (const f of g.frames) {
      const key = f.parent ?? '__root__'
      const i = perParent.get(key) ?? 0
      perParent.set(key, i + 1)
      siblingIndex.set(f.id, i)
    }
  }

  const anchors = new Map<string, Anchor>()
  const anchorFor = (parentId?: string): Anchor => {
    const key = parentId ?? '__root__'
    const hit = anchors.get(key)
    if (hit) return hit

    let a: Anchor = { x: 200 }
    if (!parentId) {
      let maxX = 0
      cy!.nodes('[icon]').forEach((n) => { maxX = Math.max(maxX, n.position('x')) })
      a = { x: maxX + 150 }
    } else {
      const el = cy!.getElementById(parentId)
      if (el.nonempty() && !el.hasClass('ghost')) {
        const bb = el.boundingBox()
        a = { x: bb.x1 + 50, y: bb.y2 + 70 }
      } else {
        const frame = g.frames.find((f) => f.id === parentId)
        const base = anchorFor(frame?.parent)
        a = { x: base.x + (siblingIndex.get(parentId) ?? 0) * 200, y: base.y }
      }
    }

    anchors.set(key, a)
    return a
  }

  const counters = new Map<string, number>()
  for (const n of g.nodes) {
    const tier = tierOrder[n.role] ?? 5
    const key = `${n.parent ?? ''}:${tier}`
    const i = counters.get(key) ?? 0
    counters.set(key, i + 1)
    const a = anchorFor(n.parent)
    const y = a.y === undefined ? 80 + tier * 110 : a.y + (n.role === 'server' ? 100 : 0)
    cy.add({
      group: 'nodes',
      data: { id: n.id, label: n.label, role: n.role, ...(n.parent ? { parent: n.parent } : {}) },
      position: { x: a.x + i * 55, y },
      classes: 'ghost ghost-node',
    })
  }

  for (const l of g.links) {
    if (cy.getElementById(l.source).empty() || cy.getElementById(l.target).empty()) continue
    cy.add({ group: 'edges', data: { id: l.id, source: l.source, target: l.target }, classes: 'ghost' })
  }
}

// applyRemovedMarks tints everything a pending scale draft would
// delete: the devices themselves, their links, and any pod/rack frame
// whose devices are all marked.
function applyRemovedMarks() {
  if (!cy) return

  cy.elements('.marked-removed').removeClass('marked-removed')
  cy.elements('.marked-removed-frame').removeClass('marked-removed-frame')

  const removed = new Set(props.removedNodeIds ?? [])
  if (removed.size === 0) return

  for (const id of removed) cy.getElementById(id).addClass('marked-removed')
  for (const l of props.links) {
    if (removed.has(l.spec.endpointA.nodeId) || removed.has(l.spec.endpointB.nodeId)) {
      cy.getElementById(l.meta.id).addClass('marked-removed')
    }
  }

  cy.nodes(':parent').not('.ghost').forEach((frame) => {
    const devices = frame.descendants('[icon]')
    if (devices.length > 0 && devices.toArray().every((d) => removed.has(d.id()))) {
      frame.addClass('marked-removed-frame')
    }
  })
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
  if (rendered.length !== props.nodes.length || cy.edges().not('.ghost').length !== props.links.length) {
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
  applyRemovedMarks()
  applyDiagnosePaths()
  applyDiagnoseFocus(false)
}

// applyDiagnosePaths re-paints every measured path on each refresh
// (periodic observation updates rebuild edge classes from scratch),
// so the highlight survives until the caller clears diagnosePaths.
// Paths are applied in order and later ones win where they share a
// link (e.g. two ECMP branches's common first hop), via plain
// last-write-wins on the edge's data fields — colour/width/style ride
// on data() rather than a fixed set of CSS classes, since the number
// of simultaneous paths is caller-controlled, not a fixed enum.
function applyDiagnosePaths() {
  if (!cy) return

  for (const l of props.links) {
    cy.getElementById(l.meta.id).removeClass('diagnose-path')
  }

  for (const path of props.diagnosePaths ?? []) {
    for (const linkId of path.linkIds) {
      const el = cy.getElementById(linkId)
      if (el.empty()) continue

      el.data('diagnoseColor', path.color)
      el.data('diagnoseLineStyle', path.dashed ? 'dashed' : 'solid')
      el.addClass('diagnose-path')
    }
  }
}

// applyDiagnoseFocus highlights (and, on demand, pans/centres to) one
// node — e.g. the device behind a clicked hop-table row.
function applyDiagnoseFocus(pan: boolean) {
  if (!cy) return

  cy.nodes('.diagnose-focus').removeClass('diagnose-focus')
  if (!props.diagnoseFocusNodeId) return

  const el = cy.getElementById(props.diagnoseFocusNodeId)
  if (el.empty()) return

  el.addClass('diagnose-focus')
  if (pan) cy.animate({ center: { eles: el }, zoom: Math.max(cy.zoom(), 1) }, { duration: 300 })
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
      {
        // A path a diagnostic probe (mtr) measured. Colour and line
        // style come from data() so several paths can coexist (ECMP
        // branches, a muted previous run); the default palette starts
        // at deep violet. The hue is boxed in from two sides: a
        // dual-homed node keeps its blue edge.link-highlight on the
        // uplink that was NOT measured right next to this one, which
        // ruled out teal (too close to that blue); and red/magenta
        // (an earlier choice) read as a link-quality alarm — in this
        // UI red is the failed state — when the highlight only means
        // "this is where the probe went". Violet and its
        // palette-mates carry no health semantics anywhere else
        // (badge colours reserve green/orange/grey/red; the agent
        // badge's lighter purple is a tiny node dot, not a line).
        selector: 'edge.diagnose-path',
        style: {
          width: 3,
          'line-color': 'data(diagnoseColor)',
          'line-style': 'data(diagnoseLineStyle)' as 'solid',
          'z-index': 10,
        },
      },
      {
        // The device behind a clicked hop-table row: a halo in the
        // same violet family as the measured path, distinct from the
        // blue selection border (which stays on whichever node's
        // drawer is open).
        selector: 'node.diagnose-focus',
        style: {
          'border-width': 4,
          'border-color': '#722ed1',
          'border-style': 'double',
        },
      },
      {
        // Planned device from a pending scale draft: dashed green
        // placeholder — green matches the plan preview's create tag,
        // dashed says "not real yet".
        selector: 'node.ghost-node',
        style: {
          label: 'data(label)',
          shape: 'round-rectangle',
          width: 38,
          height: 38,
          'background-color': '#67c23a',
          'background-opacity': 0.08,
          'border-width': 2,
          'border-style': 'dashed',
          'border-color': '#67c23a',
          'font-size': 11,
          color: '#529b2e',
          'text-valign': 'bottom',
          'text-margin-y': 6,
        },
      },
      {
        // Planned pod/rack frame.
        selector: 'node.ghost-frame',
        style: {
          'border-color': '#67c23a',
          'border-style': 'dashed',
          color: '#529b2e',
        },
      },
      {
        // Planned wiring: schematic only.
        selector: 'edge.ghost',
        style: { width: 1, 'line-style': 'dashed', 'line-color': '#67c23a', opacity: 0.6 },
      },
      {
        // Existing element a pending scale draft would delete —
        // red matches the plan preview's delete tag.
        selector: 'node.marked-removed',
        style: { 'border-width': 2, 'border-style': 'dashed', 'border-color': '#f56c6c', opacity: 0.45 },
      },
      {
        selector: 'edge.marked-removed',
        style: { 'line-style': 'dashed', 'line-color': '#f56c6c', opacity: 0.45 },
      },
      {
        selector: 'node.marked-removed-frame',
        style: { 'border-color': '#f56c6c', color: '#f56c6c' },
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
  // Right-click opens the scale menu: on a pod/rack frame directly,
  // on a device via the frame it belongs to (aiming at a small icon
  // is easier than at the frame border), on the background for
  // lab-level actions. The page decides what the menu offers.
  cy.on('cxttap', (ev) => {
    if (ev.target !== cy) return
    const oe = ev.originalEvent as MouseEvent
    emit('scaleMenu', { kind: 'background' }, { x: oe.clientX, y: oe.clientY })
  })
  cy.on('cxttap', 'node', (ev) => {
    const oe = ev.originalEvent as MouseEvent
    const pos = { x: oe.clientX, y: oe.clientY }
    const id = ev.target.id() as string
    if (id.startsWith('pod:')) {
      emit('scaleMenu', { kind: 'pod', podId: id.slice(4) }, pos)
    } else if (id.startsWith('rack:')) {
      const [podId, rackId] = id.slice(5).split('/')
      emit('scaleMenu', { kind: 'rack', podId, rackId }, pos)
    } else {
      const n = props.nodes.find((x) => x.meta.id === id)
      if (!n?.spec.podId) return
      if (n.spec.rackId) emit('scaleMenu', { kind: 'rack', podId: n.spec.podId, rackId: n.spec.rackId }, pos)
      else emit('scaleMenu', { kind: 'pod', podId: n.spec.podId }, pos)
    }
  })
  render()

  // Debug/e2e hook: the cytoscape instance is otherwise unreachable
  // from outside (it renders into a <canvas>), which makes automated
  // UI verification unable to locate elements to interact with.
  ;(window as unknown as Record<string, unknown>).__dcnetlabCy = cy

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
watch(() => props.diagnosePaths, applyDiagnosePaths)
watch(
  () => props.ghost,
  () => {
    renderGhosts()
    applyRemovedMarks()
    cy?.fit(undefined, 40)
  },
)
watch(() => props.removedNodeIds, applyRemovedMarks)
// Only a change of focus pans; the re-application inside render() and
// refreshState() keeps the halo without yanking the viewport around
// on every observation sweep.
watch(() => props.diagnoseFocusNodeId, () => applyDiagnoseFocus(true))
onBeforeUnmount(() => {
  resizeObserver?.disconnect()
  cy?.destroy()
})

// resetLayout recomputes the tiered layout from scratch — the escape
// hatch after dragging nodes or frames into a mess. A full render
// also re-applies ghosts, marks and diagnose overlays, then fits the
// viewport.
defineExpose({ resetLayout: render })
</script>

<template>
  <div class="wrapper">
    <div ref="container" class="canvas" @contextmenu.prevent />
    <div class="legend">
      <div class="legend-row">
        <span v-for="r in legendRoles" :key="r.role" class="legend-item">
          <img :src="roleIcons[r.role]" :alt="r.role" />
          {{ r.label }}
        </span>
      </div>
      <div class="legend-row">
        <el-tooltip
          v-for="b in legendBadges"
          :key="b.badge"
          :content="t(`topology.${b.key}Tip`)"
          placement="top"
          effect="light"
        >
          <span class="legend-item">
            <span class="badge-dot" :style="{ background: badgeColor[b.badge] }" />
            {{ t(`topology.${b.key}`) }}
          </span>
        </el-tooltip>
        <el-popover placement="top-start" :width="360" trigger="hover">
          <template #reference>
            <el-icon class="badge-help"><QuestionFilled /></el-icon>
          </template>
          <div class="badge-help-body">
            <p class="badge-help-title">{{ t('topology.badgeHelpTitle') }}</p>
            <p>{{ t('topology.badgeHelpIntro') }}</p>
            <p v-for="b in legendBadges" :key="b.badge">
              <span class="badge-dot" :style="{ background: badgeColor[b.badge] }" />
              <b>{{ t(`topology.${b.key}`) }}</b>: {{ t(`topology.${b.key}Tip`) }}
            </p>
            <p>{{ t('topology.badgeHelpNone') }}</p>
          </div>
        </el-popover>
      </div>
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
  flex-direction: column;
  gap: 5px;
  padding: 6px 12px;
  border: 1px solid var(--el-border-color);
  border-radius: 4px;
  background: color-mix(in srgb, var(--el-bg-color) 88%, transparent);
  font-size: 12px;
  color: var(--el-text-color-secondary);
}
.legend-row { display: flex; align-items: center; gap: 14px; }
.legend-item { display: inline-flex; align-items: center; gap: 5px; }
.legend-item img { width: 18px; height: 18px; }
.badge-dot {
  display: inline-block;
  width: 9px;
  height: 9px;
  border-radius: 50%;
  flex-shrink: 0;
}
.badge-help { cursor: help; font-size: 14px; }
.badge-help:hover { color: var(--el-color-primary); }
</style>

<style>
/* el-popover teleports its popper to <body>, outside this component's
   scoped tree, so the help-body styles live in an unscoped block. */
.badge-help-body { font-size: 12px; line-height: 1.7; }
.badge-help-body p { margin: 0 0 4px; }
.badge-help-body .badge-dot {
  display: inline-block;
  width: 9px;
  height: 9px;
  border-radius: 50%;
  margin-right: 5px;
  vertical-align: -1px;
}
.badge-help-body .badge-help-title { font-weight: 600; }
</style>
