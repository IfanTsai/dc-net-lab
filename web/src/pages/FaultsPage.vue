<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { useI18n } from 'vue-i18n'
import { useLabStore } from '../stores/lab'
import { labApi } from '../api/lab'
import type { FaultImpairment, FaultScenario, Link, Node } from '../types/models'

const store = useLabStore()
const { t } = useI18n()

const scenarios = ref<FaultScenario[]>([])
const loading = ref(false)
const busy = ref<Record<string, boolean>>({})

const deployed = computed(() => (store.currentLab?.meta.generation ?? '0') !== '0')

async function refresh() {
  if (!store.currentLabId) {
    scenarios.value = []
    return
  }

  try {
    scenarios.value = await labApi.faultScenarios(store.currentLabId)
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
  targetKind: 'node' as 'node' | 'link',
  nodeId: '',
  linkId: '',
  type: 'node-stop',
  side: '' as '' | 'a' | 'b' | 'both',
  delayMs: undefined as number | undefined,
  jitterMs: undefined as number | undefined,
  lossPercent: undefined as number | undefined,
  rateKbit: undefined as number | undefined,
})
const form = ref(emptyForm())

function openCreate() {
  form.value = emptyForm()
  dialogVisible.value = true
}

// Switching target kind resets the type to a valid default for that
// kind (node faults and link faults are disjoint type sets).
function onTargetKindChange(kind: 'node' | 'link') {
  form.value.targetKind = kind
  form.value.type = kind === 'node' ? 'node-stop' : 'link-down'
  form.value.side = ''
}

const selectedLink = computed<Link | undefined>(() =>
  store.links.find((l) => l.meta.id === form.value.linkId),
)

// Side options carry the real endpoint node names so the user picks
// "which physical port" instead of memorising an abstract A/B.
const sideOptions = computed(() => {
  const link = selectedLink.value
  if (!link) return []
  return [
    { label: t('faults.sideOf', { name: link.spec.endpointA.nodeName }), value: 'a' },
    { label: t('faults.sideOf', { name: link.spec.endpointB.nodeName }), value: 'b' },
  ]
})

function nodeLabel(n: Node): string {
  return `${n.meta.name} (${n.spec.role})`
}

function linkLabel(l: Link): string {
  return `${l.spec.endpointA.nodeName}:${l.spec.endpointA.interface} ↔ ${l.spec.endpointB.nodeName}:${l.spec.endpointB.interface}`
}

const canSubmit = computed(() => {
  if (!form.value.name) return false
  if (form.value.targetKind === 'node') return !!form.value.nodeId
  if (!form.value.linkId) return false
  if (form.value.type === 'interface-down') return form.value.side === 'a' || form.value.side === 'b'
  if (form.value.type === 'impairment') {
    return !!(form.value.delayMs || form.value.lossPercent || form.value.rateKbit)
  }
  return true
})

async function submitCreate() {
  const impairment: FaultImpairment | undefined =
    form.value.type === 'impairment'
      ? {
          delayMs: form.value.delayMs,
          jitterMs: form.value.delayMs ? form.value.jitterMs : undefined,
          lossPercent: form.value.lossPercent,
          rateKbit: form.value.rateKbit,
        }
      : undefined

  try {
    const fs = await labApi.createFaultScenario(store.currentLabId, {
      name: form.value.name,
      target: {
        kind: form.value.targetKind,
        nodeId: form.value.targetKind === 'node' ? form.value.nodeId : undefined,
        linkId: form.value.targetKind === 'link' ? form.value.linkId : undefined,
        side: form.value.targetKind === 'link' ? (form.value.side || undefined) : undefined,
      },
      type: form.value.type,
      impairment,
    })
    dialogVisible.value = false
    ElMessage.success(t('faults.created', { name: fs.meta.name }))
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

const toggle = (s: FaultScenario) =>
  withBusy(s.meta.id, () =>
    s.status.applied
      ? labApi.recoverFaultScenario(store.currentLabId, s.meta.id)
      : labApi.applyFaultScenario(store.currentLabId, s.meta.id),
  )

async function remove(s: FaultScenario) {
  try {
    await ElMessageBox.confirm(
      s.status.applied ? t('faults.deleteConfirmApplied', { name: s.meta.name }) : t('faults.deleteConfirm', { name: s.meta.name }),
      t('faults.deleteTitle'),
    )
  } catch {
    return
  }

  await withBusy(s.meta.id, () => labApi.deleteFaultScenario(store.currentLabId, s.meta.id))
}

function typeLabel(type: string): string {
  switch (type) {
    case 'node-stop': return t('faults.typeNodeStop')
    case 'node-restart': return t('faults.typeNodeRestart')
    case 'link-down': return t('faults.typeLinkDown')
    case 'interface-down': return t('faults.typeInterfaceDown')
    case 'impairment': return t('faults.typeImpairment')
    default: return type
  }
}

function impairmentSummary(imp?: FaultImpairment): string {
  if (!imp) return ''
  const parts: string[] = []
  if (imp.delayMs) parts.push(`delay ${imp.delayMs}ms${imp.jitterMs ? `±${imp.jitterMs}ms` : ''}`)
  if (imp.lossPercent) parts.push(`loss ${imp.lossPercent}%`)
  if (imp.rateKbit) parts.push(`rate ${imp.rateKbit}kbit`)
  return parts.join(' · ')
}
</script>

<template>
  <div>
    <div class="header">
      <h2>{{ t('faults.title') }}</h2>
      <div class="header-actions">
        <el-button type="primary" :disabled="!deployed" @click="openCreate">
          {{ t('faults.create') }}
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

    <el-alert v-if="!deployed" type="info" :closable="false" :title="t('faults.needDeploy')" class="hint" />

    <el-table :data="scenarios" v-loading="loading">
      <el-table-column prop="meta.name" :label="t('common.name')" width="140" />
      <el-table-column :label="t('faults.target')" width="240">
        <template #default="{ row }">
          <el-tag size="small" effect="plain" :type="row.spec.target.kind === 'node' ? 'primary' : 'warning'">
            {{ row.spec.target.kind === 'node' ? t('faults.targetNode') : t('faults.targetLink') }}
          </el-tag>
          <div class="sub">{{ row.spec.target.kind === 'node' ? row.spec.target.nodeName : row.spec.target.linkName }}</div>
        </template>
      </el-table-column>
      <el-table-column :label="t('faults.type')" width="200">
        <template #default="{ row }">
          <div>{{ typeLabel(row.spec.type) }}</div>
          <div class="sub" v-if="row.spec.type === 'impairment'">{{ impairmentSummary(row.spec.impairment) }}</div>
          <div class="sub" v-else-if="row.spec.target.side">{{ t('faults.side') }}: {{ row.spec.target.side }}</div>
        </template>
      </el-table-column>
      <el-table-column :label="t('faults.state')" width="130">
        <template #default="{ row }">
          <el-tag size="small" :type="row.status.applied ? 'danger' : 'info'">
            {{ row.status.applied ? t('faults.applied') : t('faults.recovered') }}
          </el-tag>
          <div class="sub error" v-if="row.status.lastError">{{ row.status.lastError }}</div>
        </template>
      </el-table-column>
      <el-table-column :label="t('common.actions')" width="220">
        <template #default="{ row }">
          <el-button
            v-if="row.spec.type !== 'node-restart' || !row.status.applied"
            size="small"
            :type="row.status.applied ? 'success' : 'danger'"
            :loading="busy[row.meta.id]"
            :disabled="!deployed"
            @click="toggle(row)"
          >
            {{ row.status.applied ? t('faults.recover') : t('faults.apply') }}
          </el-button>
          <el-button size="small" type="danger" plain :loading="busy[row.meta.id]" @click="remove(row)">
            {{ t('common.delete') }}
          </el-button>
        </template>
      </el-table-column>
    </el-table>

    <el-dialog v-model="dialogVisible" :title="t('faults.createTitle')" width="480px">
      <el-form label-width="120px">
        <el-form-item :label="t('common.name')">
          <el-input v-model="form.name" placeholder="cut-leaf1-server1" />
        </el-form-item>
        <el-form-item :label="t('faults.targetKind')">
          <el-radio-group :model-value="form.targetKind" @change="onTargetKindChange($event)">
            <el-radio-button value="node">{{ t('faults.targetNode') }}</el-radio-button>
            <el-radio-button value="link">{{ t('faults.targetLink') }}</el-radio-button>
          </el-radio-group>
        </el-form-item>

        <template v-if="form.targetKind === 'node'">
          <el-form-item :label="t('faults.targetNode')">
            <el-select v-model="form.nodeId" filterable style="width: 100%" :placeholder="t('faults.pickNode')">
              <el-option v-for="n in store.nodes" :key="n.meta.id" :label="nodeLabel(n)" :value="n.meta.id" />
            </el-select>
          </el-form-item>
          <el-form-item :label="t('faults.type')">
            <el-radio-group v-model="form.type">
              <el-radio-button value="node-stop">{{ t('faults.typeNodeStop') }}</el-radio-button>
              <el-radio-button value="node-restart">{{ t('faults.typeNodeRestart') }}</el-radio-button>
            </el-radio-group>
          </el-form-item>
        </template>

        <template v-else>
          <el-form-item :label="t('faults.targetLink')">
            <el-select v-model="form.linkId" filterable style="width: 100%" :placeholder="t('faults.pickLink')">
              <el-option v-for="l in store.links" :key="l.meta.id" :label="linkLabel(l)" :value="l.meta.id" />
            </el-select>
          </el-form-item>
          <el-form-item :label="t('faults.type')">
            <el-radio-group v-model="form.type">
              <el-radio-button value="link-down">{{ t('faults.typeLinkDown') }}</el-radio-button>
              <el-radio-button value="interface-down">{{ t('faults.typeInterfaceDown') }}</el-radio-button>
              <el-radio-button value="impairment">{{ t('faults.typeImpairment') }}</el-radio-button>
            </el-radio-group>
          </el-form-item>
          <el-form-item v-if="form.type === 'interface-down'" :label="t('faults.side')">
            <el-radio-group v-model="form.side">
              <el-radio-button v-for="o in sideOptions" :key="o.value" :value="o.value">{{ o.label }}</el-radio-button>
            </el-radio-group>
          </el-form-item>
          <template v-if="form.type === 'impairment'">
            <el-form-item :label="t('faults.side')">
              <el-radio-group v-model="form.side">
                <el-radio-button v-for="o in sideOptions" :key="o.value" :value="o.value">{{ o.label }}</el-radio-button>
                <el-radio-button value="both">{{ t('faults.sideBoth') }}</el-radio-button>
              </el-radio-group>
            </el-form-item>
            <el-form-item :label="t('faults.delayMs')">
              <el-input-number v-model="form.delayMs" :min="0" :max="60000" controls-position="right" />
            </el-form-item>
            <el-form-item v-if="form.delayMs" :label="t('faults.jitterMs')">
              <el-input-number v-model="form.jitterMs" :min="0" :max="form.delayMs" controls-position="right" />
            </el-form-item>
            <el-form-item :label="t('faults.lossPercent')">
              <el-input-number v-model="form.lossPercent" :min="0" :max="100" :step="0.5" controls-position="right" />
            </el-form-item>
            <el-form-item :label="t('faults.rateKbit')">
              <el-input-number v-model="form.rateKbit" :min="0" :max="10000000" controls-position="right" />
            </el-form-item>
            <div class="form-hint impairment-hint">{{ t('faults.impairmentHint') }}</div>
          </template>
        </template>
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
.form-hint { margin-left: 10px; font-size: 12px; color: var(--el-text-color-secondary); }
.impairment-hint { margin-left: 120px; margin-top: -8px; margin-bottom: 8px; }
</style>
