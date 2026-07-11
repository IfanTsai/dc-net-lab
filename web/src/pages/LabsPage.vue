<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { useI18n } from 'vue-i18n'
import { labApi } from '../api/lab'
import { useLabStore } from '../stores/lab'
import type { Plan } from '../types/models'
import OperationProgress from '../components/OperationProgress.vue'

const store = useLabStore()
const { t } = useI18n()

const createVisible = ref(false)
const createForm = ref({ name: '', profile: 'micro' })
const plan = ref<Plan | null>(null)
const planVisible = ref(false)
const activeOpId = ref('')

onMounted(() => store.refreshLabs())

async function createLab() {
  try {
    const lab = await labApi.create(createForm.value.name, createForm.value.profile)
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
      <el-table-column :label="t('common.actions')" width="280">
        <template #default="{ row }">
          <el-button size="small" @click="previewPlan(row.meta.id)">{{ t('labs.plan') }}</el-button>
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
      </el-form>
      <template #footer>
        <el-button @click="createVisible = false">{{ t('common.cancel') }}</el-button>
        <el-button type="primary" @click="createLab">{{ t('common.create') }}</el-button>
      </template>
    </el-dialog>

    <el-dialog v-model="planVisible" :title="t('labs.planTitle')" width="720px">
      <template v-if="plan">
        <p>
          {{ t('labs.planSummary', {
            base: plan.baseGeneration,
            next: plan.newGeneration,
            ops: plan.operations.length,
            allocs: plan.allocations.length,
          }) }}
        </p>
        <el-table :data="plan.operations" max-height="360" size="small">
          <el-table-column prop="type" :label="t('labs.operation')" width="170" />
          <el-table-column prop="summary" :label="t('labs.summary')" />
        </el-table>
      </template>
      <template #footer>
        <el-button @click="planVisible = false">{{ t('common.cancel') }}</el-button>
        <el-button type="primary" @click="applyPlan">{{ t('common.apply') }}</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<style scoped>
.header { display: flex; justify-content: space-between; align-items: center; }
.error-text { color: var(--el-color-danger); font-size: 12px; }
</style>
