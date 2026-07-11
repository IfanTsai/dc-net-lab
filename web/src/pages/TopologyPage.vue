<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { ElMessage } from 'element-plus'
import { useI18n } from 'vue-i18n'
import { useLabStore } from '../stores/lab'
import type { Link, Node } from '../types/models'
import TopologyCanvas from '../components/TopologyCanvas.vue'
import TerminalPanel from '../components/TerminalPanel.vue'

const store = useLabStore()
const { t } = useI18n()
const selectedNode = ref<Node | null>(null)
const selectedLink = ref<Link | null>(null)
const terminal = ref<InstanceType<typeof TerminalPanel> | null>(null)

onMounted(async () => {
  if (store.labs.length === 0) await store.refreshLabs()
  await store.refreshTopology()
  store.startObserving()
})
onBeforeUnmount(() => store.stopObserving())

// Keep the open drawer in sync with incoming observations.
watch(
  () => store.nodes,
  (nodes) => {
    if (!selectedNode.value) return
    const fresh = nodes.find((n) => n.meta.id === selectedNode.value!.meta.id)
    if (fresh) selectedNode.value = fresh
  },
)

const nodeLinks = computed(() =>
  selectedNode.value
    ? store.links.filter(
        (l) =>
          l.spec.endpointA.nodeId === selectedNode.value!.meta.id ||
          l.spec.endpointB.nodeId === selectedNode.value!.meta.id,
      )
    : [],
)

async function onLabChange(id: string) {
  selectedNode.value = null
  selectedLink.value = null
  terminal.value?.closeAll()
  await store.selectLab(id)
}

// Double-click on a device: close the detail drawer the first tap
// opened and drop into the node's shell.
function onOpenTerminal(node: Node) {
  if (!store.currentLabId) return

  selectedNode.value = null
  selectedLink.value = null
  terminal.value?.open(store.currentLabId, node)
}

// --- Power control ---
// The lab is powerable once it has a deployed generation; "running"
// drives both the DC button label and the link-flow animation.
const deployed = computed(() => (store.currentLab?.meta.generation ?? '0') !== '0')
const labRunning = computed(() =>
  ['Running', 'Degraded'].includes(store.currentLab?.meta.phase ?? ''),
)
const labBusy = ref(false)
const nodeBusy = ref(false)

async function powerLab() {
  labBusy.value = true
  try {
    await store.powerLab(!labRunning.value)
  } catch (e) {
    ElMessage.error((e as Error).message)
  } finally {
    labBusy.value = false
  }
}

async function powerNode() {
  if (!selectedNode.value) return

  const target = selectedNode.value
  nodeBusy.value = true
  try {
    const node = await store.powerNode(target.meta.id, target.meta.phase === 'Stopped')
    if (selectedNode.value?.meta.id === node.meta.id) selectedNode.value = node
  } catch (e) {
    ElMessage.error((e as Error).message)
  } finally {
    nodeBusy.value = false
  }
}

function linkKindLabel(l: Link): string {
  const vlan = l.spec.vlanId ?? 0
  switch (l.spec.kind) {
    case 'server-access':
      return t('topology.kindServerAccess', { vlan })
    case 'mlag-peer':
      return t('topology.kindMlagPeer', { vlan })
    default:
      return t('topology.kindFabric')
  }
}
</script>

<template>
  <div class="page">
    <div class="header">
      <h2>{{ t('topology.title') }} <span class="hint">{{ t('topology.doubleClickHint') }}</span></h2>
      <div class="header-actions">
        <el-button
          v-if="deployed"
          :type="labRunning ? 'danger' : 'success'"
          :loading="labBusy"
          @click="powerLab"
        >
          {{ labRunning ? t('topology.stopDc') : t('topology.startDc') }}
        </el-button>
        <el-select
          :model-value="store.currentLabId"
          style="width: 240px"
          :placeholder="t('topology.selectLab')"
          @change="onLabChange"
        >
          <el-option v-for="l in store.labs" :key="l.meta.id" :label="l.meta.name" :value="l.meta.id" />
        </el-select>
      </div>
    </div>

    <div class="body">
      <el-empty v-if="store.nodes.length === 0" :description="t('topology.empty')" />
      <TopologyCanvas
        v-else
        :nodes="store.nodes"
        :links="store.links"
        @select-node="selectedNode = $event; selectedLink = null"
        @select-link="selectedLink = $event; selectedNode = null"
        @open-terminal="onOpenTerminal"
      />
    </div>

    <TerminalPanel ref="terminal" />

    <!-- Non-modal: a modal mask would swallow the second click of a
         double-click on the canvas, breaking terminal access. -->
    <el-drawer :model-value="!!selectedNode" :title="selectedNode?.meta.name" size="420px" :modal="false" @close="selectedNode = null">
      <template v-if="selectedNode">
        <el-button
          v-if="deployed"
          :type="selectedNode.meta.phase === 'Stopped' ? 'success' : 'danger'"
          :loading="nodeBusy"
          size="small"
          class="node-power"
          @click="powerNode"
        >
          {{ selectedNode.meta.phase === 'Stopped' ? t('topology.startNode') : t('topology.stopNode') }}
        </el-button>
        <el-descriptions :column="1" border size="small">
          <el-descriptions-item :label="t('topology.role')">{{ selectedNode.spec.role }}</el-descriptions-item>
          <el-descriptions-item :label="t('topology.phase')">{{ selectedNode.meta.phase }}</el-descriptions-item>
          <template v-if="selectedNode.status?.lastObserved">
            <el-descriptions-item :label="t('topology.runtimeState')">
              {{ selectedNode.status.runtimeState }}
            </el-descriptions-item>
            <el-descriptions-item :label="t('topology.bgpSessions')">
              {{ selectedNode.status.bgpEstablished }} / {{ selectedNode.status.bgpConfigured }}
            </el-descriptions-item>
            <el-descriptions-item :label="t('topology.routes')">
              {{ selectedNode.status.routeCount }}
            </el-descriptions-item>
            <el-descriptions-item :label="t('topology.interfacesUp')">
              {{ selectedNode.status.interfacesUp }} / {{ selectedNode.status.interfacesTotal }}
            </el-descriptions-item>
            <el-descriptions-item :label="t('topology.lastObserved')">
              {{ new Date(selectedNode.status.lastObserved).toLocaleTimeString() }}
            </el-descriptions-item>
          </template>
          <el-descriptions-item :label="t('topology.asn')" v-if="selectedNode.spec.asn">
            {{ selectedNode.spec.asn }}
          </el-descriptions-item>
          <el-descriptions-item :label="t('topology.loopback')" v-if="selectedNode.spec.loopback">
            {{ selectedNode.spec.loopback }}
          </el-descriptions-item>
          <el-descriptions-item :label="t('topology.pod')" v-if="selectedNode.spec.podId">
            {{ selectedNode.spec.podId }}
          </el-descriptions-item>
          <el-descriptions-item :label="t('topology.rack')" v-if="selectedNode.spec.rackId">
            {{ selectedNode.spec.rackId }}
          </el-descriptions-item>
          <el-descriptions-item :label="t('topology.mlagPeer')" v-if="selectedNode.spec.mlagPeer">
            {{ selectedNode.spec.mlagPeer }}
          </el-descriptions-item>
          <el-descriptions-item :label="t('topology.vlan')" v-if="selectedNode.spec.vlanId">
            {{ selectedNode.spec.vlanId }}
          </el-descriptions-item>
          <el-descriptions-item :label="t('topology.vlanIp')" v-if="selectedNode.spec.vlanIp">
            {{ selectedNode.spec.vlanIp }}
          </el-descriptions-item>
          <el-descriptions-item :label="t('topology.serverAddress')" v-if="selectedNode.spec.address">
            {{ selectedNode.spec.address }}
          </el-descriptions-item>
          <el-descriptions-item :label="t('topology.gateway')" v-if="selectedNode.spec.gatewayIp || selectedNode.spec.defaultGateway">
            {{ selectedNode.spec.gatewayIp ?? selectedNode.spec.defaultGateway }}
          </el-descriptions-item>
          <el-descriptions-item :label="t('topology.gatewayMac')" v-if="selectedNode.spec.gatewayMac">
            {{ selectedNode.spec.gatewayMac }}
          </el-descriptions-item>
          <el-descriptions-item :label="t('topology.bgpPeers')" v-if="selectedNode.spec.bgpPeers?.length">
            {{ selectedNode.spec.bgpPeers.join(', ') }}
          </el-descriptions-item>
        </el-descriptions>

        <h4>{{ t('topology.interfaces') }}</h4>
        <el-table :data="nodeLinks" size="small">
          <el-table-column :label="t('topology.interface')" width="90">
            <template #default="{ row }">
              {{ row.spec.endpointA.nodeId === selectedNode!.meta.id ? row.spec.endpointA.interface : row.spec.endpointB.interface }}
            </template>
          </el-table-column>
          <el-table-column :label="t('topology.address')" width="130">
            <template #default="{ row }">
              {{ row.spec.endpointA.nodeId === selectedNode!.meta.id ? row.spec.endpointA.address : row.spec.endpointB.address }}
            </template>
          </el-table-column>
          <el-table-column :label="t('topology.peer')">
            <template #default="{ row }">
              {{ row.spec.endpointA.nodeId === selectedNode!.meta.id ? row.spec.endpointB.nodeName : row.spec.endpointA.nodeName }}
            </template>
          </el-table-column>
        </el-table>
      </template>
    </el-drawer>

    <el-drawer :model-value="!!selectedLink" :title="selectedLink?.meta.name" size="420px" :modal="false" @close="selectedLink = null">
      <template v-if="selectedLink">
        <el-descriptions :column="1" border size="small">
          <el-descriptions-item :label="t('topology.kind')">
            {{ linkKindLabel(selectedLink) }}
          </el-descriptions-item>
          <el-descriptions-item :label="t('topology.endpointA')">
            {{ selectedLink.spec.endpointA.nodeName }}:{{ selectedLink.spec.endpointA.interface }}
            <template v-if="selectedLink.spec.endpointA.address">({{ selectedLink.spec.endpointA.address }})</template>
          </el-descriptions-item>
          <el-descriptions-item :label="t('topology.endpointB')">
            {{ selectedLink.spec.endpointB.nodeName }}:{{ selectedLink.spec.endpointB.interface }}
            <template v-if="selectedLink.spec.endpointB.address">({{ selectedLink.spec.endpointB.address }})</template>
          </el-descriptions-item>
          <el-descriptions-item :label="t('topology.mtu')">{{ selectedLink.spec.mtu }}</el-descriptions-item>
        </el-descriptions>
      </template>
    </el-drawer>
  </div>
</template>

<style scoped>
/* Fill the viewport: el-main has 20px padding top and bottom. */
.page {
  height: calc(100vh - 40px);
  display: flex;
  flex-direction: column;
}
.header { display: flex; justify-content: space-between; align-items: center; margin-bottom: 12px; }
.header-actions { display: flex; gap: 12px; align-items: center; }
.node-power { margin-bottom: 12px; }
.header h2 { margin: 0; }
.hint { font-size: 12px; font-weight: normal; color: var(--el-text-color-secondary); margin-left: 8px; }
/* The non-modal drawers still mount a full-viewport positioning
   wrapper that swallows clicks (breaking canvas double-click); let
   events pass through it and keep only the panel interactive. */
.page :deep(.el-modal-drawer),
.page :deep(.el-overlay) { pointer-events: none; }
.page :deep(.el-drawer) { pointer-events: auto; }
.body { flex: 1; min-height: 0; }
h4 { margin: 16px 0 8px; }
</style>
