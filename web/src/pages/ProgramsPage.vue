<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { useI18n } from 'vue-i18n'
import { useLabStore } from '../stores/lab'
import { labApi } from '../api/lab'
import ServerScopeSelect from '../components/ServerScopeSelect.vue'
import type { Package, Program } from '../types/models'

const store = useLabStore()
const { t } = useI18n()

const programs = ref<Program[]>([])
const packages = ref<Package[]>([])
const loading = ref(false)
const busy = ref<Record<string, boolean>>({})

const servers = computed(() => store.nodes.filter((n) => n.spec.role === 'server'))
const deployed = computed(() => (store.currentLab?.meta.generation ?? '0') !== '0')

// trafficgen modes are presets that become the first program argument.
const trafficgenModes = ['http-server', 'http-client', 'tcp-server', 'tcp-client', 'udp-server', 'udp-client']

const packageNames = computed(() => [...new Set(packages.value.map((p) => p.meta.name))])
const versionsOf = (name: string) =>
  packages.value.filter((p) => p.meta.name === name).map((p) => p.version)

// --- listing: poll while the page is open (agent status is cheap) ---
let pollTimer: number | undefined

async function refresh() {
  if (!store.currentLabId) {
    programs.value = []
    return
  }

  try {
    programs.value = await labApi.programs(store.currentLabId)
  } catch {
    /* lab gone or backend restarting; keep the last view */
  }
}

async function refreshPackages() {
  try {
    packages.value = await labApi.packages()
  } catch {
    /* backend restarting; keep the last view */
  }
}

onMounted(async () => {
  if (store.labs.length === 0) await store.refreshLabs()
  await store.refreshTopology()
  loading.value = true
  await Promise.all([refresh(), refreshPackages()])
  loading.value = false
  pollTimer = window.setInterval(refresh, 3000)
})
onBeforeUnmount(() => window.clearInterval(pollTimer))

async function onLabChange(id: string) {
  await store.selectLab(id)
  loading.value = true
  await refresh()
  loading.value = false
}

// --- create dialog ---
const dialogVisible = ref(false)
const form = ref({ name: '', serverIds: [] as string[], packageName: '', packageVersion: '', mode: 'http-server', args: '', type: 'simple', autoStart: false, restartPolicy: 'Always' })

const isTrafficgen = computed(() => form.value.packageName === 'trafficgen')

// Oneshot rejects Always, like systemd Restart=always with Type=oneshot.
const restartChoices = computed(() =>
  form.value.type === 'oneshot' ? ['Never', 'OnFailure'] : ['Never', 'OnFailure', 'Always'],
)

function onTypeChange(type: string) {
  if (type === 'oneshot' && form.value.restartPolicy === 'Always') form.value.restartPolicy = 'Never'
}

async function openCreate() {
  await refreshPackages()
  const pkg = packageNames.value.includes('trafficgen') ? 'trafficgen' : (packageNames.value[0] ?? '')
  form.value = {
    name: '',
    serverIds: [],
    packageName: pkg,
    packageVersion: versionsOf(pkg)[0] ?? '',
    mode: 'http-server',
    args: '',
    type: 'simple',
    autoStart: false,
    restartPolicy: 'Always',
  }
  dialogVisible.value = true
}

function onPackageChange(name: string) {
  form.value.packageVersion = versionsOf(name)[0] ?? ''
}

async function submitCreate() {
  const extra = form.value.args.trim() === '' ? [] : form.value.args.trim().split(/\s+/)
  const args = isTrafficgen.value ? [form.value.mode, ...extra] : extra
  try {
    const reply = await labApi.createProgram(store.currentLabId, {
      name: form.value.name,
      serverIds: form.value.serverIds,
      packageName: form.value.packageName,
      packageVersion: form.value.packageVersion,
      args,
      type: form.value.type,
      autoStart: form.value.autoStart,
      restartPolicy: form.value.restartPolicy,
    })
    dialogVisible.value = false
    ElMessage.success(t('programs.created', { name: form.value.name, count: reply.programs.length }))
    for (const f of reply.failures) {
      ElMessage.warning(`${f.serverName || f.serverId}: ${f.error}`)
    }
    await refresh()
  } catch (e) {
    ElMessage.error((e as Error).message)
  }
}

// --- row actions ---
async function withBusy(id: string, fn: () => Promise<unknown>) {
  busy.value = { ...busy.value, [id]: true }
  try {
    await fn()
    await refresh()
  } catch (e) {
    ElMessage.error((e as Error).message)
  } finally {
    busy.value = { ...busy.value, [id]: false }
  }
}

const power = (p: Program) =>
  withBusy(p.meta.id, () =>
    p.status.state === 'Running'
      ? labApi.stopProgram(store.currentLabId, p.meta.id)
      : labApi.startProgram(store.currentLabId, p.meta.id),
  )

async function remove(p: Program) {
  try {
    await ElMessageBox.confirm(t('programs.deleteConfirm', { name: p.meta.name }), t('programs.deleteTitle'))
  } catch {
    return
  }

  await withBusy(p.meta.id, () => labApi.deleteProgram(store.currentLabId, p.meta.id))
}

// --- upgrade / rollback dialog ---
const upgradeProgram = ref<Program | null>(null)
const upgradeVersion = ref('')

const upgradeChoices = computed(() =>
  upgradeProgram.value
    ? versionsOf(upgradeProgram.value.spec.packageName).filter(
        (v) => v !== upgradeProgram.value?.spec.packageVersion,
      )
    : [],
)

async function openUpgrade(p: Program) {
  await refreshPackages()
  upgradeProgram.value = p
  upgradeVersion.value = ''
}

async function submitUpgrade() {
  const p = upgradeProgram.value
  if (!p || !upgradeVersion.value) return

  upgradeProgram.value = null
  await withBusy(p.meta.id, () => labApi.upgradeProgram(store.currentLabId, p.meta.id, upgradeVersion.value))
}

// --- logs drawer ---
const logProgram = ref<Program | null>(null)
const logContent = ref('')
const logLoading = ref(false)

async function openLogs(p: Program) {
  logProgram.value = p
  await refreshLogs()
}

async function refreshLogs() {
  if (!logProgram.value) return

  logLoading.value = true
  try {
    logContent.value = await labApi.programLogs(store.currentLabId, logProgram.value.meta.id, 200)
  } catch (e) {
    logContent.value = ''
    ElMessage.error((e as Error).message)
  } finally {
    logLoading.value = false
  }
}

function stateTag(state: string): string {
  switch (state) {
    case 'Running':
      return 'success'
    case 'Failed':
      return 'danger'
    case 'Unknown':
      return 'warning'
    default:
      return 'info'
  }
}
</script>

<template>
  <div>
    <div class="header">
      <h2>{{ t('programs.title') }}</h2>
      <div class="header-actions">
        <el-button type="primary" :disabled="!deployed || servers.length === 0" @click="openCreate">
          {{ t('programs.create') }}
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

    <el-alert v-if="!deployed" type="info" :closable="false" :title="t('programs.needDeploy')" class="hint" />

    <el-table :data="programs" v-loading="loading">
      <el-table-column prop="meta.name" :label="t('common.name')" width="140" />
      <el-table-column prop="spec.serverName" :label="t('programs.server')" width="170" />
      <el-table-column :label="t('programs.package')" width="170">
        <template #default="{ row }">
          <code>{{ row.spec.packageName }}@{{ row.spec.packageVersion }}</code>
        </template>
      </el-table-column>
      <el-table-column :label="t('programs.command')">
        <template #default="{ row }">
          <code>{{ row.spec.entrypoint }} {{ (row.spec.args ?? []).join(' ') }}</code>
        </template>
      </el-table-column>
      <el-table-column :label="t('programs.state')" width="140">
        <template #default="{ row }">
          <el-tag size="small" :type="stateTag(row.status.state)">{{ row.status.state }}</el-tag>
          <div class="sub" v-if="row.status.state === 'Running'">
            pid {{ row.status.pid }} · ↻{{ row.status.restarts ?? 0 }}
          </div>
          <div class="sub error" v-else-if="row.status.lastError">{{ row.status.lastError }}</div>
        </template>
      </el-table-column>
      <el-table-column :label="t('programs.type')" width="110">
        <template #default="{ row }">
          <span>{{ row.spec.type === 'oneshot' ? t('programs.typeOneshot') : t('programs.typeSimple') }}</span>
          <div class="sub" v-if="row.spec.autoStart">{{ t('programs.autoStart') }}</div>
        </template>
      </el-table-column>
      <el-table-column prop="spec.restartPolicy" :label="t('programs.restartPolicy')" width="105" />
      <el-table-column :label="t('common.actions')" width="290">
        <template #default="{ row }">
          <el-button
            size="small"
            :type="row.status.state === 'Running' ? 'danger' : 'success'"
            :loading="busy[row.meta.id]"
            :disabled="!deployed"
            @click="power(row)"
          >
            {{ row.status.state === 'Running' ? t('programs.stop') : row.spec.type === 'oneshot' ? t('programs.run') : t('programs.start') }}
          </el-button>
          <el-button size="small" :disabled="!deployed" @click="openUpgrade(row)">{{ t('programs.upgrade') }}</el-button>
          <el-button size="small" :disabled="!deployed" @click="openLogs(row)">{{ t('programs.logs') }}</el-button>
          <el-button size="small" type="danger" plain :loading="busy[row.meta.id]" @click="remove(row)">
            {{ t('common.delete') }}
          </el-button>
        </template>
      </el-table-column>
    </el-table>

    <el-dialog v-model="dialogVisible" :title="t('programs.createTitle')" width="480px">
      <el-form label-width="110px">
        <el-form-item :label="t('common.name')">
          <el-input v-model="form.name" placeholder="web-1" />
        </el-form-item>
        <el-form-item :label="t('programs.server')">
          <ServerScopeSelect v-model="form.serverIds" :nodes="store.nodes" :placeholder="t('programs.scopePlaceholder')" />
        </el-form-item>
        <el-form-item :label="t('programs.package')">
          <el-select v-model="form.packageName" style="width: 100%" @change="onPackageChange">
            <el-option v-for="n in packageNames" :key="n" :label="n" :value="n" />
          </el-select>
        </el-form-item>
        <el-form-item :label="t('programs.version')">
          <el-select v-model="form.packageVersion" style="width: 100%">
            <el-option v-for="v in versionsOf(form.packageName)" :key="v" :label="v" :value="v" />
          </el-select>
        </el-form-item>
        <el-form-item v-if="isTrafficgen" :label="t('programs.mode')">
          <el-select v-model="form.mode" style="width: 100%">
            <el-option v-for="m in trafficgenModes" :key="m" :label="m" :value="m" />
          </el-select>
        </el-form-item>
        <el-form-item :label="t('programs.args')">
          <el-input v-model="form.args" :placeholder="t('programs.argsPlaceholder')" />
        </el-form-item>
        <el-form-item :label="t('programs.type')">
          <el-radio-group v-model="form.type" @change="onTypeChange">
            <el-radio-button value="simple">{{ t('programs.typeSimple') }}</el-radio-button>
            <el-radio-button value="oneshot">{{ t('programs.typeOneshot') }}</el-radio-button>
          </el-radio-group>
        </el-form-item>
        <el-form-item :label="t('programs.autoStart')">
          <el-switch v-model="form.autoStart" />
          <span class="form-hint">{{ t('programs.autoStartHint') }}</span>
        </el-form-item>
        <el-form-item :label="t('programs.restartPolicy')">
          <el-select v-model="form.restartPolicy" style="width: 100%">
            <el-option v-for="p in restartChoices" :key="p" :label="p" :value="p" />
          </el-select>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="dialogVisible = false">{{ t('common.cancel') }}</el-button>
        <el-button
          type="primary"
          :disabled="!form.name || form.serverIds.length === 0 || !form.packageName || !form.packageVersion"
          @click="submitCreate"
        >
          {{ t('common.create') }}
        </el-button>
      </template>
    </el-dialog>

    <el-dialog
      :model-value="!!upgradeProgram"
      :title="t('programs.upgradeTitle', { name: upgradeProgram?.meta.name ?? '' })"
      width="420px"
      @close="upgradeProgram = null"
    >
      <p class="upgrade-hint">
        {{ t('programs.currentVersion') }}:
        <code>{{ upgradeProgram?.spec.packageName }}@{{ upgradeProgram?.spec.packageVersion }}</code>
      </p>
      <el-select v-model="upgradeVersion" style="width: 100%" :placeholder="t('programs.targetVersion')">
        <el-option v-for="v in upgradeChoices" :key="v" :label="v" :value="v" />
      </el-select>
      <el-alert
        v-if="upgradeChoices.length === 0"
        type="info"
        :closable="false"
        :title="t('programs.noOtherVersions')"
        class="hint-top"
      />
      <template #footer>
        <el-button @click="upgradeProgram = null">{{ t('common.cancel') }}</el-button>
        <el-button type="primary" :disabled="!upgradeVersion" @click="submitUpgrade">
          {{ t('programs.switchVersion') }}
        </el-button>
      </template>
    </el-dialog>

    <el-drawer
      :model-value="!!logProgram"
      :title="t('programs.logsTitle', { name: logProgram?.meta.name ?? '' })"
      size="55%"
      @close="logProgram = null"
    >
      <div class="log-toolbar">
        <el-button size="small" :loading="logLoading" @click="refreshLogs">{{ t('topology.refresh') }}</el-button>
      </div>
      <pre class="log">{{ logContent || t('programs.noLogs') }}</pre>
    </el-drawer>
  </div>
</template>

<style scoped>
.header { display: flex; justify-content: space-between; align-items: center; margin-bottom: 12px; }
.header h2 { margin: 0; }
.header-actions { display: flex; gap: 12px; align-items: center; }
.hint { margin-bottom: 12px; }
.hint-top { margin-top: 12px; }
.upgrade-hint { margin: 0 0 12px; font-size: 13px; }
.form-hint { margin-left: 10px; font-size: 12px; color: var(--el-text-color-secondary); }
.sub { font-size: 11px; color: var(--el-text-color-secondary); }
.sub.error { color: var(--el-color-danger); }
.log-toolbar { display: flex; justify-content: flex-end; margin-bottom: 8px; }
.log {
  background: var(--el-fill-color-light);
  padding: 12px;
  border-radius: 4px;
  font-size: 12px;
  line-height: 1.5;
  overflow: auto;
  white-space: pre-wrap;
  word-break: break-all;
}
code { font-size: 12px; }
</style>
