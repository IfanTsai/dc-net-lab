<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { useI18n } from 'vue-i18n'
import { labApi } from '../api/lab'
import { useLabStore } from '../stores/lab'
import ServerScopeSelect from '../components/ServerScopeSelect.vue'
import type { Node, Package, ServerInstallResult } from '../types/models'

const { t } = useI18n()
const store = useLabStore()

const packages = ref<Package[]>([])
const loading = ref(false)
const uploading = ref(false)

async function refresh() {
  loading.value = true
  try {
    packages.value = await labApi.packages()
  } catch (e) {
    ElMessage.error((e as Error).message)
  } finally {
    loading.value = false
  }
}

onMounted(async () => {
  if (store.labs.length === 0) await store.refreshLabs()
  await refresh()
})

// --- upload: the file becomes the protobuf bytes field (base64) ---
const fileInput = ref<HTMLInputElement | null>(null)

function pickFile() {
  fileInput.value?.click()
}

async function onFileChosen(ev: Event) {
  const input = ev.target as HTMLInputElement
  const file = input.files?.[0]
  input.value = ''
  if (!file) return

  uploading.value = true
  try {
    const buf = new Uint8Array(await file.arrayBuffer())
    let binary = ''
    const chunk = 0x8000
    for (let i = 0; i < buf.length; i += chunk) {
      binary += String.fromCharCode(...buf.subarray(i, i + chunk))
    }
    const pkg = await labApi.uploadPackage(btoa(binary))
    ElMessage.success(t('packages.uploaded', { name: pkg.meta.name, version: pkg.version }))
    await refresh()
  } catch (e) {
    ElMessage.error((e as Error).message)
  } finally {
    uploading.value = false
  }
}

async function remove(p: Package) {
  try {
    await ElMessageBox.confirm(
      t('packages.deleteConfirm', { name: p.meta.name, version: p.version }),
      t('packages.deleteTitle'),
    )
  } catch {
    return
  }

  try {
    await labApi.deletePackage(p.meta.name, p.version)
    await refresh()
  } catch (e) {
    ElMessage.error((e as Error).message)
  }
}

// --- install onto servers: deliver the artifact without a program
// (apt install without a service unit); empty selection = all servers ---
const installPkg = ref<Package | null>(null)
const installLabId = ref('')
const installServerIds = ref<string[]>([])
const installNodes = ref<Node[]>([])
const installing = ref(false)
const installResults = ref<ServerInstallResult[]>([])

const installServers = computed(() => installNodes.value.filter((n) => n.spec.role === 'server'))

const installLabDeployed = computed(
  () => (store.labs.find((l) => l.meta.id === installLabId.value)?.meta.generation ?? '0') !== '0',
)

async function openInstall(p: Package) {
  installPkg.value = p
  installLabId.value = store.currentLabId || (store.labs[0]?.meta.id ?? '')
  installServerIds.value = []
  installResults.value = []
  await loadInstallServers()
}

async function loadInstallServers() {
  installServerIds.value = []
  installNodes.value = []
  if (!installLabId.value) return

  try {
    installNodes.value = await labApi.nodes(installLabId.value)
  } catch (e) {
    ElMessage.error((e as Error).message)
  }
}

async function submitInstall() {
  const p = installPkg.value
  if (!p || !installLabId.value) return

  installing.value = true
  installResults.value = []
  try {
    const results = await labApi.installPackage(installLabId.value, p.meta.name, p.version, installServerIds.value)
    installResults.value = results
    const failed = results.filter((r) => r.error).length
    if (failed === 0) {
      ElMessage.success(t('packages.installed', { name: p.meta.name, version: p.version, count: results.length }))
      installPkg.value = null
    } else {
      ElMessage.warning(t('packages.installPartial', { failed, total: results.length }))
    }
  } catch (e) {
    ElMessage.error((e as Error).message)
  } finally {
    installing.value = false
  }
}

function formatSize(bytes?: string): string {
  const n = Number(bytes ?? 0)
  if (n >= 1 << 20) return `${(n / (1 << 20)).toFixed(1)} MiB`
  if (n >= 1 << 10) return `${(n / (1 << 10)).toFixed(1)} KiB`
  return `${n} B`
}
</script>

<template>
  <div>
    <div class="header">
      <h2>{{ t('packages.title') }}</h2>
      <div class="header-actions">
        <el-button type="primary" :loading="uploading" @click="pickFile">
          {{ t('packages.upload') }}
        </el-button>
        <input ref="fileInput" type="file" accept=".gz,.tgz,application/gzip" class="hidden-input" @change="onFileChosen" />
      </div>
    </div>

    <el-alert type="info" :closable="false" :title="t('packages.hint')" class="hint" />

    <el-table :data="packages" v-loading="loading">
      <el-table-column prop="meta.name" :label="t('common.name')" width="160">
        <template #default="{ row }">
          {{ row.meta.name }}
          <el-tag v-if="row.builtin" size="small" type="info" class="builtin-tag">builtin</el-tag>
        </template>
      </el-table-column>
      <el-table-column prop="version" :label="t('packages.version')" width="120" />
      <el-table-column prop="entrypoint" :label="t('packages.entrypoint')" width="180">
        <template #default="{ row }"><code>{{ row.entrypoint }}</code></template>
      </el-table-column>
      <el-table-column prop="description" :label="t('packages.description')" min-width="200" />
      <el-table-column :label="t('packages.size')" width="100">
        <template #default="{ row }">{{ formatSize(row.sizeBytes) }}</template>
      </el-table-column>
      <el-table-column :label="t('packages.digest')" width="130">
        <template #default="{ row }"><code>{{ (row.sha256 ?? '').slice(0, 12) }}</code></template>
      </el-table-column>
      <el-table-column :label="t('common.actions')" width="200">
        <template #default="{ row }">
          <el-button size="small" @click="openInstall(row)">{{ t('packages.install') }}</el-button>
          <el-button size="small" type="danger" plain :disabled="row.builtin" @click="remove(row)">
            {{ t('common.delete') }}
          </el-button>
        </template>
      </el-table-column>
    </el-table>

    <el-dialog
      :model-value="!!installPkg"
      :title="t('packages.installTitle', { name: installPkg?.meta.name ?? '', version: installPkg?.version ?? '' })"
      width="480px"
      @close="installPkg = null"
    >
      <el-form label-width="90px">
        <el-form-item :label="t('packages.installLab')">
          <el-select v-model="installLabId" style="width: 100%" @change="loadInstallServers">
            <el-option v-for="l in store.labs" :key="l.meta.id" :label="l.meta.name" :value="l.meta.id" />
          </el-select>
        </el-form-item>
        <el-form-item :label="t('programs.server')">
          <ServerScopeSelect
            v-model="installServerIds"
            :nodes="installNodes"
            :placeholder="t('packages.allServers')"
          />
        </el-form-item>
      </el-form>
      <el-alert
        v-if="!installLabDeployed"
        type="info"
        :closable="false"
        :title="t('programs.needDeploy')"
      />
      <div v-if="installResults.some((r) => r.error)" class="install-results">
        <div v-for="r in installResults" :key="r.serverId" :class="['result', r.error ? 'failed' : 'ok']">
          {{ r.serverName }}: {{ r.error || 'ok' }}
        </div>
      </div>
      <template #footer>
        <el-button @click="installPkg = null">{{ t('common.cancel') }}</el-button>
        <el-button
          type="primary"
          :loading="installing"
          :disabled="!installLabDeployed || installServers.length === 0"
          @click="submitInstall"
        >
          {{ t('packages.install') }}
        </el-button>
      </template>
    </el-dialog>
  </div>
</template>

<style scoped>
.header { display: flex; justify-content: space-between; align-items: center; margin-bottom: 12px; }
.header h2 { margin: 0; }
.header-actions { display: flex; gap: 12px; align-items: center; }
.hidden-input { display: none; }
.hint { margin-bottom: 12px; }
.builtin-tag { margin-left: 6px; }
.install-results { margin-top: 12px; font-size: 12px; max-height: 180px; overflow: auto; }
.install-results .result.failed { color: var(--el-color-danger); }
.install-results .result.ok { color: var(--el-text-color-secondary); }
code { font-size: 12px; }
</style>
