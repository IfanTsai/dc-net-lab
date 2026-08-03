import { defineStore } from 'pinia'
import { labApi } from '../api/lab'
import type { Lab, Link, Node, Observation, Operation } from '../types/models'

// The observation socket lives outside the store state so Pinia never
// proxies it.
let obsSocket: WebSocket | null = null

// The operations socket is shared by every consumer (operations page,
// progress cards) and refcounted so the last one closes it.
let opsSocket: WebSocket | null = null
let opsFeedRefs = 0

export const useLabStore = defineStore('lab', {
  state: () => ({
    labs: [] as Lab[],
    currentLabId: '' as string,
    nodes: [] as Node[],
    links: [] as Link[],
    operations: [] as Operation[],
    loading: false,
    observing: false,
    // Whether the operations WebSocket is connected; consumers poll
    // as a fallback while it is not.
    opsFeedLive: false,
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
      if (opsFeedRefs > 0) this.connectOperationsFeed()
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
    // acquireOperationsFeed subscribes to the lab's operation stream
    // (snapshot on connect, then every state change); consumers pair
    // it with releaseOperationsFeed so the last one closes the socket.
    acquireOperationsFeed() {
      opsFeedRefs++
      if (opsFeedRefs === 1) this.connectOperationsFeed()
    },
    releaseOperationsFeed() {
      opsFeedRefs = Math.max(0, opsFeedRefs - 1)
      if (opsFeedRefs === 0) {
        this.opsFeedLive = false
        opsSocket?.close()
        opsSocket = null
      }
    },
    connectOperationsFeed() {
      opsSocket?.close()
      opsSocket = null
      this.opsFeedLive = false
      if (!this.currentLabId) return

      const proto = location.protocol === 'https:' ? 'wss' : 'ws'
      const ws = new WebSocket(`${proto}://${location.host}/ws/v1/labs/${this.currentLabId}/operations`)
      opsSocket = ws
      ws.onopen = () => {
        if (opsSocket === ws) this.opsFeedLive = true
      }
      ws.onmessage = (ev) => {
        try {
          const msg = JSON.parse(ev.data as string)
          if (msg.type === 'operations') this.operations = msg.operations as Operation[]
          if (msg.type === 'operation') this.applyOperation(msg.operation as Operation)
        } catch {
          /* ignore malformed frames */
        }
      }
      ws.onclose = () => {
        // Reconnect while someone still holds the feed (e.g. after a
        // controller restart); consumers poll in the meantime.
        if (opsSocket === ws) {
          this.opsFeedLive = false
          setTimeout(() => {
            if (opsFeedRefs > 0 && opsSocket === ws) this.connectOperationsFeed()
          }, 3000)
        }
      }
    },
    applyOperation(op: Operation) {
      const i = this.operations.findIndex((o) => o.id === op.id)
      if (i >= 0) {
        this.operations = this.operations.map((o) => (o.id === op.id ? op : o))
      } else {
        // New operations go first: the REST list is newest-first.
        this.operations = [op, ...this.operations]
      }
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
    // repairLab redeploys the current generation to re-attach any
    // interfaces the runtime dropped (e.g. a host Docker restart),
    // waiting for the operation the same way powerLab does.
    async repairLab() {
      if (!this.currentLabId) return
      const { operationId } = await labApi.repair(this.currentLabId)

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
