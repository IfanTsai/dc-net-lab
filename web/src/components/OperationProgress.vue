<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from 'vue'
import { labApi } from '../api/lab'
import type { Operation } from '../types/models'

const props = defineProps<{ operationId: string }>()
const emit = defineEmits<{ done: [op: Operation] }>()

const op = ref<Operation | null>(null)
// What the bar shows. The backend reports real progress (weighted
// steps plus in-step reports); during an opaque long step the bar
// still creeps towards — never past — the running step's boundary,
// so it moves instead of stalling and jumping.
const displayed = ref(0)
let timer: ReturnType<typeof setInterval> | undefined
let animTimer: ReturnType<typeof setInterval> | undefined

async function poll() {
  try {
    const cur = await labApi.getOperation(props.operationId)
    op.value = cur
    if (cur.state === 'Succeeded' || cur.state === 'Failed' || cur.state === 'Cancelled') {
      displayed.value = cur.progress
      stop()
      emit('done', cur)
    }
  } catch {
    // operation may already be gone after lab deletion
    stop()
  }
}

// ceiling is the progress the operation will reach when the running
// step completes, derived from the step weights.
function ceiling(cur: Operation): number {
  let total = 0
  let done = 0
  let running = 0
  for (const s of cur.steps) {
    const w = s.weight || 1
    total += w
    if (s.state === 'Succeeded') done += w
    if (s.state === 'Running') running += w
  }
  if (total === 0) return cur.progress
  return ((done + running) * 100) / total
}

function animate() {
  const cur = op.value
  if (!cur) return
  if (displayed.value < cur.progress) displayed.value = cur.progress
  if (cur.state !== 'Running') return
  const cap = Math.max(cur.progress, ceiling(cur) - 1)
  if (displayed.value < cap) {
    // Asymptotic creep: fast at first, slowing as it nears the cap.
    displayed.value = Math.min(cap, displayed.value + Math.max(0.05, (cap - displayed.value) * 0.02))
  }
}

function stop() {
  if (timer) clearInterval(timer)
  if (animTimer) clearInterval(animTimer)
  timer = undefined
  animTimer = undefined
}

onMounted(() => {
  poll()
  timer = setInterval(poll, 500)
  animTimer = setInterval(animate, 200)
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
    <el-progress :percentage="Math.round(displayed)" :status="op.state === 'Failed' ? 'exception' : undefined" />
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
