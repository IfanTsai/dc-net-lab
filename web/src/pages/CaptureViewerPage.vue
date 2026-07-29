<script setup lang="ts">
import { computed, h, nextTick, onBeforeUnmount, onMounted, ref } from 'vue'
import { ElMessage } from 'element-plus'
import { QuestionFilled } from '@element-plus/icons-vue'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'
import type { Column, RowEventHandlerParams } from 'element-plus'
import { labApi } from '../api/lab'
import type { CapturePacketField, CapturePacketLayer, CapturePacketRow, CaptureSession, CaptureWsEvent } from '../types/models'

const route = useRoute()
const router = useRouter()
const { t, te, tm, rt } = useI18n()

const labId = route.params.labId as string
const sessionId = route.params.id as string

const session = ref<CaptureSession | null>(null)
const rows = ref<CapturePacketRow[]>([])
const totalPackets = ref(0)
const firstAvailable = ref(0)
const missedLive = ref(0)
const ended = ref(false)
const autoScroll = ref(true)
const stopBusy = ref(false)

const running = computed(() => !ended.value && session.value?.status.state === 'Running')

// --- filters ---
const filterDirection = ref('')
const filterProtocol = ref('')
const filterSource = ref('')
const filterDestination = ref('')
const filterSourcePort = ref('')
const filterDestinationPort = ref('')

function distinctSorted(values: Iterable<string>): string[] {
  return Array.from(new Set(values)).sort()
}

const protocolOptions = computed(() => distinctSorted(
  rows.value.map((r) => r.protocol).filter((v): v is string => !!v),
))
const sourceOptions = computed(() => distinctSorted(
  rows.value.map((r) => r.source).filter((v): v is string => !!v),
))
const destinationOptions = computed(() => distinctSorted(
  rows.value.map((r) => r.destination).filter((v): v is string => !!v),
))
const sourcePortOptions = computed(() => distinctSorted(
  rows.value.map((r) => r.sourcePort).filter((v): v is number => v != null).map(String),
).sort((a, b) => Number(a) - Number(b)))
const destinationPortOptions = computed(() => distinctSorted(
  rows.value.map((r) => r.destinationPort).filter((v): v is number => v != null).map(String),
).sort((a, b) => Number(a) - Number(b)))

// el-autocomplete's fetch-suggestions contract: call back with the
// matching {value} items for whatever the user has typed so far.
function suggest(options: string[]) {
  return (query: string, cb: (results: { value: string }[]) => void) => {
    const q = query.trim().toLowerCase()
    const matches = q ? options.filter((o) => o.toLowerCase().includes(q)) : options
    cb(matches.map((value) => ({ value })))
  }
}

const suggestSource = computed(() => suggest(sourceOptions.value))
const suggestDestination = computed(() => suggest(destinationOptions.value))
const suggestSourcePort = computed(() => suggest(sourcePortOptions.value))
const suggestDestinationPort = computed(() => suggest(destinationPortOptions.value))

const filteredRows = computed(() => {
  const src = filterSource.value.trim().toLowerCase()
  const dst = filterDestination.value.trim().toLowerCase()
  const srcPort = filterSourcePort.value.trim()
  const dstPort = filterDestinationPort.value.trim()
  return rows.value.filter((r) => {
    if (filterDirection.value && r.direction !== filterDirection.value) return false
    if (filterProtocol.value && r.protocol !== filterProtocol.value) return false
    if (src && !(r.source ?? '').toLowerCase().includes(src)) return false
    if (dst && !(r.destination ?? '').toLowerCase().includes(dst)) return false
    if (srcPort && !String(r.sourcePort ?? '').includes(srcPort)) return false
    if (dstPort && !String(r.destinationPort ?? '').includes(dstPort)) return false
    return true
  })
})

const hasFilter = computed(() => !!(
  filterDirection.value || filterProtocol.value || filterSource.value ||
  filterDestination.value || filterSourcePort.value || filterDestinationPort.value
))

function clearFilters() {
  filterDirection.value = ''
  filterProtocol.value = ''
  filterSource.value = ''
  filterDestination.value = ''
  filterSourcePort.value = ''
  filterDestinationPort.value = ''
}

// --- session status ---
async function refreshSession() {
  try {
    session.value = await labApi.getCaptureSession(labId, sessionId)
    if (session.value.status.state !== 'Running') ended.value = true
  } catch (e) {
    ElMessage.error((e as Error).message)
  }
}

let pollTimer: number | undefined

// --- live rows over WebSocket, REST as fallback ---
let ws: WebSocket | undefined

type WsRow = NonNullable<CaptureWsEvent['packets']>[number]

function toRow(p: WsRow): CapturePacketRow {
  return {
    index: p.index ?? 0,
    ts: p.ts,
    direction: p.direction,
    captureLength: p.captureLength,
    wireLength: p.wireLength,
    source: p.source,
    destination: p.destination,
    sourcePort: p.sourcePort,
    destinationPort: p.destinationPort,
    protocol: p.protocol,
    info: p.info,
  }
}

function appendRows(batch: CapturePacketRow[]) {
  if (batch.length === 0) return
  const last = rows.value.length > 0 ? rows.value[rows.value.length - 1].index : -1
  // A jump in the capture index means live batches were dropped for
  // this (slow) subscriber; the pcap still has every packet.
  if (last >= 0 && batch[0].index > last + 1) {
    missedLive.value += batch[0].index - last - 1
  }

  rows.value = rows.value.concat(batch.filter((r) => r.index > last))
  if (autoScroll.value) {
    void nextTick(() => tableRef.value?.scrollToRow?.(filteredRows.value.length - 1, 'end'))
  }
}

function connectWs() {
  const proto = location.protocol === 'https:' ? 'wss' : 'ws'
  ws = new WebSocket(`${proto}://${location.host}/ws/v1/labs/${labId}/captures/${sessionId}`)

  ws.onmessage = (msg) => {
    const ev = JSON.parse(msg.data as string) as CaptureWsEvent
    if (ev.type === 'end') {
      ended.value = true
      void refreshSession()
      return
    }

    totalPackets.value = ev.total ?? totalPackets.value
    firstAvailable.value = ev.firstAvailable ?? firstAvailable.value
    appendRows((ev.packets ?? []).map(toRow))
  }

  ws.onclose = () => {
    // Normal for finished sessions (snapshot then close). If nothing
    // arrived at all, the live window is gone — page from the file.
    if (rows.value.length === 0 && totalPackets.value === 0) void loadFromFile()
    void refreshSession()
  }
  ws.onerror = () => ws?.close()
}

// loadFromFile pages rows over REST for sessions without live state
// (finished before a controller restart).
async function loadFromFile() {
  try {
    let offset = 0
    for (;;) {
      const page = await labApi.capturePackets(labId, sessionId, offset, 500)
      const batch = (page.packets ?? []).map((p) => toRow({
        index: Number(p.index ?? 0),
        ts: p.ts as string | undefined,
        direction: p.direction as string | undefined,
        captureLength: p.captureLength as number | undefined,
        wireLength: p.wireLength as number | undefined,
        source: p.source as string | undefined,
        destination: p.destination as string | undefined,
        sourcePort: p.sourcePort as number | undefined,
        destinationPort: p.destinationPort as number | undefined,
        protocol: p.protocol as string | undefined,
        info: p.info as string | undefined,
      }))
      appendRows(batch)
      totalPackets.value = Number(page.total ?? 0)
      firstAvailable.value = Number(page.firstAvailable ?? 0)
      offset += batch.length
      if (batch.length === 0 || offset >= totalPackets.value || offset >= 20000) break
    }
  } catch (e) {
    ElMessage.error((e as Error).message)
  }
}

onMounted(async () => {
  await refreshSession()
  connectWs()
  // Counters (packets/bytes) update through polling while running;
  // the WS only carries rows.
  pollTimer = window.setInterval(() => {
    if (running.value) void refreshSession()
  }, 3000)
})

onBeforeUnmount(() => {
  window.clearInterval(pollTimer)
  ws?.close()
})

// --- packet detail ---
const selectedIndex = ref<number | null>(null)
const detailLayers = ref<CapturePacketLayer[]>([])
const detailBytes = ref<Uint8Array | null>(null)
const detailTruncated = ref<{ wire: number; cap: number } | null>(null)

// Collapse state must be a real v-model ref: a :model-value computed
// from detailLayers would be recreated on every re-render (hover
// updates included) and re-expand whatever the user collapsed. Names
// are index-prefixed because one packet can hold several BGP message
// layers with the same name.
const expandedLayers = ref<string[]>([])

function layerKey(index: number, layer: CapturePacketLayer): string {
  return `${index}:${layer.name}`
}

async function openPacket(row: CapturePacketRow) {
  selectedIndex.value = row.index
  detailTruncated.value =
    row.wireLength && row.captureLength && row.wireLength > row.captureLength
      ? { wire: row.wireLength, cap: row.captureLength }
      : null

  try {
    const detail = await labApi.capturePacket(labId, sessionId, row.index)
    detailLayers.value = detail.layers ?? []
    expandedLayers.value = detailLayers.value.map((l, i) => layerKey(i, l))
    detailBytes.value = detail.data ? Uint8Array.from(atob(detail.data), (c) => c.charCodeAt(0)) : null
  } catch (e) {
    detailLayers.value = []
    expandedLayers.value = []
    detailBytes.value = null
    ElMessage.error((e as Error).message)
  }
}

interface HexByte { offset: number; hex: string; ascii: string }

const hexLines = computed(() => {
  const bytes = detailBytes.value
  if (!bytes) return []
  const lines: { offsetLabel: string; bytes: HexByte[] }[] = []
  for (let i = 0; i < bytes.length; i += 16) {
    const end = Math.min(i + 16, bytes.length)
    const line: HexByte[] = []
    for (let o = i; o < end; o++) {
      const b = bytes[o]
      line.push({ offset: o, hex: b.toString(16).padStart(2, '0'), ascii: b >= 0x20 && b < 0x7f ? String.fromCharCode(b) : '·' })
    }
    lines.push({ offsetLabel: i.toString(16).padStart(4, '0'), bytes: line })
  }
  return lines
})

// --- tree ↔ hex hover sync, Wireshark-style ---
const hoverRange = ref<{ start: number; end: number } | null>(null)

// Nested fields are rendered as flat rows with an indent depth, so the
// tree stays a plain table (virtual scroll friendly, trivial hover).
interface FieldRow { field: CapturePacketField; depth: number }

function flattenFields(fields: CapturePacketField[] | undefined, depth = 0, out: FieldRow[] = []): FieldRow[] {
  for (const f of fields ?? []) {
    out.push({ field: f, depth })
    flattenFields(f.children, depth + 1, out)
  }
  return out
}

const fieldRanges = computed(() => {
  const out: { start: number; end: number }[] = []
  for (const l of detailLayers.value) {
    for (const { field: f } of flattenFields(l.fields)) {
      if ((f.length ?? 0) > 0) out.push({ start: f.offset ?? 0, end: (f.offset ?? 0) + (f.length ?? 0) })
    }
  }
  return out
})

function hoverField(f: CapturePacketField) {
  if ((f.length ?? 0) > 0) hoverRange.value = { start: f.offset ?? 0, end: (f.offset ?? 0) + (f.length ?? 0) }
}

// hoverByte picks the narrowest field range containing the byte, so a
// byte inside e.g. a BGP prefix highlights the prefix rather than the
// whole "NLRI" or message range enclosing it.
function hoverByte(offset: number) {
  let match: { start: number; end: number } | null = null
  for (const r of fieldRanges.value) {
    if (offset < r.start || offset >= r.end) continue
    if (!match || r.end - r.start < match.end - match.start) match = r
  }
  hoverRange.value = match ?? { start: offset, end: offset + 1 }
}

function clearHover() {
  hoverRange.value = null
}

function isByteHighlighted(offset: number): boolean {
  return !!hoverRange.value && offset >= hoverRange.value.start && offset < hoverRange.value.end
}

function isFieldHighlighted(f: CapturePacketField): boolean {
  return !!hoverRange.value && (f.length ?? 0) > 0 &&
    f.offset === hoverRange.value.start && (f.offset ?? 0) + (f.length ?? 0) === hoverRange.value.end
}

// --- field-name tooltips ---
// Only fields whose meaning is not obvious from the value get one
// (addresses, ports and checksums need none); keys resolve under
// captures.viewer.fieldTips. Backend field names are stable English.
const fieldTipKeys: Record<string, string> = {
  'Marker': 'marker',
  'Message Type': 'messageType',
  'Payload Length': 'payloadLength',
  'Hold Time': 'holdTime',
  'Router ID': 'routerId',
  'Optional Parameters': 'optionalParameters',
  'Multiprotocol': 'multiprotocol',
  'Route Refresh': 'routeRefresh',
  'Route Refresh (Cisco)': 'routeRefresh',
  'Enhanced Route Refresh': 'enhancedRouteRefresh',
  '4-octet AS': 'fourOctetAs',
  'Graceful Restart': 'gracefulRestart',
  'Long-Lived Graceful Restart': 'longLivedGracefulRestart',
  'ADD-PATH': 'addPath',
  'FQDN': 'fqdn',
  'Extended Next Hop': 'extendedNextHop',
  'Extended Message': 'extendedMessage',
  'Path Attributes': 'pathAttributes',
  'NLRI': 'nlri',
  'Withdrawn Routes': 'withdrawn',
  'Origin': 'origin',
  'AS Path': 'asPath',
  'AS4 Path': 'asPath',
  'Next Hop': 'nextHop',
  'MED': 'med',
  'Local Pref': 'localPref',
  'Communities': 'communities',
  'Extended Communities': 'extCommunities',
  'Large Communities': 'largeCommunities',
  'Atomic Aggregate': 'atomicAggregate',
  'Aggregator': 'aggregator',
  'AS4 Aggregator': 'aggregator',
  'Originator ID': 'originatorId',
  'Cluster List': 'clusterList',
  'MP Reach NLRI': 'mpReach',
  'MP Unreach NLRI': 'mpUnreach',
  'Family': 'family',
  'TTL': 'ttl',
  'Hop Limit': 'hopLimit',
  'VNI': 'vni',
  'Window': 'window',
  'Sequence': 'sequence',
  'Acknowledgment': 'acknowledgment',
}

// Fields whose name repeats across layers (IPv4 and TCP both have
// "Flags") resolve through the layer-qualified map first.
const layerFieldTipKeys: Record<string, string> = {
  'IPv4:Identification': 'identification',
  'IPv4:Flags': 'ipFlags',
  'TCP:Flags': 'tcpFlags',
}

// A tip is either a plain sentence or a sentence plus a small
// term/description table (e.g. the meaning of each TCP flag).
interface FieldTip { text: string; items: { k: string; v: string }[] }

function fieldTip(layerName: string, fieldName: string): FieldTip | undefined {
  const key = layerFieldTipKeys[`${layerName}:${fieldName}`] ?? fieldTipKeys[fieldName]
  if (!key) return undefined

  const base = `captures.viewer.fieldTips.${key}`
  if (!te(`${base}.text`)) return { text: t(base), items: [] }

  const raw = tm(`${base}.items`) as { k: never; v: never }[]
  const items = (Array.isArray(raw) ? raw : []).map((it) => ({ k: rt(it.k), v: rt(it.v) }))
  return { text: t(`${base}.text`), items }
}

// layerRows resolves each row's tooltip once so the template doesn't
// re-evaluate it per binding.
interface TipFieldRow extends FieldRow { tip?: FieldTip }

function layerRows(layer: CapturePacketLayer): TipFieldRow[] {
  return flattenFields(layer.fields).map((r) => ({ ...r, tip: fieldTip(layer.name, r.field.name) }))
}

// --- virtual packet list ---
// eslint-disable-next-line @typescript-eslint/no-explicit-any
const tableRef = ref<any>(null)

function fmtTime(ts?: string): string {
  if (!ts) return ''
  const d = new Date(ts)
  return `${d.toLocaleTimeString('en-GB')}.${String(d.getMilliseconds()).padStart(3, '0')}`
}

// Widths are tuned to fit a ~1260px pane without horizontal scroll
// (which would clip the leading columns); Info soaks up the remainder.
const columns = computed<Column[]>(() => [
  { key: 'index', dataKey: 'index', title: t('captures.viewer.colNo'), width: 62, align: 'center' },
  {
    key: 'ts', title: t('captures.viewer.colTime'), width: 118, align: 'center',
    cellRenderer: ({ rowData }) => h('span', fmtTime((rowData as CapturePacketRow).ts)),
  },
  {
    key: 'direction', title: t('captures.viewer.colDirection'), width: 44, align: 'center',
    cellRenderer: ({ rowData }) => {
      const d = (rowData as CapturePacketRow).direction
      if (d !== 'rx' && d !== 'tx') return h('span', '')
      const label = d === 'rx' ? t('captures.viewer.dirRx') : t('captures.viewer.dirTx')

      return h('span', { title: label, class: `dir-${d}` }, d === 'rx' ? '↓' : '↑')
    },
  },
  { key: 'source', dataKey: 'source', title: t('captures.viewer.colSource'), width: 124, align: 'center' },
  {
    key: 'sourcePort', title: t('captures.viewer.colPort'), width: 62, align: 'center',
    cellRenderer: ({ rowData }) => h('span', (rowData as CapturePacketRow).sourcePort ?? ''),
  },
  { key: 'destination', dataKey: 'destination', title: t('captures.viewer.colDestination'), width: 124, align: 'center' },
  {
    key: 'destinationPort', title: t('captures.viewer.colPort'), width: 62, align: 'center',
    cellRenderer: ({ rowData }) => h('span', (rowData as CapturePacketRow).destinationPort ?? ''),
  },
  { key: 'protocol', dataKey: 'protocol', title: t('captures.viewer.colProtocol'), width: 74, align: 'center' },
  { key: 'wireLength', dataKey: 'wireLength', title: t('captures.viewer.colLength'), width: 62, align: 'center' },
  { key: 'info', dataKey: 'info', title: t('captures.viewer.colInfo'), width: 300, flexGrow: 1 },
])

const rowEventHandlers = {
  onClick: ({ rowData }: RowEventHandlerParams) => void openPacket(rowData as CapturePacketRow),
}

function rowClass({ rowData }: { rowData: CapturePacketRow }): string {
  const cls = `proto-${(rowData.protocol ?? '').toLowerCase()}`
  return rowData.index === selectedIndex.value ? `${cls} row-selected` : cls
}

// --- toolbar actions ---
async function stopCapture() {
  stopBusy.value = true
  try {
    session.value = await labApi.stopCaptureSession(labId, sessionId)
    ended.value = true
  } catch (e) {
    ElMessage.error((e as Error).message)
  } finally {
    stopBusy.value = false
  }
}

function download() {
  window.open(labApi.capturePcapUrl(labId, sessionId), '_blank')
}

function stateLabel(state?: string): string {
  switch (state) {
    case 'Running': return t('captures.stateRunning')
    case 'Completed': return t('captures.stateCompleted')
    case 'Stopped': return t('captures.stateStopped')
    case 'Failed': return t('captures.stateFailed')
    default: return state ?? ''
  }
}

function fmtBytes(v?: string): string {
  const n = Number(v ?? 0)
  if (n < 1024) return `${n} B`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KiB`
  return `${(n / 1024 / 1024).toFixed(1)} MiB`
}
</script>

<template>
  <div class="viewer">
    <div class="toolbar">
      <el-button class="back" text size="small" @click="router.push('/captures')">
        ← {{ t('captures.viewer.backToList') }}
      </el-button>
      <span class="divider" />
      <span class="title">{{ session?.meta.name }}</span>
      <span class="target" v-if="session">{{ session.spec.nodeName }} : {{ session.spec.interface }}</span>
      <el-tag size="small" :type="running ? 'primary' : 'info'" effect="light" round>
        {{ stateLabel(session?.status.state) }}
      </el-tag>
      <span class="counters" v-if="session">
        {{ t('captures.packets') }} {{ session.status.packets ?? 0 }} · {{ fmtBytes(session.status.bytes) }}
      </span>
      <span class="spacer" />
      <el-switch v-model="autoScroll" size="small" :active-text="t('captures.viewer.autoScroll')" />
      <el-button v-if="running" size="small" type="warning" :loading="stopBusy" @click="stopCapture">
        {{ t('captures.stop') }}
      </el-button>
      <el-button size="small" @click="download">{{ t('captures.download') }}</el-button>
    </div>

    <!-- Widths live on the wrappers: Element Plus sizes select/autocomplete
         roots at width:100%, so setting a width on the component itself is
         overridden and the controls end up sharing space evenly. -->
    <div class="filters">
      <span class="fw fw-sel">
        <el-select v-model="filterDirection" size="small" clearable :placeholder="t('captures.viewer.colDirection')">
          <el-option value="rx" :label="t('captures.viewer.dirRxShort')" />
          <el-option value="tx" :label="t('captures.viewer.dirTxShort')" />
        </el-select>
      </span>
      <span class="fw fw-sel">
        <el-select v-model="filterProtocol" size="small" clearable :placeholder="t('captures.viewer.colProtocol')">
          <el-option v-for="p in protocolOptions" :key="p" :value="p" :label="p" />
        </el-select>
      </span>

      <span class="fdiv" />
      <span class="flabel">{{ t('captures.viewer.colSource') }}</span>
      <span class="fw fw-ip">
        <el-autocomplete
          v-model="filterSource" size="small" clearable popper-class="capture-filter-popper" :fetch-suggestions="suggestSource"
          :placeholder="t('captures.viewer.filterIp')"
        />
      </span>
      <span class="fw fw-port">
        <el-autocomplete
          v-model="filterSourcePort" size="small" clearable popper-class="capture-filter-popper" :fetch-suggestions="suggestSourcePort"
          :placeholder="t('captures.viewer.colPort')"
        />
      </span>

      <span class="fdiv" />
      <span class="flabel">{{ t('captures.viewer.colDestination') }}</span>
      <span class="fw fw-ip">
        <el-autocomplete
          v-model="filterDestination" size="small" clearable popper-class="capture-filter-popper" :fetch-suggestions="suggestDestination"
          :placeholder="t('captures.viewer.filterIp')"
        />
      </span>
      <span class="fw fw-port">
        <el-autocomplete
          v-model="filterDestinationPort" size="small" clearable popper-class="capture-filter-popper" :fetch-suggestions="suggestDestinationPort"
          :placeholder="t('captures.viewer.colPort')"
        />
      </span>

      <span class="fspacer" />
      <template v-if="hasFilter">
        <span class="filter-count">{{ t('captures.viewer.filterCount', { shown: filteredRows.length, total: rows.length }) }}</span>
        <el-button text size="small" @click="clearFilters">{{ t('captures.viewer.filterClear') }}</el-button>
      </template>
    </div>

    <el-alert
      v-if="missedLive > 0"
      type="warning"
      :closable="false"
      class="notice"
      :title="t('captures.viewer.gapNotice', { count: missedLive })"
    />
    <el-alert
      v-else-if="firstAvailable > 0"
      type="info"
      :closable="false"
      class="notice"
      :title="t('captures.viewer.windowNotice', { window: rows.length })"
    />

    <div class="packet-list">
      <el-auto-resizer>
        <template #default="{ height, width }">
          <el-table-v2
            ref="tableRef"
            :columns="columns"
            :data="filteredRows"
            :width="width"
            :height="height"
            :row-height="26"
            :header-height="30"
            :row-class="rowClass"
            :row-event-handlers="rowEventHandlers"
          />
        </template>
      </el-auto-resizer>
    </div>

    <div class="detail">
      <div class="detail-pane tree">
        <div class="pane-title">{{ t('captures.viewer.detail') }}</div>
        <div class="pane-body">
          <el-empty v-if="selectedIndex === null" :description="t('captures.viewer.pickPacket')" :image-size="48" />
          <template v-else>
            <div class="truncated" v-if="detailTruncated">
              {{ t('captures.viewer.truncated', { wire: detailTruncated.wire, cap: detailTruncated.cap }) }}
            </div>
            <el-collapse v-model="expandedLayers">
              <el-collapse-item v-for="(layer, li) in detailLayers" :key="layerKey(li, layer)" :name="layerKey(li, layer)" :title="layer.name">
                <table class="fields">
                  <tbody>
                    <tr
                      v-for="(fr, i) in layerRows(layer)" :key="`${i}-${fr.field.name}`"
                      :class="{ 'field-hl': isFieldHighlighted(fr.field), 'field-hoverable': (fr.field.length ?? 0) > 0 }"
                      @mouseenter="hoverField(fr.field)" @mouseleave="clearHover"
                    >
                      <td class="field-name" :style="{ paddingLeft: `${8 + fr.depth * 14}px` }">
                        {{ fr.field.name }}
                        <el-tooltip
                          v-if="fr.tip"
                          placement="top"
                          effect="light"
                          :show-after="300"
                          popper-class="field-tip-popper"
                        >
                          <template #content>
                            <div class="field-tip">
                              <div>{{ fr.tip.text }}</div>
                              <div v-if="fr.tip.items.length" class="field-tip-items">
                                <template v-for="it in fr.tip.items" :key="it.k">
                                  <span class="field-tip-term">{{ it.k }}</span><span>{{ it.v }}</span>
                                </template>
                              </div>
                            </div>
                          </template>
                          <el-icon class="field-help"><QuestionFilled /></el-icon>
                        </el-tooltip>
                      </td>
                      <td class="field-value">{{ fr.field.value }}</td>
                    </tr>
                  </tbody>
                </table>
              </el-collapse-item>
            </el-collapse>
          </template>
        </div>
      </div>
      <div class="detail-pane hex">
        <div class="pane-title">{{ t('captures.viewer.hex') }}</div>
        <div class="pane-body">
          <div class="hex-dump" v-if="hexLines.length > 0">
            <div v-for="line in hexLines" :key="line.offsetLabel" class="hex-line">
              <span class="hex-offset">{{ line.offsetLabel }}</span>
              <span class="hex-bytes">
                <span
                  v-for="(b, i) in line.bytes" :key="b.offset"
                  class="hex-byte" :class="{ 'hex-hl': isByteHighlighted(b.offset), 'hex-gap': i === 8 }"
                  @mouseenter="hoverByte(b.offset)" @mouseleave="clearHover"
                >{{ b.hex }}</span>
              </span>
              <span class="hex-ascii">
                <span
                  v-for="b in line.bytes" :key="b.offset"
                  class="hex-byte" :class="{ 'hex-hl': isByteHighlighted(b.offset) }"
                  @mouseenter="hoverByte(b.offset)" @mouseleave="clearHover"
                >{{ b.ascii }}</span>
              </span>
            </div>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
/* Element Plus ships no --el-font-family-mono, so a bare `monospace`
   fallback renders as the browser's default (serif-ish on Linux). The
   packet list, hex dump and every address are tabular data — they need
   a real monospace stack with tabular figures. */
.viewer {
  --mono: ui-monospace, "SF Mono", SFMono-Regular, Menlo, Consolas,
    "DejaVu Sans Mono", "Liberation Mono", monospace;
  --surface: var(--el-bg-color-overlay, #fff);

  display: flex;
  flex-direction: column;
  height: calc(100vh - 40px);
}

/* --- toolbar --- */
.toolbar { display: flex; align-items: center; gap: 10px; margin-bottom: 10px; }
.toolbar .back { padding: 0 4px; color: var(--el-text-color-secondary); }
.toolbar .back:hover { color: var(--el-color-primary); }
.toolbar .divider { width: 1px; height: 14px; background: var(--el-border-color); }
.toolbar .title { font-weight: 600; font-size: 15px; letter-spacing: 0.2px; }
.toolbar .target {
  font-family: var(--mono);
  font-size: 12px;
  color: var(--el-text-color-regular);
  background: var(--el-fill-color-light);
  border-radius: 4px;
  padding: 2px 7px;
}
.toolbar .counters { color: var(--el-text-color-secondary); font-size: 12px; font-variant-numeric: tabular-nums; }
.toolbar .spacer { flex: 1; }

/* --- filter bar: plain controls, grouped by spacing --- */
.filters {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 6px;
  margin-bottom: 10px;
  flex-shrink: 0;
}
.fw { display: inline-flex; }
.fw > * { width: 100%; }
.fw-sel { width: 104px; }
.fw-ip { width: 134px; }
.fw-port { width: 86px; }
.flabel {
  font-size: 12px;
  color: var(--el-text-color-secondary);
  white-space: nowrap;
  margin-right: 1px;
}
.fdiv {
  width: 1px;
  height: 16px;
  margin: 0 6px;
  background: var(--el-border-color-lighter);
}
.fspacer { flex: 1; }
/* el-select and el-input compute their own heights (28 vs 30 at size
   "small"); pin both so the row reads as one baseline. */
.filters :deep(.el-select__wrapper),
.filters :deep(.el-input__wrapper) {
  min-height: 28px;
  height: 28px;
  box-sizing: border-box;
}
.fw-ip :deep(.el-input__inner),
.fw-port :deep(.el-input__inner) { font-family: var(--mono); font-size: 12px; }
.filter-count {
  color: var(--el-text-color-secondary);
  font-size: 12px;
  font-variant-numeric: tabular-nums;
}
.notice { margin-bottom: 8px; flex-shrink: 0; }

/* --- packet list --- */
.packet-list {
  flex: 1;
  min-height: 160px;
  overflow: hidden;
  background: var(--surface);
  border: 1px solid var(--el-border-color-light);
  border-radius: 6px;
  font-family: var(--mono);
  font-size: 12px;
  font-variant-numeric: tabular-nums;
}
.packet-list :deep(.el-table-v2__header-row) {
  background: var(--el-fill-color-light);
  border-bottom: 1px solid var(--el-border-color-light);
}
.packet-list :deep(.el-table-v2__header-cell) {
  font-family: var(--el-font-family);
  font-size: 12px;
  font-weight: 600;
  color: var(--el-text-color-regular);
}
.packet-list :deep(.el-table-v2__row) {
  cursor: pointer;
  border-bottom: 1px solid var(--el-fill-color);
}
.packet-list :deep(.el-table-v2__row:hover) { background: var(--el-fill-color-light); }
.packet-list :deep(.row-selected),
.packet-list :deep(.row-selected:hover) {
  background: var(--el-color-primary-light-9);
  box-shadow: inset 3px 0 0 0 var(--el-color-primary);
}
.packet-list :deep(.el-table-v2__row-cell) { color: var(--el-text-color-regular); }
.packet-list :deep(.dir-rx) { color: var(--el-color-success); font-weight: 700; }
.packet-list :deep(.dir-tx) { color: var(--el-color-warning); font-weight: 700; }

/* --- detail panes --- */
.detail { display: flex; gap: 10px; height: 38%; min-height: 220px; margin-top: 10px; }
.detail-pane {
  flex: 1;
  display: flex;
  flex-direction: column;
  overflow: hidden;
  background: var(--surface);
  border: 1px solid var(--el-border-color-light);
  border-radius: 6px;
}
.pane-title {
  flex-shrink: 0;
  font-size: 11px;
  font-weight: 600;
  letter-spacing: 0.4px;
  text-transform: uppercase;
  color: var(--el-text-color-secondary);
  background: var(--el-fill-color-light);
  border-bottom: 1px solid var(--el-border-color-lighter);
  padding: 6px 10px;
}
.pane-body { flex: 1; overflow: auto; padding: 8px 10px; }
.truncated { font-size: 12px; color: var(--el-color-warning); margin-bottom: 6px; }
/* Element Plus collapse is sized for page-level sections; the tree
   needs many shallow layers visible at once. */
.pane-body :deep(.el-collapse) { border: none; }
.pane-body :deep(.el-collapse-item__header) {
  height: 26px;
  line-height: 26px;
  font-size: 12px;
  font-weight: 600;
  border-bottom: none;
}
.pane-body :deep(.el-collapse-item__wrap) { border-bottom: none; }
.pane-body :deep(.el-collapse-item__content) { padding-bottom: 6px; font-size: 12px; }
.fields { border-collapse: collapse; width: 100%; font-size: 12px; }
.fields td { padding: 3px 8px; vertical-align: top; }
.field-name { color: var(--el-text-color-secondary); white-space: nowrap; width: 140px; }
/* A question-mark icon marks names that carry an explanation tooltip,
   matching the topology legend's help affordance. */
.field-help {
  cursor: help;
  font-size: 12px;
  vertical-align: -2px;
  color: var(--el-text-color-placeholder);
}
.field-help:hover { color: var(--el-color-primary); }
.field-value { font-family: var(--mono); word-break: break-all; }
.field-hoverable { cursor: default; }
.field-hl { background: var(--el-color-primary-light-9); box-shadow: inset 3px 0 0 0 var(--el-color-primary); }

/* --- hex dump --- */
.hex-dump { font-family: var(--mono); font-size: 12px; line-height: 1.6; }
.hex-line { display: flex; gap: 14px; white-space: pre; }
.hex-offset { color: var(--el-text-color-placeholder); user-select: none; }
.hex-ascii { color: var(--el-text-color-secondary); }
.hex-byte { padding: 1px 1px; border-radius: 2px; }
.hex-bytes .hex-byte + .hex-byte { margin-left: 3px; }
.hex-bytes .hex-byte.hex-gap { margin-left: 12px; }
.hex-byte.hex-hl {
  background: var(--el-color-primary);
  color: #fff;
}
</style>

<style>
/* Suggestions are teleported out of the component, so scoped styles
   cannot reach them; they hold addresses and ports and should match
   the monospace inputs they drop from. */
.capture-filter-popper .el-autocomplete-suggestion__list li {
  font-family: ui-monospace, "SF Mono", SFMono-Regular, Menlo, Consolas,
    "DejaVu Sans Mono", "Liberation Mono", monospace;
  font-size: 12px;
  line-height: 28px;
}

/* Field tooltips are teleported too; the term/description pairs line
   up as a compact two-column glossary under the intro sentence. */
.field-tip-popper { max-width: 340px; }
.field-tip-popper .field-tip { font-size: 12px; line-height: 1.6; }
.field-tip-popper .field-tip-items {
  display: grid;
  grid-template-columns: auto 1fr;
  column-gap: 10px;
  row-gap: 2px;
  margin-top: 4px;
}
.field-tip-popper .field-tip-term {
  font-family: ui-monospace, "SF Mono", SFMono-Regular, Menlo, Consolas,
    "DejaVu Sans Mono", "Liberation Mono", monospace;
  font-weight: 600;
}
</style>
