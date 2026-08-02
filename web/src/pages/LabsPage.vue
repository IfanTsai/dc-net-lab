<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { useI18n } from 'vue-i18n'
import { labApi } from '../api/lab'
import { useLabStore } from '../stores/lab'
import type { GenerationInfo, Plan, TopologySpec } from '../types/models'
import OperationProgress from '../components/OperationProgress.vue'
import PlanPreviewDialog from '../components/PlanPreviewDialog.vue'

const store = useLabStore()
const { t } = useI18n()

const createVisible = ref(false)
const createForm = ref({ name: '', profile: 'micro', internetAccess: false })
const plan = ref<Plan | null>(null)
const planVisible = ref(false)
const activeOpId = ref('')

const scaleVisible = ref(false)
const scaleLabId = ref('')
const scaleForm = ref<TopologySpec>({ externalRouters: 1, dcEdges: 1, superSpines: 1, pods: [] })

const gensVisible = ref(false)
const gensLabId = ref('')
const gens = ref<GenerationInfo[]>([])
const gensLoading = ref(false)

onMounted(() => store.refreshLabs())

async function createLab() {
  try {
    const lab = await labApi.create(
      createForm.value.name,
      createForm.value.profile,
      createForm.value.internetAccess,
    )
    createVisible.value = false
    createForm.value.name = ''
    await store.refreshLabs()
    await store.selectLab(lab.meta.id)
    ElMessage.success(t('labs.created', { name: lab.meta.name }))
  } catch (e: any) {
    ElMessage.error(e.message)
  }
}

async function previewPlan(labId: string) {
  try {
    plan.value = await labApi.createPlan(labId)
    planVisible.value = true
  } catch (e: any) {
    ElMessage.error(e.message)
  }
}

async function applyPlan() {
  if (!plan.value) return
  try {
    const { operationId } = await labApi.applyPlan(plan.value.id)
    planVisible.value = false
    activeOpId.value = operationId
  } catch (e: any) {
    ElMessage.error(e.message)
  }
}

async function removeLab(labId: string, name: string) {
  await ElMessageBox.confirm(t('labs.deleteConfirm', { name }), t('labs.deleteTitle'), {
    type: 'warning',
  })
  const { operationId } = await labApi.remove(labId)
  activeOpId.value = operationId
}

function openScale(labId: string) {
  const lab = store.labs.find((l) => l.meta.id === labId)
  if (!lab) return
  scaleLabId.value = labId
  const topo = lab.spec.topology
  scaleForm.value = {
    externalRouters: topo.externalRouters,
    dcEdges: topo.dcEdges,
    superSpines: topo.superSpines,
    pods: topo.pods.map((p) => ({ ...p })),
  }
  scaleVisible.value = true
}

function addPod() {
  scaleForm.value.pods.push({ name: '', spines: 2, racks: 1, serversPerRack: 2 })
}

function removePod(index: number) {
  scaleForm.value.pods.splice(index, 1)
}

async function submitScale() {
  try {
    await labApi.updateTopology(scaleLabId.value, scaleForm.value)
    scaleVisible.value = false
    await store.refreshLabs()
    // The scale itself is just a spec edit; the diff preview is where
    // the user sees (and confirms) what will actually change.
    await previewPlan(scaleLabId.value)
  } catch (e: any) {
    ElMessage.error(e.message)
  }
}

async function openGenerations(labId: string) {
  gensLabId.value = labId
  gensVisible.value = true
  gensLoading.value = true
  try {
    gens.value = await labApi.generations(labId)
  } catch (e: any) {
    ElMessage.error(e.message)
  } finally {
    gensLoading.value = false
  }
}

async function rollbackTo(gen: GenerationInfo) {
  await ElMessageBox.confirm(
    t('labs.rollbackConfirm', { gen: gen.generation }),
    t('labs.rollbackTitle'),
    { type: 'warning' },
  )
  try {
    plan.value = await labApi.rollback(gensLabId.value, gen.generation)
    gensVisible.value = false
    planVisible.value = true
  } catch (e: any) {
    ElMessage.error(e.message)
  }
}

function onOperationDone() {
  activeOpId.value = ''
  store.refreshLabs()
  store.refreshTopology()
}

const phaseType: Record<string, string> = {
  Running: 'success',
  Failed: 'danger',
  Degraded: 'warning',
  Applying: 'primary',
  Planning: 'info',
}

function formatTime(ts?: string) {
  return ts ? new Date(ts).toLocaleString() : ''
}
</script>

<template>
  <div>
    <div class="header">
      <h2>{{ t('labs.title') }}</h2>
      <el-button type="primary" @click="createVisible = true">{{ t('labs.createLab') }}</el-button>
    </div>

    <el-table :data="store.labs" v-loading="store.loading">
      <el-table-column prop="meta.name" :label="t('common.name')" />
      <el-table-column prop="spec.profile" :label="t('labs.profile')" width="110" />
      <el-table-column :label="t('labs.phase')" width="130">
        <template #default="{ row }">
          <el-tag :type="phaseType[row.meta.phase] ?? 'info'">{{ row.meta.phase }}</el-tag>
        </template>
      </el-table-column>
      <el-table-column prop="meta.generation" :label="t('labs.generation')" width="110" />
      <el-table-column :label="t('common.error')">
        <template #default="{ row }">
          <span v-if="row.meta.lastError" class="error-text">{{ row.meta.lastError.message }}</span>
        </template>
      </el-table-column>
      <el-table-column :label="t('common.actions')" width="420">
        <template #default="{ row }">
          <el-button size="small" @click="previewPlan(row.meta.id)">{{ t('labs.plan') }}</el-button>
          <el-button size="small" @click="openScale(row.meta.id)">{{ t('labs.scale') }}</el-button>
          <el-button size="small" @click="openGenerations(row.meta.id)">{{ t('labs.generations') }}</el-button>
          <el-button size="small" @click="store.selectLab(row.meta.id); $router.push('/topology')">
            {{ t('labs.topology') }}
          </el-button>
          <el-button size="small" type="danger" @click="removeLab(row.meta.id, row.meta.name)">
            {{ t('common.delete') }}
          </el-button>
        </template>
      </el-table-column>
    </el-table>

    <OperationProgress v-if="activeOpId" :operation-id="activeOpId" @done="onOperationDone" />

    <el-dialog v-model="createVisible" :title="t('labs.createTitle')" width="420px">
      <el-form :model="createForm" label-width="80px">
        <el-form-item :label="t('common.name')">
          <el-input v-model="createForm.name" :placeholder="t('labs.namePlaceholder')" />
        </el-form-item>
        <el-form-item :label="t('labs.profile')">
          <el-select v-model="createForm.profile">
            <el-option :label="t('labs.profileMicro')" value="micro" />
            <el-option :label="t('labs.profileStandard')" value="standard" />
          </el-select>
        </el-form-item>
        <el-form-item :label="t('labs.internetAccess')">
          <el-switch v-model="createForm.internetAccess" />
          <span class="hint">{{ t('labs.internetAccessHint') }}</span>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="createVisible = false">{{ t('common.cancel') }}</el-button>
        <el-button type="primary" @click="createLab">{{ t('common.create') }}</el-button>
      </template>
    </el-dialog>

    <el-dialog v-model="scaleVisible" :title="t('labs.scaleTitle')" width="640px">
      <p class="hint block-hint">{{ t('labs.scaleHint') }}</p>
      <el-form label-width="110px">
        <el-form-item :label="t('labs.coreTier')">
          <div class="core-row">
            <span class="core-label">{{ t('labs.externals') }}</span>
            <el-input-number v-model="scaleForm.externalRouters" :min="1" size="small" />
            <span class="core-label">{{ t('labs.dcEdges') }}</span>
            <el-input-number v-model="scaleForm.dcEdges" :min="1" size="small" />
            <span class="core-label">{{ t('labs.superSpines') }}</span>
            <el-input-number v-model="scaleForm.superSpines" :min="1" size="small" />
          </div>
        </el-form-item>
      </el-form>
      <el-table :data="scaleForm.pods" size="small">
        <el-table-column :label="t('labs.pod')" width="110">
          <template #default="{ row }">
            <span>{{ row.name || t('labs.newPod') }}</span>
          </template>
        </el-table-column>
        <el-table-column :label="t('labs.spines')" width="140">
          <template #default="{ row }">
            <el-input-number v-model="row.spines" :min="1" size="small" />
          </template>
        </el-table-column>
        <el-table-column :label="t('labs.racks')" width="140">
          <template #default="{ row }">
            <el-input-number v-model="row.racks" :min="1" size="small" />
          </template>
        </el-table-column>
        <el-table-column :label="t('labs.serversPerRack')" width="140">
          <template #default="{ row }">
            <el-input-number v-model="row.serversPerRack" :min="1" size="small" />
          </template>
        </el-table-column>
        <el-table-column width="80">
          <template #default="{ $index }">
            <el-button
              size="small"
              type="danger"
              text
              :disabled="scaleForm.pods.length <= 1"
              @click="removePod($index)"
            >
              {{ t('common.delete') }}
            </el-button>
          </template>
        </el-table-column>
      </el-table>
      <el-button class="add-pod" size="small" @click="addPod">{{ t('labs.addPod') }}</el-button>
      <template #footer>
        <el-button @click="scaleVisible = false">{{ t('common.cancel') }}</el-button>
        <el-button type="primary" @click="submitScale">{{ t('labs.previewChanges') }}</el-button>
      </template>
    </el-dialog>

    <el-dialog v-model="gensVisible" :title="t('labs.generationsTitle')" width="560px">
      <el-table :data="gens" v-loading="gensLoading" size="small">
        <el-table-column prop="generation" :label="t('labs.generation')" width="100">
          <template #default="{ row }">
            <span>{{ row.generation }}</span>
            <el-tag v-if="row.deployed" size="small" type="success" class="deployed-tag">
              {{ t('labs.deployed') }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column :label="t('labs.createdAt')">
          <template #default="{ row }">{{ formatTime(row.createdAt) }}</template>
        </el-table-column>
        <el-table-column prop="nodeCount" :label="t('labs.nodes')" width="80" />
        <el-table-column prop="linkCount" :label="t('labs.links')" width="80" />
        <el-table-column width="100">
          <template #default="{ row }">
            <el-button size="small" :disabled="row.deployed" @click="rollbackTo(row)">
              {{ t('labs.rollback') }}
            </el-button>
          </template>
        </el-table-column>
      </el-table>
    </el-dialog>

    <PlanPreviewDialog v-model:visible="planVisible" :plan="plan" @apply="applyPlan" />
  </div>
</template>

<style scoped>
.header { display: flex; justify-content: space-between; align-items: center; }
.error-text { color: var(--el-color-danger); font-size: 12px; }
.hint { margin-left: 8px; color: var(--el-text-color-secondary); font-size: 12px; }
.block-hint { margin: 0 0 12px; }
.core-row { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
.core-label { font-size: 12px; color: var(--el-text-color-secondary); }
.add-pod { margin-top: 8px; }
.deployed-tag { margin-left: 6px; }
</style>
