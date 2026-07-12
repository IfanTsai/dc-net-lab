<script setup lang="ts">
// ServerScopeSelect picks lab servers at dc/pod/rack/server
// granularity: a cascader tree pod → rack → server where checking a
// branch selects every server under it. The model is the flat list
// of selected server IDs.
import { computed } from 'vue'
import type { Node } from '../types/models'

const props = defineProps<{ nodes: Node[]; placeholder?: string }>()
const model = defineModel<string[]>({ default: () => [] })

interface CascaderNode {
  value: string
  label: string
  children?: CascaderNode[]
}

const options = computed<CascaderNode[]>(() => {
  const pods = new Map<string, Map<string, Node[]>>()
  for (const n of props.nodes) {
    if (n.spec.role !== 'server') continue

    const pod = n.spec.podId ?? '-'
    const rack = n.spec.rackId ?? '-'
    if (!pods.has(pod)) pods.set(pod, new Map())
    const racks = pods.get(pod)!
    if (!racks.has(rack)) racks.set(rack, [])
    racks.get(rack)!.push(n)
  }

  return [...pods.entries()].map(([pod, racks]) => ({
    value: pod,
    label: pod,
    children: [...racks.entries()].map(([rack, servers]) => ({
      value: `${pod}/${rack}`,
      label: rack,
      children: servers.map((s) => ({ value: s.meta.id, label: s.meta.name })),
    })),
  }))
})
</script>

<template>
  <el-cascader
    v-model="model"
    :options="options"
    :props="{ multiple: true, emitPath: false }"
    clearable
    collapse-tags
    collapse-tags-tooltip
    :placeholder="placeholder"
    style="width: 100%"
  />
</template>
