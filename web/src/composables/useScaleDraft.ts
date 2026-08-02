// useScaleDraft drives the topology page's WYSIWYG scaling: canvas
// actions mutate a client-side draft of the lab's TopologySpec, the
// draft renders as ghost (planned) and red-marked (to-be-removed)
// elements, and confirming submits the spec and opens the regular
// change-plan preview. Ghost names replicate the builder's numbering
// rules (append-only rack numbers, next-free pod names) as a visual
// hint only — the plan is authoritative.
import { computed, ref } from 'vue'
import type { GhostElements } from '../components/TopologyCanvas.vue'
import type { Lab, Node, PodSpec, TopologySpec } from '../types/models'

export interface DraftChange {
  key: string
  count: number
}

export function useScaleDraft(
  currentLab: () => Lab | undefined,
  nodes: () => Node[],
) {
  const draft = ref<TopologySpec | null>(null)
  // The spec as it was when the draft started, for discarding after
  // an already-submitted preview.
  const baseline = ref<TopologySpec | null>(null)
  const submitted = ref(false)
  // Draft snapshots taken before each edit; undo pops back through
  // them and closes the draft when it reaches the baseline again.
  const history = ref<TopologySpec[]>([])

  function cloneSpec(spec: TopologySpec): TopologySpec {
    return JSON.parse(JSON.stringify(spec))
  }

  function ensureDraft(): TopologySpec | null {
    if (!draft.value) {
      const lab = currentLab()
      if (!lab) return null
      baseline.value = cloneSpec(lab.spec.topology)
      draft.value = cloneSpec(lab.spec.topology)
    }
    return draft.value
  }

  function discard() {
    draft.value = null
    baseline.value = null
    submitted.value = false
    history.value = []
  }

  // snapshot records the draft before one edit mutates it.
  function snapshot() {
    if (draft.value) history.value.push(cloneSpec(draft.value))
  }

  function undo() {
    const prev = history.value.pop()
    if (!prev) return
    draft.value = prev
    if (JSON.stringify(draft.value) === JSON.stringify(baseline.value)) discard()
  }

  const canUndo = computed(() => history.value.length > 0)
  const active = computed(() => draft.value !== null)

  // --- Pod lookup helpers -------------------------------------------------

  function basePod(name: string): PodSpec | undefined {
    return baseline.value?.pods.find((p) => p.name === name)
  }

  function draftPod(name: string): PodSpec | undefined {
    return draft.value?.pods.find((p) => p.name === name)
  }

  // Reality as drawn on the canvas, grouped per pod.
  const podReality = computed(() => {
    const m = new Map<string, { spines: Node[]; racks: Map<string, { leaves: Node[]; servers: Node[] }> }>()
    for (const n of nodes()) {
      const pod = n.spec.podId
      if (!pod) continue
      if (!m.has(pod)) m.set(pod, { spines: [], racks: new Map() })
      const entry = m.get(pod)!
      if (n.spec.role === 'spine') entry.spines.push(n)
      if (!n.spec.rackId) continue
      if (!entry.racks.has(n.spec.rackId)) entry.racks.set(n.spec.rackId, { leaves: [], servers: [] })
      const rack = entry.racks.get(n.spec.rackId)!
      if (n.spec.role === 'leaf') rack.leaves.push(n)
      if (n.spec.role === 'server') rack.servers.push(n)
    }
    for (const entry of m.values()) {
      entry.spines.sort((a, b) => a.meta.name.localeCompare(b.meta.name, undefined, { numeric: true }))
      for (const rack of entry.racks.values()) {
        rack.leaves.sort((a, b) => a.meta.name.localeCompare(b.meta.name))
        rack.servers.sort((a, b) => a.meta.name.localeCompare(b.meta.name, undefined, { numeric: true }))
      }
    }
    return m
  })

  function rackNumber(rackId: string): number {
    return Number(rackId.match(/(\d+)$/)?.[1] ?? 0)
  }

  function sortedRackIds(pod: string): string[] {
    const entry = podReality.value.get(pod)
    return entry ? [...entry.racks.keys()].sort((a, b) => rackNumber(a) - rackNumber(b)) : []
  }

  // --- Actions ------------------------------------------------------------
  // Count-based spec: per pod, rack/server/spine edits are monotonic
  // within one draft (mixing "+1 rack" and "-1 rack" nets out to no
  // change, which is exactly what the spec would say).

  function addPod() {
    const d = ensureDraft()
    if (!d) return
    const used = new Set(d.pods.map((p) => p.name))
    let n = 1
    while (used.has(`pod-${n}`)) n++
    snapshot()
    d.pods.push({ name: `pod-${n}`, spines: 2, racks: 1, serversPerRack: 2 })
  }

  function removePod(pod: string) {
    const d = ensureDraft()
    if (!d || d.pods.length <= 1) return
    if (!d.pods.some((p) => p.name === pod)) return
    snapshot()
    d.pods = d.pods.filter((p) => p.name !== pod)
  }

  function bump(pod: string, field: 'racks' | 'serversPerRack' | 'spines', delta: 1 | -1) {
    const d = ensureDraft()
    if (!d) return
    const p = d.pods.find((x) => x.name === pod)
    if (!p || Math.max(1, p[field] + delta) === p[field]) return
    snapshot()
    p[field] = Math.max(1, p[field] + delta)
  }

  // canRemoveRack: with the slot model only the pod's tail rack is
  // removable, and only while the draft has no planned additions in
  // that pod (mixed add+remove nets out in a count-based spec).
  function canRemoveRack(pod: string, rackId: string): { ok: boolean; reason?: string } {
    const bp = basePod(pod)
    const dp = draftPod(pod) ?? bp
    if (!bp || !dp) return { ok: false }
    if (dp.racks > bp.racks) return { ok: false, reason: 'mixed' }
    if (dp.racks <= 1) return { ok: false, reason: 'last' }
    const racks = sortedRackIds(pod)
    const tail = racks[dp.racks - 1]
    if (rackId !== tail) return { ok: false, reason: 'tail' }
    return { ok: true }
  }

  // --- Change summary -----------------------------------------------------

  const changes = computed<DraftChange[]>(() => {
    const d = draft.value
    const b = baseline.value
    if (!d || !b) return []
    const out: DraftChange[] = []
    for (const p of d.pods) {
      const bp = b.pods.find((x) => x.name === p.name)
      if (!bp) {
        out.push({ key: 'addPod', count: 1 })
        continue
      }
      if (p.racks !== bp.racks) out.push({ key: p.racks > bp.racks ? 'addRacks' : 'removeRacks', count: Math.abs(p.racks - bp.racks) })
      if (p.serversPerRack !== bp.serversPerRack) out.push({ key: p.serversPerRack > bp.serversPerRack ? 'addServers' : 'removeServers', count: Math.abs(p.serversPerRack - bp.serversPerRack) })
      if (p.spines !== bp.spines) out.push({ key: p.spines > bp.spines ? 'addSpines' : 'removeSpines', count: Math.abs(p.spines - bp.spines) })
    }
    for (const p of b.pods) {
      if (!d.pods.some((x) => x.name === p.name)) out.push({ key: 'removePod', count: 1 })
    }
    return out
  })

  const dirty = computed(() => changes.value.length > 0)

  // --- Ghost & removal preview -------------------------------------------

  const preview = computed<{ ghost: GhostElements; removedNodeIds: string[] }>(() => {
    const ghost: GhostElements = { frames: [], nodes: [], links: [] }
    const removed: string[] = []
    const d = draft.value
    const b = baseline.value
    if (!d || !b) return { ghost, removedNodeIds: removed }

    let linkSeq = 0
    const link = (source: string, target: string) => {
      ghost.links.push({ id: `ghostlink:${linkSeq++}`, source, target })
    }

    // Global rack numbering is append-only, assigned in pod order —
    // the same rule the builder applies.
    let maxRack = 0
    for (const n of nodes()) {
      if (n.spec.rackId) maxRack = Math.max(maxRack, rackNumber(n.spec.rackId))
    }

    const superspines = nodes().filter((n) => n.spec.role === 'superspine')

    const ghostRack = (
      rackLabel: string,
      frameId: string,
      parentFrame: string,
      podName: string,
      servers: number,
      spineIds: string[],
    ) => {
      ghost.frames.push({ id: frameId, label: rackLabel, frame: 'rack', parent: parentFrame })
      const leafA = `ghost:${podName}-${rackLabel}-leaf-a`
      const leafB = `ghost:${podName}-${rackLabel}-leaf-b`
      ghost.nodes.push(
        { id: leafA, label: 'leaf-a', role: 'leaf', parent: frameId },
        { id: leafB, label: 'leaf-b', role: 'leaf', parent: frameId },
      )
      for (const spine of spineIds) {
        link(spine, leafA)
        link(spine, leafB)
      }
      link(leafA, leafB)
      for (let s = 1; s <= servers; s++) {
        const srv = `ghost:${podName}-${rackLabel}-server-${s}`
        ghost.nodes.push({ id: srv, label: `server-${s}`, role: 'server', parent: frameId })
        link(leafA, srv)
        link(leafB, srv)
      }
    }

    for (const p of d.pods) {
      const bp = b.pods.find((x) => x.name === p.name)
      const reality = podReality.value.get(p.name)

      if (!bp) {
        // A whole planned pod.
        const podFrame = `ghostpod:${p.name}`
        ghost.frames.push({ id: podFrame, label: p.name, frame: 'pod' })
        const spineIds: string[] = []
        for (let s = 1; s <= p.spines; s++) {
          const id = `ghost:${p.name}-spine-${s}`
          spineIds.push(id)
          ghost.nodes.push({ id, label: `spine-${s}`, role: 'spine', parent: podFrame })
          for (const ss of superspines) link(ss.meta.id, id)
        }
        for (let r = 1; r <= p.racks; r++) {
          maxRack++
          ghostRack(`rack-${maxRack}`, `ghostrack:${p.name}/rack-${maxRack}`, podFrame, p.name, p.serversPerRack, spineIds)
        }
        continue
      }

      if (!reality) continue
      const podFrame = `pod:${p.name}`
      const spineIds = reality.spines.map((n) => n.meta.id)

      // Added spines.
      for (let s = reality.spines.length + 1; s <= p.spines; s++) {
        const id = `ghost:${p.name}-spine-${s}`
        ghost.nodes.push({ id, label: `spine-${s}`, role: 'spine', parent: podFrame })
        for (const ss of superspines) link(ss.meta.id, id)
      }
      // Removed spines: tail first.
      for (let s = reality.spines.length - 1; s >= p.spines; s--) {
        removed.push(reality.spines[s].meta.id)
      }

      const rackIds = sortedRackIds(p.name)
      // Added racks.
      for (let r = rackIds.length + 1; r <= p.racks; r++) {
        maxRack++
        ghostRack(`rack-${maxRack}`, `ghostrack:${p.name}/rack-${maxRack}`, podFrame, p.name, p.serversPerRack, spineIds)
      }
      // Removed racks: tail slots.
      for (let r = rackIds.length - 1; r >= p.racks; r--) {
        const rack = reality.racks.get(rackIds[r])
        if (!rack) continue
        for (const n of [...rack.leaves, ...rack.servers]) removed.push(n.meta.id)
      }

      // Server count changes apply to every surviving rack.
      for (let r = 0; r < Math.min(rackIds.length, p.racks); r++) {
        const rack = reality.racks.get(rackIds[r])
        if (!rack) continue
        for (let s = rack.servers.length + 1; s <= p.serversPerRack; s++) {
          const id = `ghost:${p.name}-${rackIds[r]}-server-${s}`
          ghost.nodes.push({ id, label: `server-${s}`, role: 'server', parent: `rack:${p.name}/${rackIds[r]}` })
          for (const leaf of rack.leaves) link(leaf.meta.id, id)
        }
        for (let s = rack.servers.length - 1; s >= p.serversPerRack; s--) {
          removed.push(rack.servers[s].meta.id)
        }
      }
    }

    // Removed pods.
    for (const p of b.pods) {
      if (d.pods.some((x) => x.name === p.name)) continue
      const reality = podReality.value.get(p.name)
      if (!reality) continue
      for (const n of reality.spines) removed.push(n.meta.id)
      for (const rack of reality.racks.values()) {
        for (const n of [...rack.leaves, ...rack.servers]) removed.push(n.meta.id)
      }
    }

    return { ghost, removedNodeIds: removed }
  })

  return {
    draft,
    baseline,
    submitted,
    active,
    dirty,
    changes,
    preview,
    ensureDraft,
    discard,
    undo,
    canUndo,
    addPod,
    removePod,
    bump,
    canRemoveRack,
    basePod,
    draftPod,
  }
}
