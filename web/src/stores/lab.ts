import { defineStore } from 'pinia'
import { labApi } from '../api/lab'
import type { Lab, Link, Node, Observation, Operation } from '../types/models'

// The observation socket lives outside the store state so Pinia never
// proxies it.
let obsSocket: WebSocket | null = null

export const useLabStore = defineStore('lab', {
  state: () => ({
    labs: [] as Lab[],
    currentLabId: '' as string,
    nodes: [] as Node[],
    links: [] as Link[],
    operations: [] as Operation[],
    loading: false,
    observing: false,
  }),
  getters: {
    currentLab(state): Lab | undefined {
      return state.labs.find((l) => l.meta.id === state.currentLabId)
    },
  },
  actions: {
    async refreshLabs() {
      this.loading = true
      try {
        this.labs = await labApi.list()
        if (!this.currentLabId && this.labs.length > 0) {
          this.currentLabId = this.labs[0].meta.id
        }
      } finally {
        this.loading = false
      }
    },
    async selectLab(id: string) {
      this.currentLabId = id
      await this.refreshTopology()
      if (this.observing) this.startObserving()
    },
    // startObserving subscribes to the lab's observation stream and
    // merges every sweep into nodes (fresh array: the canvas watches
    // array identity). Call stopObserving when leaving the page.
    startObserving() {
      this.stopObserving()
      if (!this.currentLabId) return
      this.observing = true

      const proto = location.protocol === 'https:' ? 'wss' : 'ws'
      const ws = new WebSocket(`${proto}://${location.host}/ws/v1/labs/${this.currentLabId}/topology`)
      obsSocket = ws
      ws.onmessage = (ev) => {
        try {
          const msg = JSON.parse(ev.data as string)
          if (msg.type === 'observations') this.applyObservations(msg.nodes as Observation[])
        } catch {
          /* ignore malformed frames */
        }
      }
      ws.onclose = () => {
        // Reconnect while the page still wants observations (e.g.
        // after a controller restart).
        if (this.observing && obsSocket === ws) {
          setTimeout(() => {
            if (this.observing) this.startObserving()
          }, 3000)
        }
      }
    },
    stopObserving() {
      this.observing = false
      obsSocket?.close()
      obsSocket = null
    },
    applyObservations(obs: Observation[]) {
      const byID = new Map(obs.map((o) => [o.nodeId, o]))
      let phaseChanged = false
      this.nodes = this.nodes.map((n) => {
        const o = byID.get(n.meta.id)
        if (!o) return n
        if (n.meta.phase !== o.phase) phaseChanged = true
        return {
          ...n,
          meta: { ...n.meta, phase: o.phase as Node['meta']['phase'] },
          status: {
            ...n.status,
            runtimeState: o.runtimeState,
            bgpEstablished: o.bgpEstablished,
            bgpConfigured: o.bgpConfigured,
            routeCount: o.routeCount,
            interfacesUp: o.interfacesUp,
            interfacesTotal: o.interfacesTotal,
            interfaces: o.interfaces,
            vrrpState: o.vrrpState,
            agentState: o.agentState,
            lastObserved: o.lastObserved,
          },
        }
      })
      // Node drift (manual docker pause, WSL restart...) also moves
      // the lab phase; refresh it so the DC power button stays honest.
      if (phaseChanged) void this.refreshLabs()
    },
    async refreshTopology() {
      if (!this.currentLabId) return
      const [nodes, links] = await Promise.all([
        labApi.nodes(this.currentLabId),
        labApi.links(this.currentLabId),
      ])
      this.nodes = nodes
      this.links = links
    },
    async refreshOperations() {
      if (!this.currentLabId) return
      this.operations = await labApi.operations(this.currentLabId)
    },
    // powerLab starts/stops every device of the current lab and waits
    // for the async operation to finish before refreshing state.
    async powerLab(on: boolean) {
      if (!this.currentLabId) return
      const { operationId } = on
        ? await labApi.start(this.currentLabId)
        : await labApi.stop(this.currentLabId)

      for (;;) {
        const op = await labApi.getOperation(operationId)
        if (op.state === 'Succeeded' || op.state === 'Failed' || op.state === 'Cancelled') {
          await Promise.all([this.refreshLabs(), this.refreshTopology()])
          if (op.state !== 'Succeeded') throw new Error(op.error?.message ?? `operation ${op.state}`)
          return
        }
        await new Promise((r) => setTimeout(r, 500))
      }
    },
    // powerNode starts/stops one device (synchronous on the backend).
    async powerNode(nodeId: string, on: boolean): Promise<Node> {
      const node = on
        ? await labApi.startNode(this.currentLabId, nodeId)
        : await labApi.stopNode(this.currentLabId, nodeId)

      // Replace the array (not splice): TopologyCanvas watches the
      // array identity to re-render node up/down state.
      this.nodes = this.nodes.map((n) => (n.meta.id === node.meta.id ? node : n))
      await this.refreshLabs()
      return node
    },
  },
})
