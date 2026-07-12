<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { ElMessage } from 'element-plus'
import { useI18n } from 'vue-i18n'
import { useLabStore } from '../stores/lab'
import { labApi } from '../api/lab'
import type { Link, MetricsPoint, Node, NodeBGP, NodeBGPTable, NodeInventory, NodeMetrics, NodeRoutes, NodeRuntime } from '../types/models'
import TopologyCanvas from '../components/TopologyCanvas.vue'
import TerminalPanel from '../components/TerminalPanel.vue'
import MetricsChart, { type ChartSeries } from '../components/MetricsChart.vue'

const store = useLabStore()
const { t } = useI18n()
const selectedNode = ref<Node | null>(null)
const selectedLink = ref<Link | null>(null)
const terminal = ref<InstanceType<typeof TerminalPanel> | null>(null)

onMounted(async () => {
  if (store.labs.length === 0) await store.refreshLabs()
  await store.refreshTopology()
  store.startObserving()
})
onBeforeUnmount(() => store.stopObserving())

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

// Opening the drawer for another node resets to the simulated view;
// observation sweeps replace the node object with the same id and
// must not reset it.
watch(selectedNode, (node, prev) => {
  if (!node || node.meta.id !== prev?.meta.id) {
    drawerTab.value = 'sim'
    nodeRuntime.value = null
    nodeRoutes.value = null
    nodeBGPTable.value = null
    nodeFIB.value = null
    nodeInventory.value = null
    nodeMetrics.value = null
    historyPoints.value = []
    historyIface.value = ''
    nodeBGP.value = null
    if (node) void loadBGP(node)
  }
})

watch(drawerTab, (tab) => {
  if (tab === 'runtime') void refreshRuntime()
  if (tab === 'routes') void refreshRoutes()
  if (tab === 'bgp-table') void refreshBGPTable()
  if (tab === 'fib') void refreshFIB()
  if (tab === 'programs') void refreshInventory()
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
</script>

<template>
  <div class="page">
    <div class="header">
      <h2>{{ t('topology.title') }} <span class="hint">{{ t('topology.doubleClickHint') }}</span></h2>
      <div class="header-actions">
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

    <div class="body">
      <el-empty v-if="store.nodes.length === 0" :description="t('topology.empty')" />
      <TopologyCanvas
        v-else
        :nodes="store.nodes"
        :links="store.links"
        @select-node="selectedNode = $event; selectedLink = null"
        @select-link="selectedLink = $event; selectedNode = null"
        @select-none="selectedNode = null; selectedLink = null"
        @open-terminal="onOpenTerminal"
      />
    </div>

    <TerminalPanel ref="terminal" />

    <!-- Non-modal: a modal mask would swallow the second click of a
         double-click on the canvas, breaking terminal access. -->
    <!-- The metrics tab draws time-series charts and gets a wide
         drawer (capped by CSS max-width); every other tab keeps the
         compact width so the canvas stays usable. -->
    <el-drawer
      :model-value="!!selectedNode"
      :title="selectedNode?.meta.name"
      :size="drawerTab === 'metrics' ? '60%' : '420px'"
      :modal="false"
      @close="selectedNode = null"
    >
      <template v-if="selectedNode">
        <el-button
          v-if="deployed"
          :type="selectedNode.meta.phase === 'Stopped' ? 'success' : 'danger'"
          :loading="nodeBusy"
          size="small"
          class="node-power"
          @click="powerNode"
        >
          {{ selectedNode.meta.phase === 'Stopped' ? t('topology.startNode') : t('topology.stopNode') }}
        </el-button>
        <el-tabs v-model="drawerTab">
          <el-tab-pane :label="t('topology.overview')" name="sim">
            <el-descriptions :column="1" border size="small">
              <el-descriptions-item :label="t('topology.role')">{{ selectedNode.spec.role }}</el-descriptions-item>
              <el-descriptions-item :label="t('topology.phase')">{{ selectedNode.meta.phase }}</el-descriptions-item>
              <template v-if="selectedNode.status?.lastObserved">
                <el-descriptions-item :label="t('topology.runtimeState')">
                  {{ selectedNode.status.runtimeState }}
                </el-descriptions-item>
                <el-descriptions-item :label="t('topology.bgpSessions')">
                  {{ selectedNode.status.bgpEstablished }} / {{ selectedNode.status.bgpConfigured }}
                </el-descriptions-item>
                <el-descriptions-item :label="t('topology.routes')">
                  {{ selectedNode.status.routeCount }}
                </el-descriptions-item>
                <el-descriptions-item :label="t('topology.interfacesUp')">
                  {{ selectedNode.status.interfacesUp }} / {{ selectedNode.status.interfacesTotal }}
                </el-descriptions-item>
                <el-descriptions-item :label="t('topology.gatewayRole')" v-if="selectedNode.status.vrrpState">
                  <el-tag size="small" :type="selectedNode.status.vrrpState === 'Master' ? 'success' : 'info'">
                    {{ selectedNode.status.vrrpState }}
                  </el-tag>
                </el-descriptions-item>
                <el-descriptions-item :label="t('topology.lastObserved')">
                  {{ new Date(selectedNode.status.lastObserved).toLocaleTimeString() }}
                </el-descriptions-item>
              </template>
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
                {{ selectedNode.spec.gatewayIp ?? selectedNode.spec.defaultGateway }}
              </el-descriptions-item>
              <el-descriptions-item :label="t('topology.gatewayMac')" v-if="selectedNode.spec.gatewayMac">
                {{ selectedNode.spec.gatewayMac }}
              </el-descriptions-item>
              <el-descriptions-item :label="t('topology.bgpPeers')" v-if="selectedNode.spec.bgpPeers?.length">
                {{ selectedNode.spec.bgpPeers.join(', ') }}
              </el-descriptions-item>
            </el-descriptions>

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

          <el-tab-pane
            v-if="selectedNode.spec.role === 'server'"
            :label="t('topology.metricsView')"
            name="metrics"
          >
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

          <el-tab-pane
            v-if="selectedNode.spec.role === 'server'"
            :label="t('topology.programsView')"
            name="programs"
          >
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

          <el-tab-pane :label="t('topology.bgpTableView')" name="bgp-table">
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
          </el-tab-pane>

          <el-tab-pane :label="t('topology.routesView')" name="routes">
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
          </el-tab-pane>

          <el-tab-pane :label="t('topology.fibView')" name="fib">
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
          </el-tab-pane>

          <el-tab-pane :label="t('topology.runtimeView')" name="runtime">
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
        </el-tabs>
      </template>
    </el-drawer>

    <el-drawer :model-value="!!selectedLink" :title="selectedLink?.meta.name" size="420px" :modal="false" @close="selectedLink = null">
      <template v-if="selectedLink">
        <el-descriptions :column="1" border size="small">
          <el-descriptions-item :label="t('topology.kind')">
            {{ linkKindLabel(selectedLink) }}
          </el-descriptions-item>
          <el-descriptions-item :label="t('topology.endpointA')">
            {{ selectedLink.spec.endpointA.nodeName }}:{{ selectedLink.spec.endpointA.interface }}
            <template v-if="selectedLink.spec.endpointA.address">({{ selectedLink.spec.endpointA.address }})</template>
          </el-descriptions-item>
          <el-descriptions-item :label="t('topology.endpointB')">
            {{ selectedLink.spec.endpointB.nodeName }}:{{ selectedLink.spec.endpointB.interface }}
            <template v-if="selectedLink.spec.endpointB.address">({{ selectedLink.spec.endpointB.address }})</template>
          </el-descriptions-item>
          <el-descriptions-item :label="t('topology.mtu')">{{ selectedLink.spec.mtu }}</el-descriptions-item>
        </el-descriptions>
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
.node-power { margin-bottom: 12px; }
.header h2 { margin: 0; }
.hint { font-size: 12px; font-weight: normal; color: var(--el-text-color-secondary); margin-left: 8px; }
/* The non-modal drawers still mount a full-viewport positioning
   wrapper that swallows clicks (breaking canvas double-click); let
   events pass through it and keep only the panel interactive. */
.page :deep(.el-modal-drawer),
.page :deep(.el-overlay) { pointer-events: none; }
.page :deep(.el-drawer) { pointer-events: auto; }
.body { flex: 1; min-height: 0; }
h4 { margin: 16px 0 8px; }
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
</style>
