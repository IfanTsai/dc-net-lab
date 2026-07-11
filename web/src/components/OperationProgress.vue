<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from 'vue'
import { labApi } from '../api/lab'
import type { Operation } from '../types/models'

const props = defineProps<{ operationId: string }>()
const emit = defineEmits<{ done: [op: Operation] }>()

const op = ref<Operation | null>(null)
let timer: ReturnType<typeof setInterval> | undefined

async function poll() {
  try {
    const cur = await labApi.getOperation(props.operationId)
    op.value = cur
    if (cur.state === 'Succeeded' || cur.state === 'Failed' || cur.state === 'Cancelled') {
      stop()
      emit('done', cur)
    }
  } catch {
    // operation may already be gone after lab deletion
    stop()
  }
}

function stop() {
  if (timer) clearInterval(timer)
  timer = undefined
}

onMounted(() => {
  poll()
  timer = setInterval(poll, 500)
})
onBeforeUnmount(stop)

const stepIcon: Record<string, string> = {
  Succeeded: '✓',
  Failed: '✗',
  Running: '…',
  Queued: '·',
}
</script>

<template>
  <el-card v-if="op" class="progress-card">
    <div class="row">
      <strong>{{ op.type }}</strong>
      <el-tag :type="op.state === 'Failed' ? 'danger' : op.state === 'Succeeded' ? 'success' : 'primary'">
        {{ op.state }}
      </el-tag>
    </div>
    <el-progress :percentage="op.progress" :status="op.state === 'Failed' ? 'exception' : undefined" />
    <ul class="steps">
      <li v-for="s in op.steps" :key="s.name">
        <span class="icon">{{ stepIcon[s.state] ?? '·' }}</span>
        {{ s.name }}
        <span v-if="s.message" class="msg">{{ s.message }}</span>
      </li>
    </ul>
  </el-card>
</template>

<style scoped>
.progress-card { margin-top: 16px; }
.row { display: flex; justify-content: space-between; margin-bottom: 8px; }
.steps { list-style: none; padding: 0; margin: 8px 0 0; font-size: 13px; }
.icon { display: inline-block; width: 16px; }
.msg { color: var(--el-color-danger); margin-left: 8px; }
</style>
