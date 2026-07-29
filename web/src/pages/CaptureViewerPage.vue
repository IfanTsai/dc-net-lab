<script setup lang="ts">
import { computed, h, nextTick, onBeforeUnmount, onMounted, ref } from 'vue'
import { ElMessage } from 'element-plus'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'
import type { Column, RowEventHandlerParams } from 'element-plus'
import { labApi } from '../api/lab'
import type { CapturePacketLayer, CapturePacketRow, CaptureSession, CaptureWsEvent } from '../types/models'

const route = useRoute()
const router = useRouter()
const { t } = useI18n()

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
    void nextTick(() => tableRef.value?.scrollToRow?.(rows.value.length - 1, 'end'))
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

async function openPacket(row: CapturePacketRow) {
  selectedIndex.value = row.index
  detailTruncated.value =
    row.wireLength && row.captureLength && row.wireLength > row.captureLength
      ? { wire: row.wireLength, cap: row.captureLength }
      : null

  try {
    const detail = await labApi.capturePacket(labId, sessionId, row.index)
    detailLayers.value = detail.layers ?? []
    detailBytes.value = detail.data ? Uint8Array.from(atob(detail.data), (c) => c.charCodeAt(0)) : null
  } catch (e) {
    detailLayers.value = []
    detailBytes.value = null
    ElMessage.error((e as Error).message)
  }
}

const hexLines = computed(() => {
  const bytes = detailBytes.value
  if (!bytes) return []
  const lines: { offset: string; hex: string; ascii: string }[] = []
  for (let i = 0; i < bytes.length; i += 16) {
    const chunk = bytes.slice(i, i + 16)
    const hex = Array.from(chunk, (b) => b.toString(16).padStart(2, '0'))
    lines.push({
      offset: i.toString(16).padStart(4, '0'),
      hex: `${hex.slice(0, 8).join(' ')}  ${hex.slice(8).join(' ')}`,
      ascii: Array.from(chunk, (b) => (b >= 0x20 && b < 0x7f ? String.fromCharCode(b) : '·')).join(''),
    })
  }
  return lines
})

// --- virtual packet list ---
// eslint-disable-next-line @typescript-eslint/no-explicit-any
const tableRef = ref<any>(null)

function fmtTime(ts?: string): string {
  if (!ts) return ''
  const d = new Date(ts)
  return `${d.toLocaleTimeString('en-GB')}.${String(d.getMilliseconds()).padStart(3, '0')}`
}

const columns = computed<Column[]>(() => [
  { key: 'index', dataKey: 'index', title: t('captures.viewer.colNo'), width: 70 },
  {
    key: 'ts', title: t('captures.viewer.colTime'), width: 110,
    cellRenderer: ({ rowData }) => h('span', fmtTime((rowData as CapturePacketRow).ts)),
  },
  {
    key: 'direction', title: '', width: 34,
    cellRenderer: ({ rowData }) => {
      const d = (rowData as CapturePacketRow).direction
      return h('span', d === 'rx' ? '↓' : d === 'tx' ? '↑' : '')
    },
  },
  { key: 'source', dataKey: 'source', title: t('captures.viewer.colSource'), width: 130 },
  { key: 'destination', dataKey: 'destination', title: t('captures.viewer.colDestination'), width: 130 },
  { key: 'protocol', dataKey: 'protocol', title: t('captures.viewer.colProtocol'), width: 80 },
  { key: 'wireLength', dataKey: 'wireLength', title: t('captures.viewer.colLength'), width: 70 },
  { key: 'info', dataKey: 'info', title: t('captures.viewer.colInfo'), width: 600, flexGrow: 1 },
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
      <el-button text @click="router.push('/captures')">← {{ t('captures.viewer.backToList') }}</el-button>
      <span class="title">{{ session?.meta.name }}</span>
      <span class="target" v-if="session">{{ session.spec.nodeName }} : {{ session.spec.interface }}</span>
      <el-tag size="small" :type="running ? 'primary' : 'info'">{{ stateLabel(session?.status.state) }}</el-tag>
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
            :data="rows"
            :width="width"
            :height="height"
            :row-height="26"
            :header-height="30"
            :row-class="rowClass"
            :row-event-handlers="rowEventHandlers"
            fixed
          />
        </template>
      </el-auto-resizer>
    </div>

    <div class="detail">
      <div class="detail-pane tree">
        <div class="pane-title">{{ t('captures.viewer.detail') }}</div>
        <el-empty v-if="selectedIndex === null" :description="t('captures.viewer.pickPacket')" :image-size="48" />
        <template v-else>
          <div class="truncated" v-if="detailTruncated">
            {{ t('captures.viewer.truncated', { wire: detailTruncated.wire, cap: detailTruncated.cap }) }}
          </div>
          <el-collapse :model-value="detailLayers.map((l) => l.name)">
            <el-collapse-item v-for="layer in detailLayers" :key="layer.name" :name="layer.name" :title="layer.name">
              <table class="fields">
                <tbody>
                  <tr v-for="f in layer.fields ?? []" :key="f.name">
                    <td class="field-name">{{ f.name }}</td>
                    <td class="field-value">{{ f.value }}</td>
                  </tr>
                </tbody>
              </table>
            </el-collapse-item>
          </el-collapse>
        </template>
      </div>
      <div class="detail-pane hex">
        <div class="pane-title">{{ t('captures.viewer.hex') }}</div>
        <div class="hex-dump" v-if="hexLines.length > 0">
          <div v-for="line in hexLines" :key="line.offset" class="hex-line">
            <span class="hex-offset">{{ line.offset }}</span>
            <span class="hex-bytes">{{ line.hex }}</span>
            <span class="hex-ascii">{{ line.ascii }}</span>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.viewer { display: flex; flex-direction: column; height: calc(100vh - 40px); }
.toolbar { display: flex; align-items: center; gap: 10px; margin-bottom: 8px; }
.toolbar .title { font-weight: 700; }
.toolbar .target { color: var(--el-text-color-secondary); font-size: 13px; }
.toolbar .counters { color: var(--el-text-color-secondary); font-size: 12px; }
.toolbar .spacer { flex: 1; }
.notice { margin-bottom: 8px; flex-shrink: 0; }
.packet-list {
  flex: 1;
  min-height: 160px;
  border: 1px solid var(--el-border-color);
  border-radius: 4px;
  font-family: var(--el-font-family-mono, monospace);
  font-size: 12px;
}
.packet-list :deep(.row-selected) { background: var(--el-color-primary-light-8); }
.packet-list :deep(.el-table-v2__row) { cursor: pointer; }
.detail { display: flex; gap: 8px; height: 38%; min-height: 220px; margin-top: 8px; }
.detail-pane {
  flex: 1;
  overflow: auto;
  border: 1px solid var(--el-border-color);
  border-radius: 4px;
  padding: 8px;
}
.pane-title { font-size: 12px; font-weight: 700; color: var(--el-text-color-secondary); margin-bottom: 6px; }
.truncated { font-size: 12px; color: var(--el-color-warning); margin-bottom: 6px; }
.fields { border-collapse: collapse; width: 100%; font-size: 12px; }
.fields td { padding: 2px 8px; vertical-align: top; }
.field-name { color: var(--el-text-color-secondary); white-space: nowrap; width: 140px; }
.field-value { font-family: var(--el-font-family-mono, monospace); word-break: break-all; }
.hex-dump { font-family: var(--el-font-family-mono, monospace); font-size: 12px; line-height: 1.5; }
.hex-line { display: flex; gap: 16px; white-space: pre; }
.hex-offset { color: var(--el-text-color-secondary); }
.hex-ascii { color: var(--el-color-primary); }
</style>
