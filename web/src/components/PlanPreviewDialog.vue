<script setup lang="ts">
// The change-plan preview shared by the Labs page and the topology
// page's WYSIWYG scaling: grouped diff operations with coloured tags,
// +/~/- counts and warnings, and the apply action.
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { Plan } from '../types/models'

const { t } = useI18n()

const props = defineProps<{
  plan: Plan | null
  visible: boolean
}>()
const emit = defineEmits<{
  'update:visible': [value: boolean]
  apply: []
}>()

const opTagType: Record<string, string> = {
  CreateNode: 'success',
  CreateLink: 'success',
  UpdateNode: 'warning',
  DeleteNode: 'danger',
  DeleteLink: 'danger',
}

// The change counts headline: creations, updates and deletions,
// excluding the fixed render/deploy trailer steps.
const counts = computed(() => {
  const c = { create: 0, update: 0, remove: 0 }
  for (const op of props.plan?.operations ?? []) {
    if (op.type.startsWith('Create')) c.create++
    else if (op.type.startsWith('Update')) c.update++
    else if (op.type.startsWith('Delete')) c.remove++
  }
  return c
})
</script>

<template>
  <el-dialog
    :model-value="visible"
    :title="t('labs.planTitle')"
    width="720px"
    @update:model-value="emit('update:visible', $event)"
  >
    <template v-if="plan">
      <p>
        {{ t('labs.planSummary', {
          base: plan.baseGeneration,
          next: plan.newGeneration,
          ops: plan.operations.length,
          allocs: plan.allocations.length,
        }) }}
        <span class="diff-counts">
          <span class="diff-create">+{{ counts.create }}</span>
          <span class="diff-update">~{{ counts.update }}</span>
          <span class="diff-remove">-{{ counts.remove }}</span>
        </span>
      </p>
      <el-alert
        v-for="w in plan.warnings ?? []"
        :key="w.message"
        type="warning"
        :closable="false"
        class="plan-warning"
        :title="w.message"
      />
      <el-table :data="plan.operations" max-height="360" size="small">
        <el-table-column :label="t('labs.operation')" width="170">
          <template #default="{ row }">
            <el-tag size="small" :type="opTagType[row.type] ?? 'info'">{{ row.type }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="summary" :label="t('labs.summary')" />
      </el-table>
    </template>
    <template #footer>
      <el-button @click="emit('update:visible', false)">{{ t('common.cancel') }}</el-button>
      <el-button type="primary" @click="emit('apply')">{{ t('common.apply') }}</el-button>
    </template>
  </el-dialog>
</template>

<style scoped>
.diff-counts { margin-left: 8px; }
.diff-create { color: var(--el-color-success); margin-right: 6px; }
.diff-update { color: var(--el-color-warning); margin-right: 6px; }
.diff-remove { color: var(--el-color-danger); }
.plan-warning { margin-bottom: 8px; }
</style>
