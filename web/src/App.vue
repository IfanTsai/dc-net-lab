<script setup lang="ts">
import { computed } from 'vue'
import { useRoute } from 'vue-router'
import { useI18n } from 'vue-i18n'
import elementEn from 'element-plus/es/locale/lang/en'
import elementZhCn from 'element-plus/es/locale/lang/zh-cn'
import { setLocale, type Locale } from './i18n'

const route = useRoute()
const { t, locale } = useI18n()

const menu = computed(() => [
  { path: '/labs', label: t('menu.labs') },
  { path: '/topology', label: t('menu.topology') },
  { path: '/programs', label: t('menu.programs') },
  { path: '/packages', label: t('menu.packages') },
  { path: '/traffic', label: t('menu.traffic') },
  { path: '/faults', label: t('menu.faults') },
  { path: '/captures', label: t('menu.captures') },
  { path: '/operations', label: t('menu.operations') },
])

const elementLocale = computed(() => (locale.value === 'zh-CN' ? elementZhCn : elementEn))
</script>

<template>
  <el-config-provider :locale="elementLocale">
    <el-container class="app">
      <el-aside width="200px" class="sidebar">
        <div class="brand">DCNetLab</div>
        <!-- Highlight by first path segment so nested routes (e.g. the
             capture viewer) keep their section active. -->
        <el-menu :default-active="'/' + route.path.split('/')[1]" router>
          <el-menu-item v-for="m in menu" :key="m.path" :index="m.path">
            {{ m.label }}
          </el-menu-item>
        </el-menu>
        <div class="locale">
          <el-select
            :model-value="locale"
            size="small"
            @change="setLocale($event as Locale)"
          >
            <el-option label="简体中文" value="zh-CN" />
            <el-option label="English" value="en" />
          </el-select>
        </div>
      </el-aside>
      <el-main>
        <router-view />
      </el-main>
    </el-container>
  </el-config-provider>
</template>

<style>
html, body, #app { height: 100%; margin: 0; }
.app { height: 100%; }
.sidebar {
  border-right: 1px solid var(--el-border-color);
  display: flex;
  flex-direction: column;
}
.sidebar .el-menu { flex: 1; border-right: none; }
.brand {
  font-size: 18px;
  font-weight: 700;
  padding: 16px;
}
.locale { padding: 12px 16px; }
</style>
