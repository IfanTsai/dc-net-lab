<script setup lang="ts">
import { onBeforeUnmount, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { useLabStore } from '../stores/lab'

const store = useLabStore()
const { t } = useI18n()
let timer: ReturnType<typeof setInterval> | undefined

onMounted(async () => {
  if (store.labs.length === 0) await store.refreshLabs()
  await store.refreshOperations()
  timer = setInterval(() => store.refreshOperations(), 2000)
})
onBeforeUnmount(() => timer && clearInterval(timer))

async function onLabChange(id: string) {
  await store.selectLab(id)
  await store.refreshOperations()
}

const stateType: Record<string, string> = {
  Succeeded: 'success',
  Failed: 'danger',
  Running: 'primary',
  Queued: 'info',
}
</script>

<template>
  <div>
    <div class="header">
      <h2>{{ t('operations.title') }}</h2>
      <el-select
        :model-value="store.currentLabId"
        style="width: 240px"
        :placeholder="t('topology.selectLab')"
        @change="onLabChange"
      >
        <el-option v-for="l in store.labs" :key="l.meta.id" :label="l.meta.name" :value="l.meta.id" />
      </el-select>
    </div>

    <el-table :data="store.operations">
      <el-table-column prop="type" :label="t('operations.type')" width="140" />
      <el-table-column :label="t('operations.state')" width="120">
        <template #default="{ row }">
          <el-tag :type="stateType[row.state] ?? 'info'">{{ row.state }}</el-tag>
        </template>
      </el-table-column>
      <el-table-column :label="t('operations.progress')" width="180">
        <template #default="{ row }">
          <el-progress :percentage="row.progress" :status="row.state === 'Failed' ? 'exception' : undefined" />
        </template>
      </el-table-column>
      <el-table-column :label="t('operations.steps')">
        <template #default="{ row }">
          <span v-for="s in row.steps" :key="s.name" class="step" :data-state="s.state">
            {{ s.name }}
          </span>
        </template>
      </el-table-column>
      <el-table-column prop="createdAt" :label="t('operations.created')" width="200" />
      <el-table-column :label="t('operations.error')">
        <template #default="{ row }">
          <span v-if="row.error" class="error-text">{{ row.error.message }}</span>
        </template>
      </el-table-column>
    </el-table>
  </div>
</template>

<style scoped>
.header { display: flex; justify-content: space-between; align-items: center; margin-bottom: 12px; }
.step {
  display: inline-block;
  margin-right: 6px;
  padding: 1px 6px;
  border-radius: 3px;
  font-size: 12px;
  background: var(--el-fill-color);
}
.step[data-state='Succeeded'] { background: var(--el-color-success-light-8); }
.step[data-state='Failed'] { background: var(--el-color-danger-light-8); }
.step[data-state='Running'] { background: var(--el-color-primary-light-8); }
.error-text { color: var(--el-color-danger); font-size: 12px; }
</style>
