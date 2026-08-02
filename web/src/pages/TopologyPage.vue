<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Aim, Box, Document, Monitor, Position, QuestionFilled, Refresh, Search, TrendCharts, Warning } from '@element-plus/icons-vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { useLabStore } from '../stores/lab'
import { labApi } from '../api/lab'
import { nodeCaptureInterfaces } from '../utils/capture'
import { badgeColor, nodeBadge, roleColor } from '../utils/health'
import type { FaultScenario, Link, LinkEndpoint, MetricsPoint, MTRHop, MTRPathScan, Node, NodeBGP, NodeBGPTable, NodeInventory, NodeMetrics, NodeMTR, NodeRoutes, NodeRuntime, Plan } from '../types/models'
import TopologyCanvas, { type ScaleMenuTarget } from '../components/TopologyCanvas.vue'
import TerminalPanel from '../components/TerminalPanel.vue'
import MetricsChart, { type ChartSeries } from '../components/MetricsChart.vue'
import PlanPreviewDialog from '../components/PlanPreviewDialog.vue'
import OperationProgress from '../components/OperationProgress.vue'
import { useScaleDraft } from '../composables/useScaleDraft'

const store = useLabStore()
const router = useRouter()
const { t } = useI18n()
const selectedNode = ref<Node | null>(null)
const selectedLink = ref<Link | null>(null)
const terminal = ref<InstanceType<typeof TerminalPanel> | null>(null)
const canvas = ref<InstanceType<typeof TopologyCanvas> | null>(null)

// Ctrl/Cmd+Z steps the scale draft back while one is being edited
// (not once its preview was submitted — the desired state already
// moved on by then).
function onScaleUndoKey(e: KeyboardEvent) {
  if (!(e.ctrlKey || e.metaKey) || e.key !== 'z' || e.shiftKey) return
  if (!scale.canUndo.value || scale.submitted.value) return
  const target = e.target as HTMLElement | null
  if (target && (target.tagName === 'INPUT' || target.tagName === 'TEXTAREA' || target.isContentEditable)) return
  e.preventDefault()
  scale.undo()
}

onMounted(async () => {
  document.addEventListener('click', closeScaleMenu)
  document.addEventListener('keydown', onScaleUndoKey)
  if (store.labs.length === 0) await store.refreshLabs()
  await store.refreshTopology()
  store.startObserving()
})
onBeforeUnmount(() => {
  document.removeEventListener('click', closeScaleMenu)
  document.removeEventListener('keydown', onScaleUndoKey)
  store.stopObserving()
})

// Keep the open drawer in sync with incoming observations.
watch(
  () => store.nodes,
  (nodes) => {
    if (!selectedNode.value) return
    const fresh = nodes.find((n) => n.meta.id === selectedNode.value!.meta.id)
    if (fresh) selectedNode.value = fresh
  },
)

const nodeLinks = computed(() =>
  selectedNode.value
    ? store.links.filter(
        (l) =>
          l.spec.endpointA.nodeId === selectedNode.value!.meta.id ||
          l.spec.endpointB.nodeId === selectedNode.value!.meta.id,
      )
    : [],
)

// The simulated interface table: topology link endpoints plus the
// modeled logical interfaces (leaf vlanif, server bond0), with the
// observed oper state once the observer has swept the node. Fabric
// links carry /31 addresses on both ends; L2 links have none.
interface IfaceRow {
  name: string
  address: string
  peer: string
  peerIface: string
  peerAddress: string
  up?: boolean
}

const ifaceRows = computed<IfaceRow[]>(() => {
  const node = selectedNode.value
  if (!node) return []

  const states = new Map((node.status.interfaces ?? []).map((i) => [i.name, i.up]))
  const up = (name: string) => (states.size > 0 ? (states.get(name) ?? false) : undefined)

  // Numeric ordering (eth2 before eth10): links are stored in
  // creation order, which is not per-node ascending.
  const num = (name: string) => Number(name.match(/(\d+)$/)?.[1] ?? 0)
  const rows = nodeLinks.value
    .map((l) => {
      const mine = l.spec.endpointA.nodeId === node.meta.id ? l.spec.endpointA : l.spec.endpointB
      const other = l.spec.endpointA.nodeId === node.meta.id ? l.spec.endpointB : l.spec.endpointA
      return {
        name: mine.interface,
        address: mine.address ?? '',
        peer: other.nodeName,
        peerIface: other.interface,
        peerAddress: other.address ?? '',
        up: up(mine.interface),
      }
    })
    .sort((a, b) => num(a.name) - num(b.name) || a.name.localeCompare(b.name))
  if (node.spec.vlanId) {
    const name = `vlan${node.spec.vlanId}`
    rows.push({ name, address: node.spec.vlanIp ?? '', peer: '—', peerIface: '', peerAddress: '', up: up(name) })
  }
  if (node.spec.role === 'server' && node.spec.address) {
    rows.push({ name: 'bond0', address: node.spec.address, peer: '—', peerIface: '', peerAddress: '', up: up('bond0') })
  }
  return rows
})

// --- Drawer header: the same badge the canvas paints on the icon,
// rendered as a labelled chip with the legend tooltip ---
const nodeHealth = computed(() => {
  const node = selectedNode.value
  if (!node) return null

  const badge = nodeBadge(node)
  if (!badge) return null

  const key = { ok: 'badgeOk', warn: 'badgeWarn', agent: 'badgeAgent', stopped: 'badgeStopped', failed: 'badgeFailed' }[badge]!
  return { color: badgeColor[badge], label: t(`topology.${key}`), tip: t(`topology.${key}Tip`) }
})

// Overview stat tiles: the three observed numbers an operator scans
// first, coloured by whether they are fully converged.
const statTiles = computed(() => {
  const s = selectedNode.value?.status
  if (!s?.lastObserved) return []

  const bgpEst = s.bgpEstablished ?? 0
  const bgpConf = s.bgpConfigured ?? 0
  const ifUp = s.interfacesUp ?? 0
  const ifTotal = s.interfacesTotal ?? 0
  const bgpTone = bgpConf === 0 ? '' : bgpEst >= bgpConf ? 'ok' : 'warn'
  const ifaceTone = ifTotal === 0 ? '' : ifUp >= ifTotal ? 'ok' : 'bad'
  return [
    { label: t('topology.bgpSessions'), value: `${bgpEst} / ${bgpConf}`, tone: bgpTone },
    { label: t('topology.routes'), value: `${s.routeCount ?? 0}`, tone: '' },
    { label: t('topology.interfacesUp'), value: `${ifUp} / ${ifTotal}`, tone: ifaceTone },
  ]
})

// The network-config section only renders for roles that carry any
// of its fields, so spine-class nodes don't get an empty box.
const hasNetworkConfig = computed(() => {
  const sp = selectedNode.value?.spec
  if (!sp) return false

  return Boolean(
    sp.vlanId || sp.vlanIp || sp.address || sp.gatewayIp || sp.defaultGateway || sp.gatewayMac || sp.bgpPeers?.length,
  )
})

// --- BGP configuration: compiled from the model, static per node ---
const nodeBGP = ref<NodeBGP | null>(null)

// Rows for the BGP table: static neighbors plus the leaf's dynamic
// server listen range as a synthetic last row. A session is iBGP
// exactly when the remote AS equals the node's own AS (the MLAG peer
// pair shares one rack ASN).
const bgpRows = computed(() => {
  const bgp = nodeBGP.value
  if (!bgp) return []

  const kind = (remoteAs: number) => (remoteAs === bgp.asn ? 'iBGP' : 'eBGP')
  const rows = (bgp.neighbors ?? []).map((n) => ({
    address: n.address,
    remoteAs: n.remoteAs,
    kind: kind(n.remoteAs),
    description: n.description ?? '',
  }))
  if (bgp.serverGroup) {
    rows.push({
      address: bgp.serverGroup.listenRange,
      remoteAs: bgp.serverGroup.remoteAs,
      kind: kind(bgp.serverGroup.remoteAs),
      description: t('topology.bgpServerGroup'),
    })
  }
  return rows
})

async function loadBGP(node: Node) {
  if (!store.currentLabId) return

  try {
    const bgp = await labApi.nodeBGP(store.currentLabId, node.meta.id)
    // Ignore stale replies after the user switched nodes.
    if (selectedNode.value?.meta.id === node.meta.id) nodeBGP.value = bgp
  } catch {
    nodeBGP.value = null
  }
}

// --- On-demand views (BGP table / RIB / FIB / runtime), mirroring
// how a real DC operator walks the layers: Loc-RIB before best-path
// selection, the RIB it feeds and the FIB that actually forwards ---
const drawerTab = ref('sim')
const nodeRuntime = ref<NodeRuntime | null>(null)
const runtimeLoading = ref(false)
const nodeRoutes = ref<NodeRoutes | null>(null)
const routesLoading = ref(false)
const nodeBGPTable = ref<NodeBGPTable | null>(null)
const bgpTableLoading = ref(false)
const nodeFIB = ref<NodeRoutes | null>(null)
const fibLoading = ref(false)
const nodeInventory = ref<NodeInventory | null>(null)
const inventoryLoading = ref(false)
const nodeMetrics = ref<NodeMetrics | null>(null)
const metricsLoading = ref(false)
const nodeMTR = ref<NodeMTR | null>(null)
const mtrLoading = ref(false)

// The routing tab folds the three route-table views (Loc-RIB → RIB
// → FIB) behind one segmented switch so the tab bar stays readable.
type InspectView = 'bgp-table' | 'routes' | 'fib'
const inspectView = ref<InspectView>('bgp-table')
const inspectOptions = computed(() => [
  { label: t('topology.bgpTableView'), value: 'bgp-table' },
  { label: t('topology.routesView'), value: 'routes' },
  { label: t('topology.fibView'), value: 'fib' },
])

// Opening the drawer for another node resets to the simulated view;
// observation sweeps replace the node object with the same id and
// must not reset it.
watch(selectedNode, (node, prev) => {
  if (!node || node.meta.id !== prev?.meta.id) {
    drawerTab.value = 'sim'
    inspectView.value = 'bgp-table'
    nodeRuntime.value = null
    nodeRoutes.value = null
    nodeBGPTable.value = null
    nodeFIB.value = null
    nodeInventory.value = null
    nodeMetrics.value = null
    historyPoints.value = []
    historyIface.value = ''
    nodeBGP.value = null
    // The measured paths belonged to the previous node's probes;
    // stale highlights on the new node's drawer would misread as
    // "this is its path" instead of "nothing has been probed yet".
    nodeMTR.value = null
    mtrPrevious.value = null
    mtrScan.value = null
    mtrFocusNodeId.value = ''
    mtrTargetNodeId.value = ''
    if (node) void loadBGP(node)
  }
})

watch(drawerTab, (tab) => {
  if (tab === 'inspect') void inspectRefresh[inspectView.value]()
  if (tab === 'runtime') void refreshRuntime()
  if (tab === 'programs') void refreshInventory()
  if (tab === 'fault') void refreshFaults()
  if (tab === 'metrics') {
    void refreshMetrics()
    void refreshHistory()
  }

  // Metrics are rates over a short sampling window: keep them fresh
  // while the tab is visible instead of asking for manual refreshes.
  if (tab === 'metrics') startMetricsPolling()
  else stopMetricsPolling()
})

let metricsTimer: number | undefined

function startMetricsPolling() {
  stopMetricsPolling()
  metricsTimer = window.setInterval(() => void refreshMetricsSilently(), 5000)
}

// The periodic refresh skips the spinner and swallows errors: a
// missed sample keeps the last one on screen instead of toasting
// every 5 seconds. History is collected every 15 s, so its curves
// piggyback on the same timer at a slower cadence.
async function refreshMetricsSilently() {
  if (!selectedNode.value || !store.currentLabId || metricsLoading.value) return

  try {
    nodeMetrics.value = await labApi.nodeMetrics(store.currentLabId, selectedNode.value.meta.id)
  } catch {
    // keep the previous sample
  }

  if (Date.now() - lastHistoryFetch > 30_000) void refreshHistory(true)
}

function stopMetricsPolling() {
  if (metricsTimer !== undefined) {
    clearInterval(metricsTimer)
    metricsTimer = undefined
  }
}

onBeforeUnmount(stopMetricsPolling)

// fetchView wraps the shared fetch-on-demand pattern of the drawer
// tabs: guard, spinner, error toast.
async function fetchView<T>(
  target: { value: T | null },
  loading: { value: boolean },
  call: (labId: string, nodeId: string) => Promise<T>,
) {
  if (!selectedNode.value || !store.currentLabId) return

  loading.value = true
  try {
    target.value = await call(store.currentLabId, selectedNode.value.meta.id)
  } catch (e) {
    ElMessage.error((e as Error).message)
  } finally {
    loading.value = false
  }
}

const refreshRuntime = () => fetchView(nodeRuntime, runtimeLoading, labApi.nodeRuntime)
const refreshRoutes = () => fetchView(nodeRoutes, routesLoading, labApi.nodeRoutes)
const refreshBGPTable = () => fetchView(nodeBGPTable, bgpTableLoading, labApi.nodeBGPTable)
const refreshFIB = () => fetchView(nodeFIB, fibLoading, labApi.nodeFIB)
const refreshInventory = () => fetchView(nodeInventory, inventoryLoading, labApi.nodeInventory)
const refreshMetrics = () => fetchView(nodeMetrics, metricsLoading, labApi.nodeMetrics)

const inspectRefresh: Record<InspectView, () => Promise<void>> = {
  'bgp-table': refreshBGPTable,
  routes: refreshRoutes,
  fib: refreshFIB,
}

// Switching the segmented view fetches on demand, mirroring what
// switching tabs used to do when each view was its own tab.
watch(inspectView, (view) => {
  if (drawerTab.value === 'inspect') void inspectRefresh[view]()
})

// --- Metrics formatting: protobuf int64 fields arrive as strings ---

function fmtBytes(v?: string | number): string {
  let n = Number(v ?? 0)
  if (!Number.isFinite(n) || n <= 0) return '0 B'

  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB']
  let i = 0
  while (n >= 1024 && i < units.length - 1) {
    n /= 1024
    i++
  }
  return `${n >= 100 || i === 0 ? Math.round(n) : n.toFixed(1)} ${units[i]}`
}

const fmtRate = (v?: number) => `${fmtBytes(v)}/s`

function fmtUptime(seconds?: string): string {
  const s = Number(seconds ?? 0)
  if (s < 60) return `${Math.floor(s)}s`
  const d = Math.floor(s / 86400)
  const h = Math.floor((s % 86400) / 3600)
  const m = Math.floor((s % 3600) / 60)
  if (d > 0) return `${d}d ${h}h`
  if (h > 0) return `${h}h ${m}m`
  return `${m}m`
}

// gaugeRows condenses CPU / memory / filesystem into one usage-bar
// list; percentages clamp to [0, 100] for el-progress.
const gaugeRows = computed(() => {
  const m = nodeMetrics.value
  if (!m) return []

  const clamp = (v: number) => Math.min(100, Math.max(0, v))
  const pct = (used: number, total: number) => (total > 0 ? clamp((used / total) * 100) : 0)

  const cpu = clamp(m.cpu?.usagePercent ?? 0)
  const memUsed = Number(m.memory?.usedBytes ?? 0)
  const memLimit = Number(m.memory?.limitBytes ?? 0)
  const fsUsed = Number(m.filesystem?.usedBytes ?? 0)
  const fsSize = Number(m.filesystem?.sizeBytes ?? 0)

  return [
    {
      name: 'CPU',
      percent: cpu,
      detail: `${(m.cpu?.usagePercent ?? 0).toFixed(1)}% · usr ${(m.cpu?.userPercent ?? 0).toFixed(1)}% / sys ${(m.cpu?.systemPercent ?? 0).toFixed(1)}% · ${m.cpu?.limitCores ?? 0} cores`,
    },
    {
      name: t('topology.metricsMemory'),
      percent: pct(memUsed, memLimit),
      detail: `${fmtBytes(memUsed)} / ${fmtBytes(memLimit)} · cache ${fmtBytes(m.memory?.cacheBytes)}`,
    },
    {
      name: t('topology.metricsFilesystem'),
      percent: pct(fsUsed, fsSize),
      detail: `${fmtBytes(fsUsed)} / ${fmtBytes(fsSize)}`,
    },
  ]
})

const gaugeStatus = (percent: number) => (percent >= 90 ? 'exception' : percent >= 75 ? 'warning' : undefined)

// --- Metrics history: 15 s collector points drawn as line charts ---
const historyRange = ref(1800) // seconds; 30m default
const historyPoints = ref<MetricsPoint[]>([])
const historyLoading = ref(false)
const historyIface = ref('')
let lastHistoryFetch = 0

async function refreshHistory(silent = false) {
  if (!selectedNode.value || !store.currentLabId) return

  lastHistoryFetch = Date.now()
  if (!silent) historyLoading.value = true
  try {
    const end = Math.floor(Date.now() / 1000)
    historyPoints.value = await labApi.nodeMetricsHistory(
      store.currentLabId, selectedNode.value.meta.id, end - historyRange.value, end,
    )
  } catch (e) {
    if (!silent) ElMessage.error((e as Error).message)
  } finally {
    if (!silent) historyLoading.value = false
  }
}

watch(historyRange, () => void refreshHistory())

const toMs = (ts?: string) => Number(ts ?? 0) * 1000

// The interface picker offers what the latest point saw; the
// management interface eth0 exists everywhere, so the first data
// interface is the more interesting default.
const ifaceOptions = computed(() => {
  const last = historyPoints.value[historyPoints.value.length - 1]
  return (last?.interfaces ?? []).map((i) => i.name)
})

watch(ifaceOptions, (names) => {
  if (names.length === 0 || names.includes(historyIface.value)) return
  historyIface.value = names.find((n) => n !== 'lo' && n !== 'eth0') ?? names[0]
})

const cpuSeries = computed<ChartSeries[]>(() => [
  { name: t('topology.metricsSeriesUsage'), points: historyPoints.value.map((p) => [toMs(p.ts), p.cpu?.usagePercent ?? 0]) },
  { name: 'usr', points: historyPoints.value.map((p) => [toMs(p.ts), p.cpu?.userPercent ?? 0]) },
  { name: 'sys', points: historyPoints.value.map((p) => [toMs(p.ts), p.cpu?.systemPercent ?? 0]) },
])

const memSeries = computed<ChartSeries[]>(() => [
  { name: t('topology.metricsSeriesUsed'), points: historyPoints.value.map((p) => [toMs(p.ts), Number(p.memory?.usedBytes ?? 0)]) },
  { name: t('topology.metricsSeriesLimit'), dashed: true, points: historyPoints.value.map((p) => [toMs(p.ts), Number(p.memory?.limitBytes ?? 0)]) },
])

const netSeries = computed<ChartSeries[]>(() => {
  const of = (p: MetricsPoint) => (p.interfaces ?? []).find((i) => i.name === historyIface.value)
  return [
    { name: t('topology.metricsRx'), points: historyPoints.value.map((p) => [toMs(p.ts), of(p)?.rxBytesPerSec ?? 0]) },
    { name: t('topology.metricsTx'), points: historyPoints.value.map((p) => [toMs(p.ts), of(p)?.txBytesPerSec ?? 0]) },
  ]
})

const diskSeries = computed<ChartSeries[]>(() => [
  { name: t('topology.metricsRead'), points: historyPoints.value.map((p) => [toMs(p.ts), p.disk?.readBytesPerSec ?? 0]) },
  { name: t('topology.metricsWrite'), points: historyPoints.value.map((p) => [toMs(p.ts), p.disk?.writeBytesPerSec ?? 0]) },
])

// programStateTag mirrors the Programs page state colouring.
function programStateTag(state: string): string {
  switch (state) {
    case 'Running':
      return 'success'
    case 'Failed':
      return 'danger'
    case 'Unknown':
      return 'warning'
    default:
      return 'info'
  }
}

// The FIB mixes two layers, like a real switch: LPM forwarding
// entries and host (punt) entries for the device's own addresses.
const fibCounts = computed(() => {
  const routes = nodeFIB.value?.routes ?? []
  const host = routes.filter((r) => r.kind === 'local').length
  return { lpm: routes.length - host, host }
})

async function onLabChange(id: string) {
  selectedNode.value = null
  selectedLink.value = null
  terminal.value?.closeAll()
  await store.selectLab(id)
}

// Double-click on a device: close the detail drawer the first tap
// opened and drop into the node's shell.
function onOpenTerminal(node: Node) {
  if (!store.currentLabId) return

  selectedNode.value = null
  selectedLink.value = null
  terminal.value?.open(store.currentLabId, node)
}

// --- Power control ---
// The lab is powerable once it has a deployed generation; "running"
// drives both the DC button label and the link-flow animation.
const deployed = computed(() => (store.currentLab?.meta.generation ?? '0') !== '0')
const labRunning = computed(() =>
  ['Running', 'Degraded'].includes(store.currentLab?.meta.phase ?? ''),
)
const labBusy = ref(false)
const nodeBusy = ref(false)

async function powerLab() {
  labBusy.value = true
  try {
    await store.powerLab(!labRunning.value)
  } catch (e) {
    ElMessage.error((e as Error).message)
  } finally {
    labBusy.value = false
  }
}

// --- Drift detection: a host Docker/WSL restart can drop the veth
// pairs containerlab wired in, leaving a container docker-Running
// but network-dead. This is structurally distinct from a fault: a
// FaultScenario only ever toggles an existing interface's state
// (interface-down/link-down set it administratively down), it never
// removes the interface, so `missing` can only ever mean drift —
// no cross-check against active faults is needed. Only Running-phase
// nodes are considered: a paused node (node-stop fault or manual
// stop) keeps its last observed (frozen) interfaces, which is not
// drift, and a node mid-Apply is not yet expected to have them.
const driftedNodes = computed(() =>
  store.nodes.filter(
    (n) => n.meta.phase === 'Running' && n.status.interfaces?.some((i) => i.missing),
  ),
)
const driftedNodeNames = computed(() => {
  const names = driftedNodes.value.map((n) => n.meta.name)
  return names.length > 4 ? `${names.slice(0, 4).join(', ')}, +${names.length - 4}` : names.join(', ')
})
const repairing = ref(false)

async function repairLab() {
  repairing.value = true
  try {
    await store.repairLab()
    ElMessage.success(t('topology.repaired'))
  } catch (e) {
    ElMessage.error((e as Error).message)
  } finally {
    repairing.value = false
  }
}

// --- WYSIWYG scaling: right-click the canvas to grow or shrink the
// fabric. Actions edit a client-side draft rendered as ghost
// (planned) and red-marked (to-be-removed) elements; confirming goes
// through the regular change-plan preview and incremental apply.
const scale = useScaleDraft(() => store.currentLab, () => store.nodes)
const scaleMenu = ref<{ visible: boolean; x: number; y: number; target: ScaleMenuTarget | null }>({
  visible: false, x: 0, y: 0, target: null,
})
const scalePlan = ref<Plan | null>(null)
const scalePlanVisible = ref(false)
const scaleOpId = ref('')
const scaleBusy = ref(false)

function onScaleMenu(target: ScaleMenuTarget, pos: { x: number; y: number }) {
  const phase = store.currentLab?.meta.phase
  if (!store.currentLab || phase === 'Applying') return
  scaleMenu.value = { visible: true, x: pos.x, y: pos.y, target }
}

function closeScaleMenu() {
  scaleMenu.value.visible = false
}

interface ScaleMenuItem {
  key: string
  label: string
  disabled?: boolean
  tip?: string
  danger?: boolean
  run: () => void
}

const scaleMenuItems = computed<ScaleMenuItem[]>(() => {
  const tgt = scaleMenu.value.target
  if (!tgt) return []

  if (tgt.kind === 'background') {
    return [{ key: 'addPod', label: t('scale.addPod'), run: () => scale.addPod() }]
  }

  const pod = tgt.podId!
  scale.ensureDraft()
  const dp = scale.draftPod(pod)
  const items: ScaleMenuItem[] = []

  if (tgt.kind === 'rack') {
    const can = scale.canRemoveRack(pod, tgt.rackId!)
    items.push({
      key: 'removeThisRack',
      label: t('scale.removeThisRack', { rack: tgt.rackId }),
      disabled: !can.ok,
      tip: can.ok ? undefined : t(`scale.removeRackHint.${can.reason}`),
      danger: true,
      run: () => scale.bump(pod, 'racks', -1),
    })
  }

  items.push(
    { key: 'addRack', label: t('scale.addRack', { pod }), run: () => scale.bump(pod, 'racks', 1) },
    { key: 'addServer', label: t('scale.addServer', { pod }), run: () => scale.bump(pod, 'serversPerRack', 1) },
    {
      key: 'removeServer',
      label: t('scale.removeServer', { pod }),
      disabled: (dp?.serversPerRack ?? 1) <= 1,
      run: () => scale.bump(pod, 'serversPerRack', -1),
    },
    { key: 'addSpine', label: t('scale.addSpine', { pod }), run: () => scale.bump(pod, 'spines', 1) },
    {
      key: 'removeSpine',
      label: t('scale.removeSpine', { pod }),
      disabled: (dp?.spines ?? 1) <= 1,
      run: () => scale.bump(pod, 'spines', -1),
    },
  )

  if (tgt.kind === 'pod') {
    items.push({
      key: 'removePod',
      label: t('scale.removePod', { pod }),
      disabled: (scale.draft.value?.pods.length ?? 2) <= 1,
      danger: true,
      run: () => scale.removePod(pod),
    })
  }

  return items
})

function runScaleMenuItem(item: ScaleMenuItem) {
  if (item.disabled) return
  item.run()
  closeScaleMenu()
}

// The draft summary line for the pending bar.
const scaleSummary = computed(() =>
  scale.changes.value.map((c) => t(`scale.changes.${c.key}`, { n: c.count })).join(t('scale.changeSep')),
)

// Ghosts are only drawn while the draft is client-side: once the
// preview submitted the spec and created a plan, the desired topology
// itself contains the planned nodes and drawing ghosts on top would
// duplicate them.
const scaleGhost = computed(() => (scale.submitted.value ? undefined : scale.preview.value.ghost))
const scaleRemovedIds = computed(() => (scale.submitted.value ? [] : scale.preview.value.removedNodeIds))

async function previewScaleDraft() {
  if (!scale.draft.value || !store.currentLabId) return
  scaleBusy.value = true
  try {
    await labApi.updateTopology(store.currentLabId, scale.draft.value)
    scale.submitted.value = true
    scalePlan.value = await labApi.createPlan(store.currentLabId)
    scalePlanVisible.value = true
    await store.refreshTopology()
  } catch (e) {
    ElMessage.error((e as Error).message)
  } finally {
    scaleBusy.value = false
  }
}

async function applyScalePlan() {
  if (!scalePlan.value) return
  try {
    const { operationId } = await labApi.applyPlan(scalePlan.value.id)
    scalePlanVisible.value = false
    scaleOpId.value = operationId
  } catch (e) {
    ElMessage.error((e as Error).message)
  }
}

function onScaleOpDone() {
  scaleOpId.value = ''
  scale.discard()
  store.refreshLabs()
  store.refreshTopology()
}

// Discarding after the preview already submitted the spec restores
// the baseline server-side too (an extra no-op plan is the price of
// getting the desired rows back to the deployed shape).
async function discardScaleDraft() {
  const submitted = scale.submitted.value
  const baseline = scale.baseline.value
  scale.discard()
  if (submitted && baseline && store.currentLabId) {
    try {
      await labApi.updateTopology(store.currentLabId, baseline)
      await labApi.createPlan(store.currentLabId)
      await store.refreshTopology()
    } catch (e) {
      ElMessage.error((e as Error).message)
    }
  }
}

async function powerNode() {
  if (!selectedNode.value) return

  const target = selectedNode.value
  nodeBusy.value = true
  try {
    const node = await store.powerNode(target.meta.id, target.meta.phase === 'Stopped')
    if (selectedNode.value?.meta.id === node.meta.id) selectedNode.value = node
  } catch (e) {
    ElMessage.error((e as Error).message)
  } finally {
    nodeBusy.value = false
  }
}

function linkKindLabel(l: Link): string {
  const vlan = l.spec.vlanId ?? 0
  switch (l.spec.kind) {
    case 'server-access':
      return t('topology.kindServerAccess', { vlan })
    case 'mlag-peer':
      return t('topology.kindMlagPeer', { vlan })
    default:
      return t('topology.kindFabric')
  }
}

// --- Fault: quick-launch from the node/link drawers, target already
// known from the selection, so it is created and applied in one step
// instead of walking the Faults page's full dialog ---
const labFaults = ref<FaultScenario[]>([])
const faultBusy = ref(false)

async function refreshFaults() {
  if (!store.currentLabId) return

  try {
    labFaults.value = await labApi.faultScenarios(store.currentLabId)
  } catch {
    /* keep the last view */
  }
}

// Re-fetch whenever the link drawer opens; the node drawer's fault
// tab is covered by the drawerTab watcher above.
watch(selectedLink, (link) => {
  if (link) void refreshFaults()
})

const nodeFaults = computed(() =>
  selectedNode.value
    ? labFaults.value.filter((f) => f.spec.target.kind === 'node' && f.spec.target.nodeId === selectedNode.value!.meta.id)
    : [],
)
const linkFaults = computed(() =>
  selectedLink.value
    ? labFaults.value.filter((f) => f.spec.target.kind === 'link' && f.spec.target.linkId === selectedLink.value!.meta.id)
    : [],
)

function faultTypeLabel(type: string): string {
  switch (type) {
    case 'node-stop': return t('faults.typeNodeStop')
    case 'node-restart': return t('faults.typeNodeRestart')
    case 'link-down': return t('faults.typeLinkDown')
    case 'interface-down': return t('faults.typeInterfaceDown')
    case 'impairment': return t('faults.typeImpairment')
    default: return type
  }
}

// createAndApplyFault is the shared "quick launch" path: the target
// is already known from the drawer selection, so create + apply run
// back to back instead of leaving a Stopped scenario the user has to
// separately Apply from the Faults page.
async function createAndApplyFault(name: string, target: FaultScenario['spec']['target'], type: string, impairment?: FaultScenario['spec']['impairment']) {
  if (!store.currentLabId) return

  faultBusy.value = true
  try {
    const fs = await labApi.createFaultScenario(store.currentLabId, { name, target, type, impairment })
    await labApi.applyFaultScenario(store.currentLabId, fs.meta.id)
    ElMessage.success(t('faults.created', { name: fs.meta.name }))
    await refreshFaults()
  } catch (e) {
    ElMessage.error((e as Error).message)
  } finally {
    faultBusy.value = false
  }
}

function faultSlug(prefix: string): string {
  return `${prefix}-${Date.now().toString(36)}`
}

async function quickNodeFault(type: 'node-stop' | 'node-restart') {
  const node = selectedNode.value
  if (!node) return

  try {
    await ElMessageBox.confirm(
      t(type === 'node-stop' ? 'faults.confirmStopNode' : 'faults.confirmRestartNode', { name: node.meta.name }),
      t('faults.title'),
    )
  } catch {
    return
  }

  await createAndApplyFault(faultSlug(`${node.meta.name}-${type}`), { kind: 'node', nodeId: node.meta.id }, type)
}

async function quickLinkFault(type: 'link-down' | 'interface-down', side?: 'a' | 'b') {
  const link = selectedLink.value
  if (!link) return

  await createAndApplyFault(faultSlug(`${link.meta.name}-${type}`), { kind: 'link', linkId: link.meta.id, side }, type)
}

const impairForm = ref({
  delayMs: undefined as number | undefined,
  lossPercent: undefined as number | undefined,
  rateKbit: undefined as number | undefined,
})

async function quickImpairment() {
  const link = selectedLink.value
  if (!link) return

  if (!(impairForm.value.delayMs || impairForm.value.lossPercent || impairForm.value.rateKbit)) return

  await createAndApplyFault(
    faultSlug(`${link.meta.name}-impairment`),
    { kind: 'link', linkId: link.meta.id, side: 'both' },
    'impairment',
    { ...impairForm.value },
  )
  impairForm.value = { delayMs: undefined, lossPercent: undefined, rateKbit: undefined }
}

// --- quick capture (node drawer tab and link drawer section) ---
// Creating a session starts capturing immediately; on success we jump
// straight into the viewer.
const captureBusy = ref(false)
const captureIface = ref('')
const captureProto = ref('')
const captureDuration = ref(30)

const captureIfaceOptions = computed(() =>
  selectedNode.value ? nodeCaptureInterfaces(selectedNode.value, store.links) : [],
)

watch(selectedNode, (node, prev) => {
  if (node?.meta.id !== prev?.meta.id) captureIface.value = ''
})

async function startCapture(name: string, nodeId: string, iface: string, protocol?: string) {
  if (!store.currentLabId) return

  captureBusy.value = true
  try {
    const sess = await labApi.createCaptureSession(store.currentLabId, {
      name,
      nodeId,
      interface: iface,
      durationSeconds: captureDuration.value,
      filter: protocol ? { protocol } : undefined,
    })
    ElMessage.success(t('captures.created', { name: sess.meta.name }))
    void router.push(`/captures/${store.currentLabId}/${sess.meta.id}`)
  } catch (e) {
    ElMessage.error((e as Error).message)
  } finally {
    captureBusy.value = false
  }
}

async function quickNodeCapture() {
  const node = selectedNode.value
  if (!node || !captureIface.value) return

  await startCapture(
    faultSlug(`${node.meta.name}-${captureIface.value}`),
    node.meta.id,
    captureIface.value,
    captureProto.value || undefined,
  )
}

async function quickEndpointCapture(ep: LinkEndpoint) {
  await startCapture(faultSlug(`${ep.nodeName}-${ep.interface}`), ep.nodeId, ep.interface)
}

// --- Diagnose (mtr): a one-shot probe, not an on-demand view like
// BGP/routes/runtime — it only runs when the operator clicks "run",
// mirroring the capture tab's quick-form pattern rather than
// fetchView's load-on-tab-open one ---
const mtrMode = ref<'single' | 'scan'>('single')
const mtrTargetMode = ref<'node' | 'custom'>('node')
const mtrTargetNodeId = ref('')
const mtrTargetCustom = ref('')
const mtrProtocol = ref('icmp')
const mtrPort = ref<number>()
const mtrCycles = ref(10)
const mtrScanSamples = ref(8)
// The run before the current one, kept for a before/after comparison
// (e.g. inject a fault, probe again, see the path move): drawn dashed
// and muted behind the current path.
const mtrPrevious = ref<NodeMTR | null>(null)
const mtrScan = ref<MTRPathScan | null>(null)
const mtrFocusNodeId = ref('')

// An ECMP scan needs a port for the fabric's 5-tuple hashing to vary;
// entering scan mode steps an icmp selection up to tcp.
watch(mtrMode, (mode) => {
  if (mode === 'scan' && mtrProtocol.value === 'icmp') mtrProtocol.value = 'tcp'
})

const mtrTargetOptions = computed(() =>
  store.nodes.filter((n) => n.meta.id !== selectedNode.value?.meta.id).map((n) => ({ label: n.meta.name, value: n.meta.id })),
)

// Distinct colours for simultaneously drawn ECMP branches; deep
// violet first to match the single-path colour. All picked to stay
// clear of the selection blue, the grey link state, and — since a
// probe path is information, not a health signal — of the red/orange
// hues this UI reserves for failed/degraded states.
const mtrScanPalette = ['#722ed1', '#08979c', '#d48806', '#7cb305', '#1d39c4']

// diagnosePaths feeds the canvas: scan mode shows every distinct
// branch in its own colour; single mode shows the current path solid
// magenta with the previous run dashed and muted behind it.
const diagnosePaths = computed(() => {
  if (mtrScan.value?.paths?.length) {
    return mtrScan.value.paths.map((p, i) => ({
      id: `scan-${i}`,
      linkIds: p.pathLinkIds ?? [],
      color: mtrScanPalette[i % mtrScanPalette.length],
    }))
  }

  const paths = []
  if (mtrPrevious.value?.pathLinkIds?.length) {
    paths.push({ id: 'previous', linkIds: mtrPrevious.value.pathLinkIds, color: '#b37feb', dashed: true })
  }

  if (nodeMTR.value?.pathLinkIds?.length) {
    paths.push({ id: 'current', linkIds: nodeMTR.value.pathLinkIds, color: '#722ed1' })
  }

  if (linkMTR.value?.pathLinkIds?.length) {
    paths.push({ id: 'link', linkIds: linkMTR.value.pathLinkIds, color: '#722ed1' })
  }

  return paths
})

function validateMTRForm(): boolean {
  if (mtrTargetMode.value === 'node' && !mtrTargetNodeId.value) return false
  if (mtrTargetMode.value === 'custom' && !mtrTargetCustom.value) return false
  if (mtrProtocol.value !== 'icmp' && !mtrPort.value) {
    ElMessage.warning(t('mtr.portRequired'))
    return false
  }

  return true
}

// runDiagnose runs a single probe; cycles = 1 is the quick-ping
// preset (one round trip, fastest possible answer to "is it up").
async function runDiagnose(cycles?: number) {
  const node = selectedNode.value
  if (!node || !store.currentLabId || !validateMTRForm()) return

  mtrLoading.value = true
  try {
    const result = await labApi.nodeMTR(store.currentLabId, node.meta.id, {
      targetNodeId: mtrTargetMode.value === 'node' ? mtrTargetNodeId.value : undefined,
      target: mtrTargetMode.value === 'custom' ? mtrTargetCustom.value : undefined,
      protocol: mtrProtocol.value,
      port: mtrProtocol.value !== 'icmp' ? mtrPort.value : undefined,
      cycles: cycles ?? mtrCycles.value,
    })
    mtrPrevious.value = nodeMTR.value
    nodeMTR.value = result
    mtrScan.value = null
    mtrFocusNodeId.value = ''
  } catch (e) {
    ElMessage.error((e as Error).message)
  } finally {
    mtrLoading.value = false
  }
}

async function runDiagnoseScan() {
  const node = selectedNode.value
  if (!node || !store.currentLabId || !validateMTRForm()) return

  mtrLoading.value = true
  try {
    mtrScan.value = await labApi.nodeMTRScan(store.currentLabId, node.meta.id, {
      targetNodeId: mtrTargetMode.value === 'node' ? mtrTargetNodeId.value : undefined,
      target: mtrTargetMode.value === 'custom' ? mtrTargetCustom.value : undefined,
      protocol: mtrProtocol.value,
      port: mtrPort.value,
      samples: mtrScanSamples.value,
    })
    nodeMTR.value = null
    mtrPrevious.value = null
    mtrFocusNodeId.value = ''
  } catch (e) {
    ElMessage.error((e as Error).message)
  } finally {
    mtrLoading.value = false
  }
}

// Clicking a hop row highlights (and pans to) that device on the
// canvas; clicking it again clears the halo.
function toggleMTRFocus(row: MTRHop) {
  if (!row.nodeId) return

  mtrFocusNodeId.value = mtrFocusNodeId.value === row.nodeId ? '' : row.nodeId
}

// --- Link drawer diagnose: probe one endpoint from the other, the
// quickest answer to "is this link's segment healthy" without picking
// nodes in the form ---
const linkMTR = ref<NodeMTR | null>(null)
const linkMTRLoading = ref(false)
const linkMTRFrom = ref('')

async function probeLinkEndpoint(from: LinkEndpoint, to: LinkEndpoint) {
  if (!store.currentLabId) return

  linkMTRLoading.value = true
  linkMTRFrom.value = from.nodeName
  try {
    linkMTR.value = await labApi.nodeMTR(store.currentLabId, from.nodeId, {
      targetNodeId: to.nodeId,
      protocol: 'icmp',
      cycles: 5,
    })
  } catch (e) {
    ElMessage.error((e as Error).message)
  } finally {
    linkMTRLoading.value = false
  }
}

watch(selectedLink, (link, prev) => {
  if (!link || link.meta.id !== prev?.meta.id) {
    linkMTR.value = null
    linkMTRFrom.value = ''
  }
})

async function toggleFault(f: FaultScenario) {
  if (!store.currentLabId) return

  faultBusy.value = true
  try {
    if (f.status.applied) await labApi.recoverFaultScenario(store.currentLabId, f.meta.id)
    else await labApi.applyFaultScenario(store.currentLabId, f.meta.id)

    await refreshFaults()
  } catch (e) {
    ElMessage.error((e as Error).message)
  } finally {
    faultBusy.value = false
  }
}

async function deleteFault(f: FaultScenario) {
  if (!store.currentLabId) return

  faultBusy.value = true
  try {
    await labApi.deleteFaultScenario(store.currentLabId, f.meta.id)
    await refreshFaults()
  } catch (e) {
    ElMessage.error((e as Error).message)
  } finally {
    faultBusy.value = false
  }
}
</script>

<template>
  <div class="page">
    <div class="header">
      <h2>{{ t('topology.title') }} <span class="hint">{{ t('topology.doubleClickHint') }}</span></h2>
      <div class="header-actions">
        <el-button v-if="store.nodes.length" :icon="Refresh" @click="canvas?.resetLayout()">
          {{ t('topology.resetLayout') }}
        </el-button>
        <el-button
          v-if="deployed"
          :type="labRunning ? 'danger' : 'success'"
          :loading="labBusy"
          @click="powerLab"
        >
          {{ labRunning ? t('topology.stopDc') : t('topology.startDc') }}
        </el-button>
        <el-select
          :model-value="store.currentLabId"
          style="width: 240px"
          :placeholder="t('topology.selectLab')"
          @change="onLabChange"
        >
          <el-option v-for="l in store.labs" :key="l.meta.id" :label="l.meta.name" :value="l.meta.id" />
        </el-select>
      </div>
    </div>

    <el-alert v-if="driftedNodes.length" type="warning" show-icon :closable="false" class="drift-banner">
      <template #title>
        <span>{{ t('topology.driftBanner', { count: driftedNodes.length }) }}</span>
        <el-popover placement="bottom-start" :width="360" trigger="hover">
          <template #reference>
            <el-icon class="drift-why"><QuestionFilled /></el-icon>
          </template>
          <div class="drift-why-body">{{ t('topology.driftBannerWhy') }}</div>
        </el-popover>
      </template>
      <div class="drift-body">
        <span class="drift-nodes">{{ t('topology.driftNodes', { names: driftedNodeNames }) }}</span>
        <el-button size="small" type="warning" :loading="repairing" @click="repairLab">
          {{ t('topology.repair') }}
        </el-button>
      </div>
    </el-alert>

    <el-alert v-if="scale.dirty.value" type="success" :closable="false" class="scale-banner">
      <template #title>
        <span>{{ t('scale.pending', { summary: scaleSummary }) }}</span>
      </template>
      <div class="scale-banner-body">
        <span class="scale-hint">{{ t('scale.pendingHint') }}</span>
        <span class="scale-actions">
          <el-button size="small" :disabled="!scale.canUndo.value || scale.submitted.value" @click="scale.undo()">
            {{ t('scale.undo') }}
          </el-button>
          <el-button size="small" @click="discardScaleDraft">{{ t('scale.discard') }}</el-button>
          <el-button size="small" type="primary" :loading="scaleBusy" @click="previewScaleDraft">
            {{ t('labs.previewChanges') }}
          </el-button>
        </span>
      </div>
    </el-alert>

    <div class="body">
      <el-empty v-if="store.nodes.length === 0" :description="t('topology.empty')" />
      <TopologyCanvas
        v-else
        ref="canvas"
        :nodes="store.nodes"
        :links="store.links"
        :diagnose-paths="diagnosePaths"
        :diagnose-focus-node-id="mtrFocusNodeId"
        :ghost="scaleGhost"
        :removed-node-ids="scaleRemovedIds"
        @select-node="selectedNode = $event; selectedLink = null"
        @select-link="selectedLink = $event; selectedNode = null"
        @select-none="selectedNode = null; selectedLink = null"
        @open-terminal="onOpenTerminal"
        @scale-menu="onScaleMenu"
      />
    </div>

    <Teleport to="body">
      <div
        v-if="scaleMenu.visible"
        class="scale-menu"
        :style="{ left: scaleMenu.x + 'px', top: scaleMenu.y + 'px' }"
        @click.stop
      >
        <div
          v-for="item in scaleMenuItems"
          :key="item.key"
          class="scale-menu-item"
          :class="{ disabled: item.disabled, danger: item.danger && !item.disabled }"
          @click="runScaleMenuItem(item)"
        >
          <span>{{ item.label }}</span>
          <span v-if="item.tip" class="scale-menu-tip">{{ item.tip }}</span>
        </div>
      </div>
    </Teleport>

    <PlanPreviewDialog v-model:visible="scalePlanVisible" :plan="scalePlan" @apply="applyScalePlan" />
    <OperationProgress v-if="scaleOpId" :operation-id="scaleOpId" @done="onScaleOpDone" />

    <TerminalPanel ref="terminal" />

    <!-- Non-modal: a modal mask would swallow the second click of a
         double-click on the canvas, breaking terminal access. -->
    <!-- The metrics tab draws time-series charts and gets a wide
         drawer (capped by CSS max-width); every other tab keeps the
         compact width so the canvas stays usable. -->
    <el-drawer
      :model-value="!!selectedNode"
      :size="drawerTab === 'metrics' ? '60%' : '540px'"
      :modal="false"
      @close="selectedNode = null"
    >
      <template #header>
        <div v-if="selectedNode" class="drawer-head">
          <div class="drawer-id">
            <span class="drawer-name" :title="selectedNode.meta.name">{{ selectedNode.meta.name }}</span>
            <el-tag size="small" effect="dark" :color="roleColor[selectedNode.spec.role] ?? '#909399'" class="role-tag">
              {{ selectedNode.spec.role }}
            </el-tag>
            <el-tooltip v-if="nodeHealth" :content="nodeHealth.tip" placement="bottom-start" :show-after="300">
              <span class="health-chip">
                <span class="health-dot" :style="{ background: nodeHealth.color }" />
                {{ nodeHealth.label }}
              </span>
            </el-tooltip>
          </div>
          <el-button
            v-if="deployed"
            :type="selectedNode.meta.phase === 'Stopped' ? 'success' : 'danger'"
            :loading="nodeBusy"
            size="small"
            plain
            @click="powerNode"
          >
            {{ selectedNode.meta.phase === 'Stopped' ? t('topology.startNode') : t('topology.stopNode') }}
          </el-button>
        </div>
      </template>
      <template v-if="selectedNode">
        <el-tabs v-model="drawerTab">
          <el-tab-pane name="sim">
            <template #label>
              <span class="tab-label"><el-icon><Document /></el-icon>{{ t('topology.overview') }}</span>
            </template>
            <template v-if="statTiles.length">
              <div class="stat-tiles">
                <div v-for="tile in statTiles" :key="tile.label" class="stat-tile" :class="tile.tone">
                  <div class="stat-value">{{ tile.value }}</div>
                  <div class="stat-label">{{ tile.label }}</div>
                </div>
              </div>
              <div v-if="selectedNode.status?.lastObserved" class="observed-at">
                {{ t('topology.lastObserved') }} · {{ new Date(selectedNode.status.lastObserved).toLocaleTimeString() }}
              </div>
            </template>

            <h4>{{ t('topology.identity') }}</h4>
            <el-descriptions :column="1" border size="small">
              <el-descriptions-item :label="t('topology.phase')">{{ selectedNode.meta.phase }}</el-descriptions-item>
              <el-descriptions-item :label="t('topology.asn')" v-if="selectedNode.spec.asn">
                {{ selectedNode.spec.asn }}
              </el-descriptions-item>
              <el-descriptions-item :label="t('topology.loopback')" v-if="selectedNode.spec.loopback">
                {{ selectedNode.spec.loopback }}
              </el-descriptions-item>
              <el-descriptions-item :label="t('topology.pod')" v-if="selectedNode.spec.podId">
                {{ selectedNode.spec.podId }}
              </el-descriptions-item>
              <el-descriptions-item :label="t('topology.rack')" v-if="selectedNode.spec.rackId">
                {{ selectedNode.spec.rackId }}
              </el-descriptions-item>
              <el-descriptions-item :label="t('topology.mlagPeer')" v-if="selectedNode.spec.mlagPeer">
                {{ selectedNode.spec.mlagPeer }}
              </el-descriptions-item>
              <el-descriptions-item :label="t('topology.gatewayRole')" v-if="selectedNode.status?.vrrpState">
                <el-tag size="small" :type="selectedNode.status.vrrpState === 'Master' ? 'success' : 'info'">
                  {{ selectedNode.status.vrrpState }}
                </el-tag>
              </el-descriptions-item>
            </el-descriptions>

            <template v-if="hasNetworkConfig">
              <h4>{{ t('topology.networkConfig') }}</h4>
              <el-descriptions :column="1" border size="small">
                <el-descriptions-item :label="t('topology.vlan')" v-if="selectedNode.spec.vlanId">
                  {{ selectedNode.spec.vlanId }}
                </el-descriptions-item>
                <el-descriptions-item :label="t('topology.vlanIp')" v-if="selectedNode.spec.vlanIp">
                  {{ selectedNode.spec.vlanIp }}
                </el-descriptions-item>
                <el-descriptions-item :label="t('topology.serverAddress')" v-if="selectedNode.spec.address">
                  {{ selectedNode.spec.address }}
                </el-descriptions-item>
                <el-descriptions-item :label="t('topology.gateway')" v-if="selectedNode.spec.gatewayIp || selectedNode.spec.defaultGateway">
                  {{ selectedNode.spec.gatewayIp || selectedNode.spec.defaultGateway }}
                </el-descriptions-item>
                <el-descriptions-item :label="t('topology.gatewayMac')" v-if="selectedNode.spec.gatewayMac">
                  {{ selectedNode.spec.gatewayMac }}
                </el-descriptions-item>
                <el-descriptions-item :label="t('topology.bgpPeers')" v-if="selectedNode.spec.bgpPeers?.length">
                  {{ selectedNode.spec.bgpPeers.join(', ') }}
                </el-descriptions-item>
              </el-descriptions>
            </template>

            <h4>{{ t('topology.interfaces') }}</h4>
            <el-table :data="ifaceRows" size="small">
              <el-table-column :label="t('topology.interface')" width="120">
                <template #default="{ row }">
                  <div>{{ row.name }}</div>
                  <div class="sub">{{ row.address }}</div>
                </template>
              </el-table-column>
              <el-table-column :label="t('topology.state')" width="70">
                <template #default="{ row }">
                  <el-tag v-if="row.up !== undefined" size="small" effect="plain" :type="row.up ? 'success' : 'danger'">
                    {{ row.up ? 'up' : 'down' }}
                  </el-tag>
                </template>
              </el-table-column>
              <el-table-column :label="t('topology.peer')">
                <template #default="{ row }">
                  <div>{{ row.peer }}<template v-if="row.peerIface">:{{ row.peerIface }}</template></div>
                  <div class="sub">{{ row.peerAddress }}</div>
                </template>
              </el-table-column>
            </el-table>

            <template v-if="bgpRows.length">
              <h4>{{ t('topology.bgpConfig') }}</h4>
              <el-table :data="bgpRows" size="small">
                <el-table-column :label="t('topology.bgpNeighbor')" width="120">
                  <template #default="{ row }">
                    <div>{{ row.address }}</div>
                    <div class="sub">AS {{ row.remoteAs }}</div>
                  </template>
                </el-table-column>
                <el-table-column :label="t('topology.bgpType')" width="70">
                  <template #default="{ row }">
                    <el-tag size="small" effect="plain" :type="row.kind === 'iBGP' ? 'warning' : 'primary'">
                      {{ row.kind }}
                    </el-tag>
                  </template>
                </el-table-column>
                <el-table-column prop="description" :label="t('topology.peer')" />
              </el-table>
            </template>
          </el-tab-pane>

          <el-tab-pane v-if="selectedNode.spec.role === 'server'" name="metrics">
            <template #label>
              <span class="tab-label"><el-icon><TrendCharts /></el-icon>{{ t('topology.metricsView') }}</span>
            </template>
            <div class="runtime-toolbar">
              <span class="runtime-hint">{{ t('topology.metricsHint') }}</span>
              <el-button size="small" :loading="metricsLoading" @click="refreshMetrics">
                {{ t('topology.refresh') }}
              </el-button>
            </div>
            <template v-if="nodeMetrics">
              <div class="metrics-grid">
                <div>
                  <el-descriptions :column="2" border size="small">
                    <el-descriptions-item :label="t('topology.metricsUptime')">
                      {{ fmtUptime(nodeMetrics.uptimeSeconds) }}
                    </el-descriptions-item>
                    <el-descriptions-item :label="t('topology.metricsProcs')">
                      {{ nodeMetrics.procs ?? 0 }}
                    </el-descriptions-item>
                    <el-descriptions-item :label="t('topology.metricsLoad')" :span="2">
                      {{ (nodeMetrics.load?.load1 ?? 0).toFixed(2) }} /
                      {{ (nodeMetrics.load?.load5 ?? 0).toFixed(2) }} /
                      {{ (nodeMetrics.load?.load15 ?? 0).toFixed(2) }}
                      <span class="sub">{{ t('topology.metricsLoadNote') }}</span>
                    </el-descriptions-item>
                  </el-descriptions>

                  <h4>{{ t('topology.metricsUsage') }}</h4>
                  <div v-for="g in gaugeRows" :key="g.name" class="gauge">
                    <div class="gauge-head">
                      <span>{{ g.name }}</span>
                      <span class="sub">{{ g.detail }}</span>
                    </div>
                    <el-progress :percentage="g.percent" :status="gaugeStatus(g.percent)" :show-text="false" />
                  </div>

                  <h4>{{ t('topology.metricsDisk') }}</h4>
                  <el-descriptions :column="2" border size="small">
                    <el-descriptions-item :label="t('topology.metricsRead')">
                      {{ fmtRate(nodeMetrics.disk?.readBytesPerSec) }}
                      <span class="sub">{{ (nodeMetrics.disk?.readOpsPerSec ?? 0).toFixed(1) }} iops</span>
                    </el-descriptions-item>
                    <el-descriptions-item :label="t('topology.metricsWrite')">
                      {{ fmtRate(nodeMetrics.disk?.writeBytesPerSec) }}
                      <span class="sub">{{ (nodeMetrics.disk?.writeOpsPerSec ?? 0).toFixed(1) }} iops</span>
                    </el-descriptions-item>
                  </el-descriptions>
                </div>

                <div>
                  <h4>{{ t('topology.metricsNet') }}</h4>
                  <el-table :data="nodeMetrics.interfaces ?? []" size="small" v-loading="metricsLoading">
                <el-table-column :label="t('topology.interface')" width="110">
                  <template #default="{ row }">{{ row.name }}</template>
                </el-table-column>
                <el-table-column :label="t('topology.metricsRx')">
                  <template #default="{ row }">
                    <div>{{ fmtRate(row.rxBytesPerSec) }}</div>
                    <div class="sub">{{ (row.rxPacketsPerSec ?? 0).toFixed(1) }} pps · Σ {{ fmtBytes(row.rxBytesTotal) }}</div>
                  </template>
                </el-table-column>
                <el-table-column :label="t('topology.metricsTx')">
                  <template #default="{ row }">
                    <div>{{ fmtRate(row.txBytesPerSec) }}</div>
                    <div class="sub">{{ (row.txPacketsPerSec ?? 0).toFixed(1) }} pps · Σ {{ fmtBytes(row.txBytesTotal) }}</div>
                  </template>
                </el-table-column>
                <el-table-column :label="t('topology.metricsErrors')" width="110">
                  <template #default="{ row }">
                    <el-tag
                      size="small"
                      effect="plain"
                      :type="Number(row.rxErrors ?? 0) + Number(row.txErrors ?? 0) + Number(row.rxDropped ?? 0) + Number(row.txDropped ?? 0) > 0 ? 'danger' : 'info'"
                    >
                      {{ Number(row.rxErrors ?? 0) + Number(row.txErrors ?? 0) }} err ·
                      {{ Number(row.rxDropped ?? 0) + Number(row.txDropped ?? 0) }} drop
                    </el-tag>
                  </template>
                </el-table-column>
              </el-table>
                </div>
              </div>

              <h4>{{ t('topology.metricsHistory') }}</h4>
              <div class="runtime-toolbar">
                <el-radio-group v-model="historyRange" size="small">
                  <el-radio-button :value="1800">30m</el-radio-button>
                  <el-radio-button :value="3600">1h</el-radio-button>
                  <el-radio-button :value="7200">2h</el-radio-button>
                  <el-radio-button :value="86400">24h</el-radio-button>
                </el-radio-group>
                <div class="toolbar-right">
                  <el-select v-model="historyIface" size="small" style="width: 110px">
                    <el-option v-for="name in ifaceOptions" :key="name" :label="name" :value="name" />
                  </el-select>
                  <el-button size="small" :loading="historyLoading" @click="refreshHistory()">
                    {{ t('topology.refresh') }}
                  </el-button>
                </div>
              </div>
              <div v-if="historyPoints.length" class="metrics-grid">
                <MetricsChart title="CPU" :series="cpuSeries" unit="percent" />
                <MetricsChart :title="t('topology.metricsMemory')" :series="memSeries" unit="bytes" />
                <MetricsChart :title="`${t('topology.metricsNet')} · ${historyIface}`" :series="netSeries" unit="bytesRate" />
                <MetricsChart :title="t('topology.metricsDisk')" :series="diskSeries" unit="bytesRate" />
              </div>
              <div v-else-if="!historyLoading" class="runtime-hint">
                {{ t('topology.metricsHistoryEmpty') }}
              </div>
            </template>
          </el-tab-pane>

          <el-tab-pane v-if="selectedNode.spec.role === 'server'" name="programs">
            <template #label>
              <span class="tab-label"><el-icon><Box /></el-icon>{{ t('topology.programsView') }}</span>
            </template>
            <div class="runtime-toolbar">
              <span class="runtime-hint">{{ t('topology.programsHint') }}</span>
              <el-button size="small" :loading="inventoryLoading" @click="refreshInventory">
                {{ t('topology.refresh') }}
              </el-button>
            </div>
            <template v-if="nodeInventory">
              <h4>{{ t('programs.title') }}</h4>
              <el-table :data="nodeInventory.programs ?? []" size="small" v-loading="inventoryLoading">
                <el-table-column :label="t('common.name')" width="130">
                  <template #default="{ row }">
                    <div>{{ row.name }}</div>
                    <el-tag v-if="!row.managed" size="small" type="warning">{{ t('topology.nodeLocal') }}</el-tag>
                  </template>
                </el-table-column>
                <el-table-column :label="t('programs.package')">
                  <template #default="{ row }">
                    <code>{{ row.packageName }}@{{ row.packageVersion }}</code>
                    <div class="sub">
                      {{ row.type === 'oneshot' ? t('programs.typeOneshot') : t('programs.typeSimple') }}
                      <template v-if="row.autoStart"> · {{ t('programs.autoStart') }}</template>
                    </div>
                  </template>
                </el-table-column>
                <el-table-column :label="t('programs.state')" width="110">
                  <template #default="{ row }">
                    <el-tag size="small" :type="programStateTag(row.state)">{{ row.state }}</el-tag>
                    <div class="sub" v-if="row.state === 'Running'">pid {{ row.pid }} · ↻{{ row.restarts ?? 0 }}</div>
                  </template>
                </el-table-column>
              </el-table>

              <h4>{{ t('menu.packages') }}</h4>
              <el-table :data="nodeInventory.packages ?? []" size="small" v-loading="inventoryLoading">
                <el-table-column prop="name" :label="t('common.name')" width="150" />
                <el-table-column prop="version" :label="t('packages.version')" width="110" />
                <el-table-column :label="t('packages.digest')">
                  <template #default="{ row }"><code>{{ (row.sha256 ?? '').slice(0, 12) }}</code></template>
                </el-table-column>
              </el-table>
            </template>
          </el-tab-pane>

          <el-tab-pane name="inspect">
            <template #label>
              <span class="tab-label"><el-icon><Search /></el-icon>{{ t('topology.routingView') }}</span>
            </template>
            <el-segmented v-model="inspectView" :options="inspectOptions" block class="inspect-switch" />

            <template v-if="inspectView === 'bgp-table'">
              <div class="runtime-toolbar">
                <span class="runtime-hint" v-if="nodeBGPTable">
                  {{ t('topology.bgpTableHint', {
                    routerId: nodeBGPTable.routerId ?? '—',
                    as: nodeBGPTable.localAs ?? 0,
                    count: nodeBGPTable.paths?.length ?? 0,
                  }) }}
                </span>
                <el-button size="small" :loading="bgpTableLoading" @click="refreshBGPTable">
                  {{ t('topology.refresh') }}
                </el-button>
              </div>
              <el-table v-if="nodeBGPTable" :data="nodeBGPTable.paths ?? []" size="small" v-loading="bgpTableLoading">
                <el-table-column :label="t('topology.routePrefix')" width="130">
                  <template #default="{ row }">
                    <div>{{ row.prefix }}</div>
                    <div class="sub">{{ row.asPath || t('topology.bgpLocalOrigin') }}</div>
                  </template>
                </el-table-column>
                <el-table-column :label="t('topology.bgpFlags')" width="76">
                  <template #default="{ row }">
                    <div class="flags">
                      <el-tag v-if="row.best" size="small" effect="plain" type="success">best</el-tag>
                      <el-tag v-else-if="row.multipath" size="small" effect="plain" type="primary">multi</el-tag>
                      <el-tag v-if="row.internal" size="small" effect="plain" type="warning">iBGP</el-tag>
                      <el-tag v-if="row.valid === false" size="small" effect="plain" type="danger">invalid</el-tag>
                    </div>
                  </template>
                </el-table-column>
                <el-table-column :label="t('topology.routeNexthop')">
                  <template #default="{ row }">
                    <div>{{ row.nexthop }}</div>
                    <div class="sub">{{ row.nexthopName }}</div>
                  </template>
                </el-table-column>
              </el-table>
            </template>

            <template v-else-if="inspectView === 'routes'">
              <div class="runtime-toolbar">
                <span class="runtime-hint" v-if="nodeRoutes">
                  {{ t('topology.routesCount', { count: nodeRoutes.routes?.length ?? 0 }) }}
                </span>
                <el-button size="small" :loading="routesLoading" @click="refreshRoutes">
                  {{ t('topology.refresh') }}
                </el-button>
              </div>
              <el-table v-if="nodeRoutes" :data="nodeRoutes.routes ?? []" size="small" v-loading="routesLoading">
                <el-table-column :label="t('topology.routePrefix')" width="130">
                  <template #default="{ row }">
                    <div>{{ row.prefix }}</div>
                    <div class="sub">AD {{ row.distance ?? 0 }} / M {{ row.metric ?? 0 }}</div>
                  </template>
                </el-table-column>
                <el-table-column :label="t('topology.routeProtocol')" width="100">
                  <template #default="{ row }">
                    <el-tag
                      size="small"
                      effect="plain"
                      :type="row.protocol === 'bgp' ? 'primary' : row.protocol === 'connected' ? 'success' : 'info'"
                    >
                      {{ row.protocol }}
                    </el-tag>
                  </template>
                </el-table-column>
                <el-table-column :label="t('topology.routeNexthop')">
                  <template #default="{ row }">
                    <div v-for="(nh, i) in row.nexthops ?? []" :key="i">
                      <template v-if="nh.via">{{ nh.via }} </template>
                      <span class="sub">{{ nh.interface }}</span>
                    </div>
                  </template>
                </el-table-column>
              </el-table>
            </template>

            <template v-else>
              <div class="runtime-toolbar">
                <span class="runtime-hint" v-if="nodeFIB">
                  {{ t('topology.fibCount', fibCounts) }}
                </span>
                <el-button size="small" :loading="fibLoading" @click="refreshFIB">
                  {{ t('topology.refresh') }}
                </el-button>
              </div>
              <el-table v-if="nodeFIB" :data="nodeFIB.routes ?? []" size="small" v-loading="fibLoading">
                <el-table-column :label="t('topology.routePrefix')" width="130">
                  <template #default="{ row }">
                    <div>{{ row.prefix }}</div>
                    <div class="sub">M {{ row.metric ?? 0 }}</div>
                  </template>
                </el-table-column>
                <el-table-column :label="t('topology.routeProtocol')" width="100">
                  <template #default="{ row }">
                    <div class="flags">
                      <el-tag
                        size="small"
                        effect="plain"
                        :type="row.protocol === 'bgp' ? 'primary' : row.protocol === 'kernel' ? 'success' : 'info'"
                      >
                        {{ row.protocol }}
                      </el-tag>
                      <el-tag v-if="row.kind === 'local'" size="small" effect="plain" type="warning">local</el-tag>
                    </div>
                  </template>
                </el-table-column>
                <el-table-column :label="t('topology.routeNexthop')">
                  <template #default="{ row }">
                    <div v-for="(nh, i) in row.nexthops ?? []" :key="i">
                      <template v-if="nh.via">{{ nh.via }} </template>
                      <span class="sub">{{ nh.interface }}</span>
                    </div>
                  </template>
                </el-table-column>
              </el-table>
            </template>

          </el-tab-pane>

          <el-tab-pane name="runtime">
            <template #label>
              <span class="tab-label"><el-icon><Monitor /></el-icon>{{ t('topology.runtimeView') }}</span>
            </template>
            <div class="runtime-toolbar">
              <span class="runtime-hint">{{ t('topology.runtimeHint') }}</span>
              <el-button size="small" :loading="runtimeLoading" @click="refreshRuntime">
                {{ t('topology.refresh') }}
              </el-button>
            </div>
            <template v-if="nodeRuntime">
              <el-descriptions :column="1" border size="small">
                <el-descriptions-item :label="t('topology.containerState')">
                  {{ nodeRuntime.containerState }}
                </el-descriptions-item>
              </el-descriptions>

              <h4>{{ t('topology.interfaces') }}</h4>
              <el-table :data="nodeRuntime.interfaces ?? []" size="small" v-loading="runtimeLoading">
                <el-table-column :label="t('topology.interface')" width="120">
                  <template #default="{ row }">
                    <div>{{ row.name }}</div>
                    <div class="sub">{{ row.mac }}</div>
                  </template>
                </el-table-column>
                <el-table-column prop="state" :label="t('topology.state')" width="90" />
                <el-table-column :label="t('topology.address')">
                  <template #default="{ row }">
                    <div v-for="a in row.addresses ?? []" :key="a">{{ a }}</div>
                  </template>
                </el-table-column>
              </el-table>
            </template>
          </el-tab-pane>

          <el-tab-pane name="diagnose">
            <template #label>
              <span class="tab-label"><el-icon><Position /></el-icon>{{ t('mtr.title') }}</span>
            </template>
            <el-form label-width="90px">
              <el-form-item :label="t('mtr.mode')">
                <el-radio-group v-model="mtrMode">
                  <el-radio-button value="single">{{ t('mtr.modeSingle') }}</el-radio-button>
                  <el-radio-button value="scan">{{ t('mtr.modeScan') }}</el-radio-button>
                </el-radio-group>
              </el-form-item>
              <el-form-item :label="t('mtr.targetMode')">
                <el-radio-group v-model="mtrTargetMode">
                  <el-radio-button value="node">{{ t('mtr.targetModeNode') }}</el-radio-button>
                  <el-radio-button value="custom">{{ t('mtr.targetModeCustom') }}</el-radio-button>
                </el-radio-group>
              </el-form-item>
              <el-form-item v-if="mtrTargetMode === 'node'" :label="t('common.name')">
                <el-select v-model="mtrTargetNodeId" filterable style="width: 100%" :placeholder="t('mtr.pickTargetNode')">
                  <el-option v-for="o in mtrTargetOptions" :key="o.value" :label="o.label" :value="o.value" />
                </el-select>
              </el-form-item>
              <el-form-item v-else :label="t('common.name')">
                <el-input v-model="mtrTargetCustom" :placeholder="t('mtr.targetCustomPlaceholder')" />
              </el-form-item>
              <el-form-item :label="t('mtr.protocol')">
                <el-radio-group v-model="mtrProtocol">
                  <el-radio-button value="icmp" :disabled="mtrMode === 'scan'">ICMP</el-radio-button>
                  <el-radio-button value="tcp">TCP</el-radio-button>
                  <el-radio-button value="udp">UDP</el-radio-button>
                </el-radio-group>
              </el-form-item>
              <el-form-item v-if="mtrProtocol !== 'icmp'" :label="t('mtr.port')">
                <el-input-number v-model="mtrPort" :min="1" :max="65535" controls-position="right" :placeholder="t('mtr.portPlaceholder')" />
              </el-form-item>
              <el-form-item v-if="mtrMode === 'single'" :label="t('mtr.cycles')">
                <el-input-number v-model="mtrCycles" :min="1" :max="30" controls-position="right" />
              </el-form-item>
              <el-form-item v-else :label="t('mtr.scanSamples')">
                <el-input-number v-model="mtrScanSamples" :min="2" :max="20" controls-position="right" />
              </el-form-item>
            </el-form>
            <div class="capture-quick-footer">
              <span class="sub">{{ mtrMode === 'scan' ? t('mtr.scanHint') : t('mtr.hint') }}</span>
              <span>
                <el-button
                  v-if="mtrMode === 'single'"
                  size="small"
                  :loading="mtrLoading"
                  :disabled="mtrTargetMode === 'node' ? !mtrTargetNodeId : !mtrTargetCustom"
                  @click="runDiagnose(1)"
                >
                  {{ t('mtr.quickPing') }}
                </el-button>
                <el-button
                  type="primary"
                  size="small"
                  :loading="mtrLoading"
                  :disabled="mtrTargetMode === 'node' ? !mtrTargetNodeId : !mtrTargetCustom"
                  @click="mtrMode === 'scan' ? runDiagnoseScan() : runDiagnose()"
                >
                  {{ mtrMode === 'scan' ? t('mtr.runScan') : t('mtr.run') }}
                </el-button>
              </span>
            </div>

            <template v-if="mtrScan">
              <el-alert v-if="mtrScan.containerState !== 'running'" type="warning" :closable="false" show-icon :title="t('mtr.notRunning')" />
              <template v-else>
                <div class="sub mtr-scan-summary">{{ t('mtr.scanSummary', { paths: mtrScan.paths?.length ?? 0, samples: mtrScan.samplesRun }) }}</div>
                <div v-for="(p, i) in mtrScan.paths ?? []" :key="i" class="mtr-scan-path">
                  <div class="mtr-scan-path-header">
                    <span class="mtr-path-swatch" :style="{ background: mtrScanPalette[i % mtrScanPalette.length] }" />
                    <b>{{ t('mtr.scanPath', { n: i + 1 }) }}</b>
                    <el-tag size="small" disable-transitions>{{ t('mtr.scanCount', { count: p.count }, p.count) }}</el-tag>
                    <span class="sub">{{ (p.hops ?? []).filter((h) => h.nodeName).map((h) => h.nodeName).join(' → ') }}</span>
                  </div>
                </div>
              </template>
            </template>

            <template v-else-if="nodeMTR">
              <el-alert v-if="nodeMTR.containerState !== 'running'" type="warning" :closable="false" show-icon :title="t('mtr.notRunning')" />
              <template v-else>
                <div v-if="mtrPrevious" class="sub mtr-compare-hint">
                  <span class="mtr-path-swatch" style="background: #722ed1" />{{ t('mtr.currentPath') }}
                  <span class="mtr-path-swatch mtr-swatch-dashed" style="background: #b37feb" />{{ t('mtr.previousPath') }}
                </div>
                <el-table :data="nodeMTR.hops ?? []" size="small" class="mtr-hop-table" @row-click="toggleMTRFocus">
                  <el-table-column :label="t('mtr.hop')" prop="ttl" width="50" />
                  <el-table-column :label="t('mtr.device')" width="130">
                    <template #default="{ row }">
                      <span v-if="row.nodeName" :class="{ 'mtr-focused': row.nodeId === mtrFocusNodeId }">{{ row.nodeName }} <el-tag size="small" :color="roleColor[row.nodeRole]" effect="dark" disable-transitions>{{ row.nodeRole }}</el-tag></span>
                      <span v-else class="sub">—</span>
                    </template>
                  </el-table-column>
                  <el-table-column :label="t('mtr.host')" width="120">
                    <template #default="{ row }">
                      <span v-if="row.timeout" class="sub">{{ t('mtr.timeout') }}</span>
                      <span v-else>{{ row.host }}</span>
                    </template>
                  </el-table-column>
                  <el-table-column :label="t('mtr.loss')" width="70">
                    <template #default="{ row }">{{ row.lossPercent?.toFixed(0) }}%</template>
                  </el-table-column>
                  <el-table-column :label="t('mtr.last')" width="70">
                    <template #default="{ row }">{{ row.lastMs?.toFixed(1) }}</template>
                  </el-table-column>
                  <el-table-column :label="t('mtr.avg')" width="70">
                    <template #default="{ row }">{{ row.avgMs?.toFixed(1) }}</template>
                  </el-table-column>
                  <el-table-column :label="t('mtr.best')" width="70">
                    <template #default="{ row }">{{ row.bestMs?.toFixed(1) }}</template>
                  </el-table-column>
                  <el-table-column :label="t('mtr.worst')" width="70">
                    <template #default="{ row }">{{ row.worstMs?.toFixed(1) }}</template>
                  </el-table-column>
                  <el-table-column :label="t('mtr.stddev')" width="70">
                    <template #default="{ row }">{{ row.stddevMs?.toFixed(1) }}</template>
                  </el-table-column>
                </el-table>
                <div class="sub mtr-row-hint">{{ t('mtr.rowClickHint') }}</div>
              </template>
            </template>
          </el-tab-pane>

          <el-tab-pane name="fault">
            <template #label>
              <span class="tab-label"><el-icon><Warning /></el-icon>{{ t('faults.title') }}</span>
            </template>
            <div class="runtime-toolbar">
              <el-button size="small" :loading="faultBusy" @click="quickNodeFault('node-stop')">
                {{ t('faults.typeNodeStop') }}
              </el-button>
              <el-button size="small" :loading="faultBusy" @click="quickNodeFault('node-restart')">
                {{ t('faults.typeNodeRestart') }}
              </el-button>
            </div>
            <el-table v-if="nodeFaults.length" :data="nodeFaults" size="small">
              <el-table-column prop="meta.name" :label="t('common.name')" />
              <el-table-column :label="t('faults.type')">
                <template #default="{ row }">{{ faultTypeLabel(row.spec.type) }}</template>
              </el-table-column>
              <el-table-column :label="t('faults.state')" width="90">
                <template #default="{ row }">
                  <el-tag size="small" :type="row.status.applied ? 'danger' : 'info'">
                    {{ row.status.applied ? t('faults.applied') : t('faults.recovered') }}
                  </el-tag>
                </template>
              </el-table-column>
              <el-table-column :label="t('common.actions')" width="150">
                <template #default="{ row }">
                  <el-button
                    v-if="row.spec.type !== 'node-restart' || !row.status.applied"
                    size="small"
                    :type="row.status.applied ? 'success' : 'danger'"
                    :loading="faultBusy"
                    @click="toggleFault(row)"
                  >
                    {{ row.status.applied ? t('faults.recover') : t('faults.apply') }}
                  </el-button>
                  <el-button size="small" type="danger" plain :loading="faultBusy" @click="deleteFault(row)">
                    {{ t('common.delete') }}
                  </el-button>
                </template>
              </el-table-column>
            </el-table>
            <el-empty v-else :description="t('faults.recovered')" />
          </el-tab-pane>

          <el-tab-pane name="capture">
            <template #label>
              <span class="tab-label"><el-icon><Aim /></el-icon>{{ t('captures.quickTitle') }}</span>
            </template>
            <el-form label-width="100px">
              <el-form-item :label="t('captures.interface')">
                <el-select v-model="captureIface" style="width: 100%" :placeholder="t('captures.pickInterface')">
                  <el-option v-for="i in captureIfaceOptions" :key="i.value" :label="i.label" :value="i.value" />
                </el-select>
              </el-form-item>
              <el-form-item :label="t('captures.protocol')">
                <el-select v-model="captureProto" clearable style="width: 100%" :placeholder="t('captures.anyProtocol')">
                  <el-option v-for="p in ['arp', 'icmp', 'tcp', 'udp', 'bgp', 'vxlan']" :key="p" :label="p.toUpperCase()" :value="p" />
                </el-select>
              </el-form-item>
              <el-form-item :label="t('captures.durationSeconds')">
                <el-input-number v-model="captureDuration" :min="1" :max="600" controls-position="right" />
              </el-form-item>
            </el-form>
            <div class="capture-quick-footer">
              <span class="sub">{{ t('captures.quickHint') }}</span>
              <el-button type="primary" size="small" :loading="captureBusy" :disabled="!captureIface" @click="quickNodeCapture">
                {{ t('captures.quickStart') }}
              </el-button>
            </div>
          </el-tab-pane>
        </el-tabs>
      </template>
    </el-drawer>

    <el-drawer :model-value="!!selectedLink" size="540px" :modal="false" @close="selectedLink = null">
      <template #header>
        <div v-if="selectedLink" class="drawer-head">
          <div class="drawer-id">
            <span class="drawer-name" :title="selectedLink.meta.name">{{ selectedLink.meta.name }}</span>
            <el-tag size="small" effect="plain" class="kind-tag">{{ linkKindLabel(selectedLink) }}</el-tag>
          </div>
        </div>
      </template>
      <template v-if="selectedLink">
        <div class="link-endpoints">
          <div class="endpoint">
            <div class="endpoint-node" :title="selectedLink.spec.endpointA.nodeName">{{ selectedLink.spec.endpointA.nodeName }}</div>
            <div class="endpoint-iface">{{ selectedLink.spec.endpointA.interface }}</div>
            <div v-if="selectedLink.spec.endpointA.address" class="endpoint-addr">
              {{ selectedLink.spec.endpointA.address }}
            </div>
          </div>
          <div class="link-wire">
            <span class="wire-line" />
            <span class="wire-mtu">MTU {{ selectedLink.spec.mtu }}</span>
          </div>
          <div class="endpoint">
            <div class="endpoint-node" :title="selectedLink.spec.endpointB.nodeName">{{ selectedLink.spec.endpointB.nodeName }}</div>
            <div class="endpoint-iface">{{ selectedLink.spec.endpointB.interface }}</div>
            <div v-if="selectedLink.spec.endpointB.address" class="endpoint-addr">
              {{ selectedLink.spec.endpointB.address }}
            </div>
          </div>
        </div>
        <div
          v-if="!selectedLink.spec.endpointA.address && !selectedLink.spec.endpointB.address"
          class="observed-at l2-note"
        >
          {{ t('topology.l2NoAddress') }}
        </div>

        <h4>{{ t('mtr.title') }}</h4>
        <div class="fault-actions">
          <el-button
            size="small"
            :loading="linkMTRLoading"
            @click="probeLinkEndpoint(selectedLink.spec.endpointA, selectedLink.spec.endpointB)"
          >
            {{ t('mtr.probeEndpoint', { from: selectedLink.spec.endpointA.nodeName, to: selectedLink.spec.endpointB.nodeName }) }}
          </el-button>
          <el-button
            size="small"
            :loading="linkMTRLoading"
            @click="probeLinkEndpoint(selectedLink.spec.endpointB, selectedLink.spec.endpointA)"
          >
            {{ t('mtr.probeEndpoint', { from: selectedLink.spec.endpointB.nodeName, to: selectedLink.spec.endpointA.nodeName }) }}
          </el-button>
        </div>
        <template v-if="linkMTR">
          <el-alert v-if="linkMTR.containerState !== 'running'" type="warning" :closable="false" show-icon :title="t('mtr.notRunning')" />
          <el-table v-else :data="linkMTR.hops ?? []" size="small">
            <el-table-column :label="t('mtr.hop')" prop="ttl" width="50" />
            <el-table-column :label="t('mtr.device')">
              <template #default="{ row }">
                <span v-if="row.nodeName">{{ row.nodeName }}</span>
                <span v-else-if="row.timeout" class="sub">{{ t('mtr.timeout') }}</span>
                <span v-else>{{ row.host }}</span>
              </template>
            </el-table-column>
            <el-table-column :label="t('mtr.loss')" width="70">
              <template #default="{ row }">{{ row.lossPercent?.toFixed(0) }}%</template>
            </el-table-column>
            <el-table-column :label="t('mtr.avg')" width="80">
              <template #default="{ row }">{{ row.avgMs?.toFixed(1) }}</template>
            </el-table-column>
            <el-table-column :label="t('mtr.worst')" width="80">
              <template #default="{ row }">{{ row.worstMs?.toFixed(1) }}</template>
            </el-table-column>
          </el-table>
        </template>

        <h4>{{ t('faults.title') }}</h4>
        <div class="fault-actions">
          <el-button size="small" :loading="faultBusy" @click="quickLinkFault('link-down')">
            {{ t('faults.typeLinkDown') }}
          </el-button>
          <el-button size="small" :loading="faultBusy" @click="quickLinkFault('interface-down', 'a')">
            {{ t('faults.typeInterfaceDown') }}: {{ selectedLink.spec.endpointA.nodeName }}
          </el-button>
          <el-button size="small" :loading="faultBusy" @click="quickLinkFault('interface-down', 'b')">
            {{ t('faults.typeInterfaceDown') }}: {{ selectedLink.spec.endpointB.nodeName }}
          </el-button>
        </div>
        <div class="fault-impairment">
          <el-input-number v-model="impairForm.delayMs" :min="0" :max="60000" :placeholder="t('faults.delayMs')" size="small" controls-position="right" />
          <el-input-number v-model="impairForm.lossPercent" :min="0" :max="100" :step="0.5" :placeholder="t('faults.lossPercent')" size="small" controls-position="right" />
          <el-input-number v-model="impairForm.rateKbit" :min="0" :max="10000000" :placeholder="t('faults.rateKbit')" size="small" controls-position="right" />
          <el-button size="small" type="warning" :loading="faultBusy" @click="quickImpairment">
            {{ t('faults.typeImpairment') }}
          </el-button>
        </div>

        <el-table v-if="linkFaults.length" :data="linkFaults" size="small">
          <el-table-column prop="meta.name" :label="t('common.name')" />
          <el-table-column :label="t('faults.type')">
            <template #default="{ row }">{{ faultTypeLabel(row.spec.type) }}</template>
          </el-table-column>
          <el-table-column :label="t('faults.state')" width="90">
            <template #default="{ row }">
              <el-tag size="small" :type="row.status.applied ? 'danger' : 'info'">
                {{ row.status.applied ? t('faults.applied') : t('faults.recovered') }}
              </el-tag>
            </template>
          </el-table-column>
          <el-table-column :label="t('common.actions')" width="150">
            <template #default="{ row }">
              <el-button
                size="small"
                :type="row.status.applied ? 'success' : 'danger'"
                :loading="faultBusy"
                @click="toggleFault(row)"
              >
                {{ row.status.applied ? t('faults.recover') : t('faults.apply') }}
              </el-button>
              <el-button size="small" type="danger" plain :loading="faultBusy" @click="deleteFault(row)">
                {{ t('common.delete') }}
              </el-button>
            </template>
          </el-table-column>
        </el-table>

        <h4>{{ t('captures.quickTitle') }}</h4>
        <div class="fault-actions">
          <el-button size="small" :loading="captureBusy" @click="quickEndpointCapture(selectedLink.spec.endpointA)">
            {{ t('captures.captureEndpoint', { name: selectedLink.spec.endpointA.nodeName }) }}
          </el-button>
          <el-button size="small" :loading="captureBusy" @click="quickEndpointCapture(selectedLink.spec.endpointB)">
            {{ t('captures.captureEndpoint', { name: selectedLink.spec.endpointB.nodeName }) }}
          </el-button>
        </div>
      </template>
    </el-drawer>
  </div>
</template>

<style scoped>
/* Fill the viewport: el-main has 20px padding top and bottom. */
.page {
  height: calc(100vh - 40px);
  display: flex;
  flex-direction: column;
}
.header { display: flex; justify-content: space-between; align-items: center; margin-bottom: 12px; }
.header-actions { display: flex; gap: 12px; align-items: center; }
.header h2 { margin: 0; }
.hint { font-size: 12px; font-weight: normal; color: var(--el-text-color-secondary); margin-left: 8px; }
.drift-banner { margin-bottom: 12px; }
.scale-banner { margin-bottom: 12px; }
.scale-banner-body { display: flex; align-items: center; justify-content: space-between; gap: 12px; }
.scale-hint { font-size: 12px; color: var(--el-text-color-secondary); }
.scale-actions { display: inline-flex; gap: 8px; flex-shrink: 0; }
.drift-body { display: flex; align-items: center; gap: 12px; margin-top: 4px; }
.drift-nodes { font-size: 13px; color: var(--el-text-color-secondary); }
.drift-why { margin-left: 6px; vertical-align: -2px; cursor: help; color: var(--el-text-color-secondary); }
/* The non-modal drawers still mount a full-viewport positioning
   wrapper that swallows clicks (breaking canvas double-click); let
   events pass through it and keep only the panel interactive. */
.page :deep(.el-modal-drawer),
.page :deep(.el-overlay) { pointer-events: none; }
.page :deep(.el-drawer) { pointer-events: auto; }
/* The scale plan-preview dialog renders in place (inside .page), so
   it needs the same escape hatch as the drawer panel above. */
.page :deep(.el-dialog) { pointer-events: auto; }
.body { flex: 1; min-height: 0; }

/* Drawer chrome: a compact identity header (name + role + health)
   instead of the stock oversized title, with a divider to the body. */
.page :deep(.el-drawer__header) { margin-bottom: 0; padding: 14px 20px; border-bottom: 1px solid var(--el-border-color-lighter); }
.page :deep(.el-drawer__body) { padding: 16px 20px; }
.drawer-head { flex: 1; min-width: 0; display: flex; align-items: center; justify-content: space-between; gap: 10px; margin-right: 8px; }
.drawer-id { display: flex; align-items: center; gap: 8px; min-width: 0; }
.drawer-name { font-size: 16px; font-weight: 600; color: var(--el-text-color-primary); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.role-tag { border: none; }
.kind-tag { max-width: 220px; }
.health-chip { display: inline-flex; align-items: center; gap: 5px; font-size: 12px; color: var(--el-text-color-regular); background: var(--el-fill-color-light); border-radius: 10px; padding: 2px 8px; white-space: nowrap; cursor: default; }
.health-dot { width: 8px; height: 8px; border-radius: 50%; flex-shrink: 0; }

/* Section titles: small muted label with a trailing hairline. */
h4 { display: flex; align-items: center; gap: 8px; margin: 18px 0 8px; font-size: 12px; font-weight: 600; color: var(--el-text-color-secondary); }
h4::after { content: ''; flex: 1; height: 1px; background: var(--el-border-color-lighter); }

/* Overview stat tiles. */
.stat-tiles { display: grid; grid-template-columns: repeat(3, 1fr); gap: 8px; }
.stat-tile { background: var(--el-fill-color-light); border-radius: 8px; padding: 10px 12px; }
.stat-value { font-size: 18px; font-weight: 600; font-variant-numeric: tabular-nums; color: var(--el-text-color-primary); }
.stat-tile.ok .stat-value { color: var(--el-color-success); }
.stat-tile.warn .stat-value { color: var(--el-color-warning); }
.stat-tile.bad .stat-value { color: var(--el-color-danger); }
.stat-label { margin-top: 2px; font-size: 11px; color: var(--el-text-color-secondary); }
.observed-at { margin-top: 6px; text-align: right; font-size: 11px; color: var(--el-text-color-secondary); }

/* Query tab view switch. */
.inspect-switch { margin-bottom: 12px; }
.tab-label { display: inline-flex; align-items: center; gap: 4px; }
.tab-label .el-icon { font-size: 14px; }
/* Icons widen the labels; tighten item padding so all tabs fit the
   compact drawer without scroll arrows. */
.page :deep(.el-tabs__item) { padding: 0 9px; }

/* Link drawer: the two endpoints drawn as cards joined by a wire. */
.link-endpoints { display: flex; align-items: stretch; gap: 10px; }
.endpoint { flex: 1; min-width: 0; background: var(--el-fill-color-light); border-radius: 8px; padding: 10px 12px; }
.endpoint-node { font-size: 13px; font-weight: 600; color: var(--el-text-color-primary); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.endpoint-iface { margin-top: 2px; font-size: 12px; color: var(--el-text-color-regular); }
.endpoint-addr { margin-top: 2px; font-size: 11px; color: var(--el-text-color-secondary); font-variant-numeric: tabular-nums; }
.link-wire { display: flex; flex-direction: column; align-items: center; justify-content: center; gap: 4px; min-width: 64px; }
.wire-line { width: 100%; height: 2px; border-radius: 1px; background: var(--el-border-color); }
.wire-mtu { font-size: 11px; color: var(--el-text-color-secondary); white-space: nowrap; }
.l2-note { text-align: left; }
.runtime-toolbar { display: flex; justify-content: space-between; align-items: center; gap: 8px; margin-bottom: 12px; }
.runtime-hint { font-size: 12px; color: var(--el-text-color-secondary); }
.sub { font-size: 11px; color: var(--el-text-color-secondary); }
.gauge { margin-bottom: 10px; }
.gauge-head { display: flex; justify-content: space-between; align-items: baseline; margin-bottom: 2px; font-size: 13px; }
.toolbar-right { display: flex; gap: 8px; align-items: center; }
/* One column in the compact drawer, two side by side once the
   metrics tab widens it; the drawer itself is capped so ultrawide
   screens don't stretch the charts. */
.metrics-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(340px, 1fr)); column-gap: 20px; align-items: start; }
.page :deep(.el-drawer) { max-width: 900px; }
.flags { display: flex; flex-direction: column; align-items: flex-start; gap: 2px; }
.fault-actions { display: flex; flex-wrap: wrap; gap: 8px; margin-bottom: 8px; }
.capture-quick-footer { display: flex; justify-content: space-between; align-items: center; gap: 8px; }
.fault-impairment { display: flex; flex-wrap: wrap; gap: 8px; align-items: center; margin-bottom: 12px; }
.fault-impairment .el-input-number { width: 130px; }
.mtr-path-swatch {
  display: inline-block;
  width: 18px;
  height: 4px;
  border-radius: 2px;
  vertical-align: middle;
  margin-right: 4px;
}
.mtr-swatch-dashed { background-image: linear-gradient(90deg, transparent 33%, var(--el-bg-color) 33%, var(--el-bg-color) 66%, transparent 66%); }
.mtr-compare-hint { display: flex; align-items: center; gap: 6px; margin: 8px 0 4px; }
.mtr-scan-summary { margin: 8px 0 4px; }
.mtr-scan-path { padding: 6px 0; border-bottom: 1px solid var(--el-border-color-lighter); }
.mtr-scan-path-header { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
.mtr-hop-table :deep(.el-table__row) { cursor: pointer; }
.mtr-focused { font-weight: 700; color: #722ed1; }
.mtr-row-hint { margin-top: 4px; }
</style>

<style>
/* el-popover teleports its popper to <body>, outside this component's
   scoped tree, so the drift-reason body style has to live in an
   unscoped block. The scale context menu teleports to <body> as well. */
.drift-why-body { font-size: 12px; line-height: 1.7; }

.scale-menu {
  position: fixed;
  z-index: 3000;
  min-width: 200px;
  padding: 4px;
  border: 1px solid var(--el-border-color-light);
  border-radius: 6px;
  background: var(--el-bg-color-overlay);
  box-shadow: var(--el-box-shadow-light);
  font-size: 13px;
}
.scale-menu-item {
  padding: 7px 12px;
  border-radius: 4px;
  cursor: pointer;
  display: flex;
  flex-direction: column;
  gap: 2px;
}
.scale-menu-item:hover { background: var(--el-fill-color-light); }
.scale-menu-item.disabled { color: var(--el-text-color-disabled); cursor: not-allowed; }
.scale-menu-item.disabled:hover { background: none; }
.scale-menu-item.danger { color: var(--el-color-danger); }
.scale-menu-tip { font-size: 11px; color: var(--el-text-color-secondary); white-space: normal; max-width: 240px; }
</style>
