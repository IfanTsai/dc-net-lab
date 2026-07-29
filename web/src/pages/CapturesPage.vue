<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { useLabStore } from '../stores/lab'
import { labApi } from '../api/lab'
import { nodeCaptureInterfaces } from '../utils/capture'
import type { CaptureFilter, CaptureSession, Node } from '../types/models'

const store = useLabStore()
const router = useRouter()
const { t } = useI18n()

const sessions = ref<CaptureSession[]>([])
const loading = ref(false)
const busy = ref<Record<string, boolean>>({})

const deployed = computed(() => (store.currentLab?.meta.generation ?? '0') !== '0')

async function refresh() {
  if (!store.currentLabId) {
    sessions.value = []
    return
  }

  try {
    sessions.value = await labApi.captureSessions(store.currentLabId)
  } catch {
    /* lab gone or backend restarting; keep the last view */
  }
}

let pollTimer: number | undefined

onMounted(async () => {
  if (store.labs.length === 0) await store.refreshLabs()
  await store.refreshTopology()
  loading.value = true
  await refresh()
  loading.value = false
  pollTimer = window.setInterval(refresh, 3000)
})
onBeforeUnmount(() => window.clearInterval(pollTimer))

async function onLabChange(id: string) {
  await store.selectLab(id)
  loading.value = true
  await refresh()
  loading.value = false
}

// --- create dialog ---
const dialogVisible = ref(false)
const emptyForm = () => ({
  name: '',
  nodeId: '',
  iface: '',
  direction: 'both',
  durationSeconds: 30,
  snapLength: 256,
  protocol: '',
  srcPrefix: '',
  dstPrefix: '',
  port: undefined as number | undefined,
})
const form = ref(emptyForm())

function openCreate() {
  form.value = emptyForm()
  dialogVisible.value = true
}

function nodeLabel(n: Node): string {
  return `${n.meta.name} (${n.spec.role})`
}

// The interface dropdown mirrors the backend's simulation-view scope:
// topology link endpoints plus the modelled logical interfaces
// (vlanif on leaves, bond0 on servers); eth0/br0/macvlan are not
// capture targets.
const interfaceOptions = computed(() => {
  const node = store.nodes.find((n) => n.meta.id === form.value.nodeId)
  if (!node) return []
  return nodeCaptureInterfaces(node, store.links)
})

function onNodeChange() {
  form.value.iface = ''
}

const canSubmit = computed(() => !!(form.value.name && form.value.nodeId && form.value.iface))

async function submitCreate() {
  const filter: CaptureFilter | undefined =
    form.value.protocol || form.value.srcPrefix || form.value.dstPrefix || form.value.port
      ? {
          protocol: form.value.protocol || undefined,
          srcPrefix: form.value.srcPrefix || undefined,
          dstPrefix: form.value.dstPrefix || undefined,
          port: form.value.port || undefined,
        }
      : undefined

  try {
    const sess = await labApi.createCaptureSession(store.currentLabId, {
      name: form.value.name,
      nodeId: form.value.nodeId,
      interface: form.value.iface,
      direction: form.value.direction,
      durationSeconds: form.value.durationSeconds,
      snapLength: form.value.snapLength,
      filter,
    })
    dialogVisible.value = false
    ElMessage.success(t('captures.created', { name: sess.meta.name }))
    openViewer(sess)
  } catch (e) {
    ElMessage.error((e as Error).message)
  }
}

// --- row actions ---
function openViewer(s: CaptureSession) {
  void router.push(`/captures/${s.spec.labId}/${s.meta.id}`)
}

async function withBusy(id: string, fn: () => Promise<unknown>) {
  busy.value = { ...busy.value, [id]: true }
  try {
    await fn()
    await refresh()
  } catch (e) {
    ElMessage.error((e as Error).message)
  } finally {
    busy.value = { ...busy.value, [id]: false }
  }
}

const stop = (s: CaptureSession) =>
  withBusy(s.meta.id, () => labApi.stopCaptureSession(store.currentLabId, s.meta.id))

async function remove(s: CaptureSession) {
  try {
    await ElMessageBox.confirm(
      s.status.state === 'Running'
        ? t('captures.deleteConfirmRunning', { name: s.meta.name })
        : t('captures.deleteConfirm', { name: s.meta.name }),
      t('captures.deleteTitle'),
    )
  } catch {
    return
  }

  await withBusy(s.meta.id, () => labApi.deleteCaptureSession(store.currentLabId, s.meta.id))
}

function download(s: CaptureSession) {
  window.open(labApi.capturePcapUrl(store.currentLabId, s.meta.id), '_blank')
}

function stateTag(state: string): 'primary' | 'success' | 'info' | 'danger' {
  switch (state) {
    case 'Running': return 'primary'
    case 'Completed': return 'success'
    case 'Stopped': return 'info'
    default: return 'danger'
  }
}

function stateLabel(state: string): string {
  switch (state) {
    case 'Running': return t('captures.stateRunning')
    case 'Completed': return t('captures.stateCompleted')
    case 'Stopped': return t('captures.stateStopped')
    case 'Failed': return t('captures.stateFailed')
    default: return state
  }
}

function fmtBytes(v?: string): string {
  const n = Number(v ?? 0)
  if (n < 1024) return `${n} B`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KiB`
  return `${(n / 1024 / 1024).toFixed(1)} MiB`
}

function filterSummary(f?: CaptureFilter): string {
  if (!f) return ''
  const parts: string[] = []
  if (f.protocol) parts.push(f.protocol)
  if (f.srcPrefix) parts.push(`src ${f.srcPrefix}`)
  if (f.dstPrefix) parts.push(`dst ${f.dstPrefix}`)
  if (f.port) parts.push(`port ${f.port}`)
  return parts.join(' · ')
}
</script>

<template>
  <div>
    <div class="header">
      <h2>{{ t('captures.title') }}</h2>
      <div class="header-actions">
        <el-button type="primary" :disabled="!deployed" @click="openCreate">
          {{ t('captures.create') }}
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

    <el-alert v-if="!deployed" type="info" :closable="false" :title="t('captures.needDeploy')" class="hint" />

    <el-table :data="sessions" v-loading="loading" @row-dblclick="openViewer">
      <el-table-column prop="meta.name" :label="t('common.name')" width="150" />
      <el-table-column :label="t('captures.target')" width="220">
        <template #default="{ row }">
          <div>{{ row.spec.nodeName }} : {{ row.spec.interface }}</div>
          <div class="sub" v-if="filterSummary(row.spec.filter)">{{ filterSummary(row.spec.filter) }}</div>
          <div class="sub" v-else-if="row.spec.direction && row.spec.direction !== 'both'">{{ row.spec.direction }}</div>
        </template>
      </el-table-column>
      <el-table-column :label="t('captures.state')" width="130">
        <template #default="{ row }">
          <el-tag size="small" :type="stateTag(row.status.state)">{{ stateLabel(row.status.state) }}</el-tag>
          <div class="sub error" v-if="row.status.lastError">{{ row.status.lastError }}</div>
        </template>
      </el-table-column>
      <el-table-column :label="t('captures.packets')" width="100">
        <template #default="{ row }">{{ row.status.packets ?? 0 }}</template>
      </el-table-column>
      <el-table-column :label="t('captures.bytes')" width="110">
        <template #default="{ row }">{{ fmtBytes(row.status.bytes) }}</template>
      </el-table-column>
      <el-table-column :label="t('common.actions')" min-width="300">
        <template #default="{ row }">
          <el-button size="small" type="primary" @click="openViewer(row)">{{ t('captures.open') }}</el-button>
          <el-button
            v-if="row.status.state === 'Running'"
            size="small"
            type="warning"
            :loading="busy[row.meta.id]"
            @click="stop(row)"
          >
            {{ t('captures.stop') }}
          </el-button>
          <el-button size="small" @click="download(row)">{{ t('captures.download') }}</el-button>
          <el-button size="small" type="danger" plain :loading="busy[row.meta.id]" @click="remove(row)">
            {{ t('common.delete') }}
          </el-button>
        </template>
      </el-table-column>
    </el-table>

    <el-dialog v-model="dialogVisible" :title="t('captures.createTitle')" width="520px">
      <el-form label-width="130px">
        <el-form-item :label="t('common.name')">
          <el-input v-model="form.name" placeholder="bgp-on-leaf1" />
        </el-form-item>
        <el-form-item :label="t('captures.node')">
          <el-select v-model="form.nodeId" filterable style="width: 100%" :placeholder="t('captures.pickNode')" @change="onNodeChange">
            <el-option v-for="n in store.nodes" :key="n.meta.id" :label="nodeLabel(n)" :value="n.meta.id" />
          </el-select>
        </el-form-item>
        <el-form-item :label="t('captures.interface')">
          <el-select v-model="form.iface" style="width: 100%" :placeholder="t('captures.pickInterface')" :disabled="!form.nodeId">
            <el-option v-for="i in interfaceOptions" :key="i.value" :label="i.label" :value="i.value" />
          </el-select>
        </el-form-item>
        <el-form-item :label="t('captures.direction')">
          <el-radio-group v-model="form.direction">
            <el-radio-button value="both">{{ t('captures.dirBoth') }}</el-radio-button>
            <el-radio-button value="rx">{{ t('captures.dirRx') }}</el-radio-button>
            <el-radio-button value="tx">{{ t('captures.dirTx') }}</el-radio-button>
          </el-radio-group>
        </el-form-item>
        <el-form-item :label="t('captures.durationSeconds')">
          <el-input-number v-model="form.durationSeconds" :min="1" :max="600" controls-position="right" />
        </el-form-item>
        <el-form-item :label="t('captures.snapLength')">
          <el-input-number v-model="form.snapLength" :min="64" :max="65535" :step="64" controls-position="right" />
        </el-form-item>
        <el-divider>{{ t('captures.filter') }}</el-divider>
        <el-form-item :label="t('captures.protocol')">
          <el-select v-model="form.protocol" clearable style="width: 100%" :placeholder="t('captures.anyProtocol')">
            <el-option v-for="p in ['arp', 'icmp', 'tcp', 'udp', 'bgp', 'vxlan']" :key="p" :label="p.toUpperCase()" :value="p" />
          </el-select>
        </el-form-item>
        <el-form-item :label="t('captures.srcPrefix')">
          <el-input v-model="form.srcPrefix" placeholder="10.100.0.0/24" />
        </el-form-item>
        <el-form-item :label="t('captures.dstPrefix')">
          <el-input v-model="form.dstPrefix" placeholder="10.100.1.11" />
        </el-form-item>
        <el-form-item :label="t('captures.port')">
          <el-input-number v-model="form.port" :min="1" :max="65535" controls-position="right" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="dialogVisible = false">{{ t('common.cancel') }}</el-button>
        <el-button type="primary" :disabled="!canSubmit" @click="submitCreate">
          {{ t('common.create') }}
        </el-button>
      </template>
    </el-dialog>
  </div>
</template>

<style scoped>
.header { display: flex; justify-content: space-between; align-items: center; margin-bottom: 12px; }
.header h2 { margin: 0; }
.header-actions { display: flex; gap: 12px; align-items: center; }
.hint { margin-bottom: 12px; }
.sub { font-size: 11px; color: var(--el-text-color-secondary); }
.sub.error { color: var(--el-color-danger); }
</style>
