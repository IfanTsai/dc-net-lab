<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { useI18n } from 'vue-i18n'
import { useLabStore } from '../stores/lab'
import { labApi } from '../api/lab'
import MetricsChart, { type ChartSeries } from '../components/MetricsChart.vue'
import type { TrafficAssertion, TrafficPoint, TrafficScenario } from '../types/models'

const store = useLabStore()
const { t } = useI18n()

const scenarios = ref<TrafficScenario[]>([])
const loading = ref(false)
const busy = ref<Record<string, boolean>>({})

const servers = computed(() => store.nodes.filter((n) => n.spec.role === 'server'))
const deployed = computed(() => (store.currentLab?.meta.generation ?? '0') !== '0')

async function refresh() {
  if (!store.currentLabId) {
    scenarios.value = []
    return
  }

  try {
    scenarios.value = await labApi.trafficScenarios(store.currentLabId)
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
  name: '', sourceServerId: '', destServerId: '', protocol: 'http', port: undefined as number | undefined,
  rate: 2, concurrency: 1, payloadBytes: undefined as number | undefined, durationMinutes: 0,
  assertions: [] as TrafficAssertion[],
})
const form = ref(emptyForm())

function openCreate() {
  form.value = emptyForm()
  dialogVisible.value = true
}

function addAssertion() {
  form.value.assertions.push({ metric: 'successRate', comparator: 'gte', threshold: 99 })
}

function removeAssertion(i: number) {
  form.value.assertions.splice(i, 1)
}

async function submitCreate() {
  try {
    const sc = await labApi.createTrafficScenario(store.currentLabId, {
      name: form.value.name,
      sourceServerId: form.value.sourceServerId,
      destServerId: form.value.destServerId,
      protocol: form.value.protocol,
      port: form.value.port,
      rate: form.value.rate,
      concurrency: form.value.concurrency,
      payloadBytes: form.value.payloadBytes,
      durationSeconds: form.value.durationMinutes ? form.value.durationMinutes * 60 : undefined,
      assertions: form.value.assertions.length ? form.value.assertions : undefined,
    })
    dialogVisible.value = false
    ElMessage.success(t('traffic.created', { name: sc.meta.name }))
    await refresh()
  } catch (e) {
    ElMessage.error((e as Error).message)
  }
}

// --- row actions ---
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

const power = (s: TrafficScenario) =>
  withBusy(s.meta.id, () =>
    s.status.phase === 'Running'
      ? labApi.stopTrafficScenario(store.currentLabId, s.meta.id)
      : labApi.startTrafficScenario(store.currentLabId, s.meta.id),
  )

async function remove(s: TrafficScenario) {
  try {
    await ElMessageBox.confirm(t('traffic.deleteConfirm', { name: s.meta.name }), t('traffic.deleteTitle'))
  } catch {
    return
  }

  await withBusy(s.meta.id, () => labApi.deleteTrafficScenario(store.currentLabId, s.meta.id))
  if (chartScenario.value?.meta.id === s.meta.id) chartScenario.value = null
}

function phaseTag(phase: string): string {
  switch (phase) {
    case 'Running':
      return 'success'
    case 'Failed':
      return 'danger'
    default:
      return 'info'
  }
}

const usMs = (us?: string) => (us ? Number(us) / 1000 : 0)

// --- live chart drawer ---
const chartScenario = ref<TrafficScenario | null>(null)
const historyPoints = ref<TrafficPoint[]>([])
const historyRange = ref(1800) // seconds; 30m default
let chartTimer: number | undefined

async function openChart(s: TrafficScenario) {
  chartScenario.value = s
  await refreshHistory()
  chartTimer = window.setInterval(refreshHistory, 5000)
}

function closeChart() {
  chartScenario.value = null
  window.clearInterval(chartTimer)
}

async function refreshHistory() {
  if (!chartScenario.value || !store.currentLabId) return

  try {
    const end = Math.floor(Date.now() / 1000)
    historyPoints.value = await labApi.trafficScenarioHistory(
      store.currentLabId, chartScenario.value.meta.id, end - historyRange.value, end,
    )
  } catch {
    /* keep the last series on a transient error */
  }
}

watch(historyRange, () => void refreshHistory())
onBeforeUnmount(() => window.clearInterval(chartTimer))

const toMs = (ts?: string) => Number(ts ?? 0) * 1000

const rateSeries = computed<ChartSeries[]>(() => [
  { name: t('traffic.rate'), points: historyPoints.value.map((p) => [toMs(p.ts), p.rate ?? 0]) },
])
const successSeries = computed<ChartSeries[]>(() => [
  { name: t('traffic.successRate'), points: historyPoints.value.map((p) => [toMs(p.ts), p.successRate ?? 0]) },
])
const latencySeries = computed<ChartSeries[]>(() => [
  { name: 'p50', points: historyPoints.value.map((p) => [toMs(p.ts), usMs(p.p50Us)]) },
  { name: 'p95', points: historyPoints.value.map((p) => [toMs(p.ts), usMs(p.p95Us)]) },
  { name: 'p99', points: historyPoints.value.map((p) => [toMs(p.ts), usMs(p.p99Us)]) },
])
</script>

<template>
  <div>
    <div class="header">
      <h2>{{ t('traffic.title') }}</h2>
      <div class="header-actions">
        <el-button type="primary" :disabled="!deployed || servers.length < 2" @click="openCreate">
          {{ t('traffic.create') }}
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

    <el-alert v-if="!deployed" type="info" :closable="false" :title="t('traffic.needDeploy')" class="hint" />

    <el-table :data="scenarios" v-loading="loading">
      <el-table-column prop="meta.name" :label="t('common.name')" width="130" />
      <el-table-column :label="t('traffic.path')" width="220">
        <template #default="{ row }">
          <code>{{ row.spec.sourceServerName }} → {{ row.spec.destServerName }}</code>
          <div class="sub">{{ row.spec.protocol }}{{ row.spec.port ? `:${row.spec.port}` : '' }}</div>
        </template>
      </el-table-column>
      <el-table-column :label="t('traffic.load')" width="150">
        <template #default="{ row }">
          {{ row.spec.rate }} req/s × {{ row.spec.concurrency }}
          <div class="sub" v-if="row.spec.payloadBytes">{{ row.spec.payloadBytes }} B</div>
        </template>
      </el-table-column>
      <el-table-column :label="t('traffic.state')" width="110">
        <template #default="{ row }">
          <el-tag size="small" :type="phaseTag(row.status.phase)">{{ row.status.phase }}</el-tag>
        </template>
      </el-table-column>
      <el-table-column :label="t('traffic.metrics')" width="260">
        <template #default="{ row }">
          <template v-if="row.status.phase === 'Running' && row.status.lastPoint">
            <span>{{ (row.status.lastPoint.rate ?? 0).toFixed(1) }} req/s</span>
            · <span>{{ (row.status.lastPoint.successRate ?? 0).toFixed(1) }}%</span>
            · <span>p99 {{ usMs(row.status.lastPoint.p99Us).toFixed(1) }} ms</span>
          </template>
          <span v-else class="sub">—</span>
        </template>
      </el-table-column>
      <el-table-column :label="t('traffic.assertions')" width="120">
        <template #default="{ row }">
          <template v-if="row.status.assertions?.length">
            <el-tag
              v-for="(a, i) in row.status.assertions"
              :key="i"
              size="small"
              class="assertion-tag"
              :type="a.pass ? 'success' : 'danger'"
            >
              {{ a.metric }}
            </el-tag>
          </template>
          <span v-else class="sub">—</span>
        </template>
      </el-table-column>
      <el-table-column :label="t('common.actions')" width="260">
        <template #default="{ row }">
          <el-button
            size="small"
            :type="row.status.phase === 'Running' ? 'danger' : 'success'"
            :loading="busy[row.meta.id]"
            :disabled="!deployed"
            @click="power(row)"
          >
            {{ row.status.phase === 'Running' ? t('traffic.stop') : t('traffic.start') }}
          </el-button>
          <el-button size="small" @click="openChart(row)">{{ t('traffic.chart') }}</el-button>
          <el-button size="small" type="danger" plain :loading="busy[row.meta.id]" @click="remove(row)">
            {{ t('common.delete') }}
          </el-button>
        </template>
      </el-table-column>
    </el-table>

    <el-dialog v-model="dialogVisible" :title="t('traffic.createTitle')" width="480px">
      <el-form label-width="120px">
        <el-form-item :label="t('common.name')">
          <el-input v-model="form.name" placeholder="http-test" />
        </el-form-item>
        <el-form-item :label="t('traffic.source')">
          <el-select v-model="form.sourceServerId" style="width: 100%" :placeholder="t('traffic.pickServer')">
            <el-option v-for="n in servers" :key="n.meta.id" :label="n.meta.name" :value="n.meta.id" />
          </el-select>
        </el-form-item>
        <el-form-item :label="t('traffic.dest')">
          <el-select v-model="form.destServerId" style="width: 100%" :placeholder="t('traffic.pickServer')">
            <el-option v-for="n in servers" :key="n.meta.id" :label="n.meta.name" :value="n.meta.id" />
          </el-select>
        </el-form-item>
        <el-form-item :label="t('traffic.protocol')">
          <el-radio-group v-model="form.protocol">
            <el-radio-button value="http">http</el-radio-button>
            <el-radio-button value="tcp">tcp</el-radio-button>
            <el-radio-button value="udp">udp</el-radio-button>
          </el-radio-group>
        </el-form-item>
        <el-form-item :label="t('traffic.port')">
          <el-input-number v-model="form.port" :min="1" :max="65535" controls-position="right" />
          <span class="form-hint">{{ t('traffic.portHint') }}</span>
        </el-form-item>
        <el-form-item :label="t('traffic.rate')">
          <el-input-number v-model="form.rate" :min="0.1" :max="1000" :step="1" controls-position="right" />
          <span class="form-hint">{{ t('traffic.rateHint') }}</span>
        </el-form-item>
        <el-form-item :label="t('traffic.concurrency')">
          <el-input-number v-model="form.concurrency" :min="1" :max="64" controls-position="right" />
        </el-form-item>
        <el-form-item :label="t('traffic.payloadBytes')">
          <el-input-number v-model="form.payloadBytes" :min="0" :max="1048576" controls-position="right" />
        </el-form-item>
        <el-form-item :label="t('traffic.duration')">
          <el-input-number v-model="form.durationMinutes" :min="0" :max="1440" controls-position="right" />
          <span class="form-hint">{{ t('traffic.durationHint') }}</span>
        </el-form-item>
        <el-form-item :label="t('traffic.assertions')">
          <div class="assertions-editor">
            <div v-for="(a, i) in form.assertions" :key="i" class="assertion-row">
              <el-select v-model="a.metric" style="width: 120px">
                <el-option label="rate" value="rate" />
                <el-option label="successRate" value="successRate" />
                <el-option label="p50" value="p50" />
                <el-option label="p95" value="p95" />
                <el-option label="p99" value="p99" />
              </el-select>
              <el-select v-model="a.comparator" style="width: 80px">
                <el-option label=">=" value="gte" />
                <el-option label="<=" value="lte" />
              </el-select>
              <el-input-number v-model="a.threshold" :controls="false" style="width: 100px" />
              <el-button size="small" text type="danger" @click="removeAssertion(i)">{{ t('common.delete') }}</el-button>
            </div>
            <el-button size="small" @click="addAssertion">{{ t('traffic.addAssertion') }}</el-button>
          </div>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="dialogVisible = false">{{ t('common.cancel') }}</el-button>
        <el-button
          type="primary"
          :disabled="!form.name || !form.sourceServerId || !form.destServerId || !form.rate"
          @click="submitCreate"
        >
          {{ t('common.create') }}
        </el-button>
      </template>
    </el-dialog>

    <el-drawer
      :model-value="!!chartScenario"
      :title="t('traffic.chartTitle', { name: chartScenario?.meta.name ?? '' })"
      size="55%"
      @close="closeChart"
    >
      <div class="chart-toolbar">
        <el-radio-group v-model="historyRange" size="small">
          <el-radio-button :value="1800">30m</el-radio-button>
          <el-radio-button :value="3600">1h</el-radio-button>
        </el-radio-group>
      </div>
      <template v-if="historyPoints.length">
        <MetricsChart :title="t('traffic.successRate')" :series="successSeries" unit="percent" />
        <MetricsChart :title="t('traffic.rate')" :series="rateSeries" unit="count" />
        <MetricsChart :title="t('traffic.latencyMs')" :series="latencySeries" unit="count" />
      </template>
      <el-alert v-else type="info" :closable="false" :title="t('traffic.chartEmpty')" />
    </el-drawer>
  </div>
</template>

<style scoped>
.header { display: flex; justify-content: space-between; align-items: center; margin-bottom: 12px; }
.header h2 { margin: 0; }
.header-actions { display: flex; gap: 12px; align-items: center; }
.hint { margin-bottom: 12px; }
.sub { font-size: 11px; color: var(--el-text-color-secondary); }
.form-hint { margin-left: 10px; font-size: 12px; color: var(--el-text-color-secondary); }
.assertion-tag { margin-right: 4px; margin-bottom: 2px; }
.assertions-editor { display: flex; flex-direction: column; gap: 6px; }
.assertion-row { display: flex; align-items: center; gap: 6px; }
.chart-toolbar { display: flex; justify-content: flex-end; margin-bottom: 8px; }
code { font-size: 12px; }
</style>
