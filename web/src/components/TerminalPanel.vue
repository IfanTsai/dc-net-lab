<script setup lang="ts">
// Floating terminal window: one closable tab per node, each an
// xterm.js terminal bridged over WebSocket to a shell inside the
// node's container (vtysh on network devices, bash on servers).
// Rendered as a fixed overlay so opening it never resizes the
// topology canvas; draggable by its header and resizable from the
// bottom-right corner.
import { computed, nextTick, onBeforeUnmount, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import '@xterm/xterm/css/xterm.css'
import type { Node } from '../types/models'

const { t } = useI18n()

interface Tab {
  nodeId: string
  title: string
}

interface Session {
  term: Terminal
  fit: FitAddon
  ws: WebSocket
  observer: ResizeObserver
}

const tabs = ref<Tab[]>([])
const active = ref('')
// Sessions hold xterm/WebSocket handles; kept out of tabs so Vue
// never proxies them (xterm breaks under a reactive proxy).
const sessions = new Map<string, Session>()
const hosts = reactive<Record<string, HTMLElement | null>>({})

// Window geometry: centred by default, user-draggable.
const pos = ref({ x: 0, y: 0 })
const placed = ref(false)
const win = ref<HTMLElement | null>(null)

// Minimise docks the window as a pill in the bottom-right corner;
// maximise fills the viewport, remembering geometry for restore.
const minimized = ref(false)
const maximized = ref(false)
let savedRect: { x: number; y: number; w: number; h: number } | null = null

// Per-tab "output arrived while minimised" markers, surfaced as dots
// on the dock pill so a hidden terminal is never silently active.
const activity = reactive<Record<string, boolean>>({})
const hasActivity = computed(() => tabs.value.some((tab) => activity[tab.nodeId]))

const encoder = new TextEncoder()

function wsURL(labId: string, nodeId: string): string {
  const proto = location.protocol === 'https:' ? 'wss' : 'ws'

  return `${proto}://${location.host}/ws/v1/labs/${labId}/nodes/${nodeId}/terminal`
}

// open connects a terminal to one node, or focuses its existing tab.
async function open(labId: string, node: Node) {
  if (sessions.has(node.meta.id)) {
    await restore(node.meta.id)
    return
  }

  tabs.value.push({ nodeId: node.meta.id, title: node.meta.name })
  active.value = node.meta.id
  minimized.value = false
  await nextTick()

  if (!placed.value && win.value) {
    // First open: settle at the centre of the viewport.
    const r = win.value.getBoundingClientRect()
    pos.value = {
      x: Math.max((window.innerWidth - r.width) / 2, 0),
      y: Math.max((window.innerHeight - r.height) / 2, 0),
    }
    placed.value = true
  }

  const host = hosts[node.meta.id]
  if (!host) return

  const term = new Terminal({
    fontSize: 13,
    fontFamily: 'Menlo, Consolas, "DejaVu Sans Mono", monospace',
    cursorBlink: true,
    theme: { background: '#1e1e1e' },
  })
  const fit = new FitAddon()
  term.loadAddon(fit)
  term.open(host)
  fit.fit()

  const ws = new WebSocket(wsURL(labId, node.meta.id))
  ws.binaryType = 'arraybuffer'
  ws.onopen = () => {
    ws.send(JSON.stringify({ type: 'resize', cols: term.cols, rows: term.rows }))
  }
  ws.onmessage = (ev) => {
    if (ev.data instanceof ArrayBuffer) {
      term.write(new Uint8Array(ev.data))
      if (minimized.value) activity[node.meta.id] = true
    } else {
      // Text frames are JSON control messages; today only "error".
      try {
        const msg = JSON.parse(ev.data as string)
        if (msg.type === 'error') term.writeln(`\x1b[31m${msg.message}\x1b[0m`)
      } catch {
        /* ignore malformed frames */
      }
    }
  }
  ws.onclose = () => {
    term.writeln(`\r\n\x1b[90m${t('terminal.disconnected')}\x1b[0m`)
  }
  term.onData((d) => {
    if (ws.readyState === WebSocket.OPEN) ws.send(encoder.encode(d))
  })
  term.onResize(({ cols, rows }) => {
    if (ws.readyState === WebSocket.OPEN) ws.send(JSON.stringify({ type: 'resize', cols, rows }))
  })

  // Refit when the window is resized; xterm needs an explicit fit
  // call, it does not track its container.
  const observer = new ResizeObserver(() => fit.fit())
  observer.observe(host)

  sessions.set(node.meta.id, { term, fit, ws, observer })
  term.focus()
}

function close(nodeId: string) {
  const s = sessions.get(nodeId)
  if (s) {
    s.observer.disconnect()
    s.ws.close()
    s.term.dispose()
    sessions.delete(nodeId)
  }

  const i = tabs.value.findIndex((tab) => tab.nodeId === nodeId)
  if (i >= 0) tabs.value.splice(i, 1)
  if (active.value === nodeId) active.value = tabs.value[Math.max(0, i - 1)]?.nodeId ?? ''
  delete hosts[nodeId]
  if (tabs.value.length === 0) placed.value = false
}

function closeAll() {
  for (const nodeId of [...sessions.keys()]) close(nodeId)
}

// Refit the newly shown terminal: hidden tabs cannot measure.
async function onTabChange(nodeId: string | number) {
  await nextTick()
  const s = sessions.get(String(nodeId))
  s?.fit.fit()
  s?.term.focus()
  delete activity[String(nodeId)]
}

function minimize() {
  minimized.value = true
}

// restore brings the window back from the dock pill, optionally
// jumping straight to one tab.
async function restore(nodeId?: string) {
  minimized.value = false
  if (nodeId) active.value = nodeId
  await nextTick()

  const s = sessions.get(active.value)
  s?.fit.fit()
  s?.term.focus()
  delete activity[active.value]
}

function toggleMaximize() {
  if (!win.value) return

  if (maximized.value) {
    maximized.value = false
    if (savedRect) {
      pos.value = { x: savedRect.x, y: savedRect.y }
      placed.value = true
      win.value.style.width = `${savedRect.w}px`
      win.value.style.height = `${savedRect.h}px`
    }
  } else {
    const r = win.value.getBoundingClientRect()
    savedRect = { x: r.x, y: r.y, w: r.width, h: r.height }
    // Clear any user-resized inline size so the maximized class rules.
    win.value.style.width = ''
    win.value.style.height = ''
    minimized.value = false
    maximized.value = true
  }

  focusActive()
}

function focusActive() {
  const s = sessions.get(active.value)
  s?.term.focus()
}

// Drag the window by its header; position is clamped so the header
// always stays reachable.
function onDragStart(ev: PointerEvent) {
  if (maximized.value) return

  const start = { x: ev.clientX - pos.value.x, y: ev.clientY - pos.value.y }

  const onMove = (e: PointerEvent) => {
    pos.value = {
      x: Math.min(Math.max(e.clientX - start.x, 0), window.innerWidth - 120),
      y: Math.min(Math.max(e.clientY - start.y, 0), window.innerHeight - 40),
    }
  }
  const onUp = () => {
    window.removeEventListener('pointermove', onMove)
    window.removeEventListener('pointerup', onUp)
  }
  window.addEventListener('pointermove', onMove)
  window.addEventListener('pointerup', onUp)
}

defineExpose({ open, closeAll })
onBeforeUnmount(closeAll)
</script>

<template>
  <Transition name="termwin">
    <div
      v-show="tabs.length && !minimized"
      ref="win"
      class="terminal-window"
      :class="{ maximized }"
      :style="!maximized && placed ? { left: `${pos.x}px`, top: `${pos.y}px`, transform: 'none' } : undefined"
    >
      <div class="titlebar" @pointerdown.prevent="onDragStart" @dblclick="toggleMaximize">
        <span class="title">{{ t('terminal.title') }}</span>
        <span class="buttons" @pointerdown.stop @dblclick.stop>
          <span class="win-btn" @click="minimize">─</span>
          <span class="win-btn" @click="toggleMaximize">{{ maximized ? '❐' : '☐' }}</span>
          <span class="win-btn close-btn" @click="closeAll">✕</span>
        </span>
      </div>
      <el-tabs
        v-model="active"
        type="card"
        closable
        @tab-remove="close(String($event))"
        @tab-change="onTabChange"
      >
        <el-tab-pane v-for="tab in tabs" :key="tab.nodeId" :label="tab.title" :name="tab.nodeId">
          <div :ref="(el) => (hosts[tab.nodeId] = el as HTMLElement | null)" class="term-host" />
        </el-tab-pane>
      </el-tabs>
    </div>
  </Transition>

  <!-- Dock pill: where the window lives while minimised. Hovering
       reveals the session list for jumping straight to one tab. -->
  <Transition name="termwin">
    <div v-if="tabs.length && minimized" class="terminal-pill">
      <div class="pill-list">
        <div v-for="tab in tabs" :key="tab.nodeId" class="pill-item" @click="restore(tab.nodeId)">
          <span class="dot" :class="{ on: activity[tab.nodeId] }" />
          <span class="pill-item-name">{{ tab.title }}</span>
        </div>
      </div>
      <div class="pill-main" @click="restore()">
        <span class="pill-icon">⌨</span>
        {{ t('terminal.title') }} · {{ tabs.length }}
        <span v-if="hasActivity" class="dot on" />
      </div>
    </div>
  </Transition>
</template>

<style scoped>
.terminal-window {
  position: fixed;
  /* Pre-placement anchor: centred (pos takes over after measuring). */
  left: 50%;
  top: 50%;
  transform: translate(-50%, -50%);
  width: 920px;
  height: 540px;
  min-width: 420px;
  min-height: 240px;
  z-index: 2100; /* above the detail drawers */
  display: flex;
  flex-direction: column;
  border: 1px solid var(--el-border-color);
  border-radius: 6px;
  background: #1e1e1e;
  box-shadow: var(--el-box-shadow);
  resize: both;
  overflow: hidden;
}
.titlebar {
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding: 6px 10px;
  background: #2d2d2d;
  color: #d4d4d4;
  font-size: 13px;
  cursor: move;
  user-select: none;
}
.terminal-window.maximized {
  left: 12px;
  top: 12px;
  transform: none;
  width: calc(100vw - 24px);
  height: calc(100vh - 24px);
  resize: none;
}
.termwin-enter-active, .termwin-leave-active { transition: opacity 0.15s ease; }
.termwin-enter-from, .termwin-leave-to { opacity: 0; }
.terminal-pill {
  position: fixed;
  right: 24px;
  bottom: 24px;
  z-index: 2100;
}
.pill-main {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 8px 16px;
  border-radius: 999px;
  border: 1px solid #3c3c3c;
  background: #2d2d2d;
  color: #d4d4d4;
  font-size: 13px;
  cursor: pointer;
  user-select: none;
  box-shadow: var(--el-box-shadow);
}
.pill-main:hover { background: #383838; color: #fff; }
.pill-icon { font-size: 15px; line-height: 1; }
.dot {
  width: 7px;
  height: 7px;
  border-radius: 50%;
  background: #565656;
  flex: none;
}
.dot.on { background: #4ec9b0; }
/* Session list: revealed above the pill on hover. */
.pill-list {
  position: absolute;
  right: 0;
  bottom: calc(100% + 6px);
  min-width: 180px;
  padding: 4px;
  border: 1px solid #3c3c3c;
  border-radius: 6px;
  background: #2d2d2d;
  box-shadow: var(--el-box-shadow);
  opacity: 0;
  transform: translateY(4px);
  pointer-events: none;
  transition: opacity 0.15s ease, transform 0.15s ease;
}
.terminal-pill:hover .pill-list {
  opacity: 1;
  transform: none;
  pointer-events: auto;
}
.pill-item {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 6px 10px;
  border-radius: 4px;
  color: #d4d4d4;
  font-size: 12px;
  cursor: pointer;
  white-space: nowrap;
}
.pill-item:hover { background: #383838; color: #fff; }
.buttons { display: inline-flex; gap: 2px; }
.win-btn {
  cursor: pointer;
  padding: 0 6px;
  border-radius: 3px;
  line-height: 20px;
}
.win-btn:hover { color: #fff; background: #454545; }
.close-btn:hover { background: #c42b1c; }
/* Dark theme for the tab strip: Element Plus defaults are dark text
   on light background, unreadable on the terminal chrome. */
.terminal-window :deep(.el-tabs) { flex: 1; min-height: 0; display: flex; flex-direction: column; }
.terminal-window :deep(.el-tabs__header) {
  margin: 0;
  background: #252526;
  border-bottom: 1px solid #3c3c3c;
}
.terminal-window :deep(.el-tabs__nav) { border: none !important; }
.terminal-window :deep(.el-tabs__item) {
  color: #9d9d9d;
  border-color: #3c3c3c !important;
  border-bottom: none;
}
.terminal-window :deep(.el-tabs__item:hover) { color: #d4d4d4; }
.terminal-window :deep(.el-tabs__item.is-active) {
  color: #ffffff;
  background: #1e1e1e;
}
.terminal-window :deep(.el-tabs__item .is-icon-close:hover) {
  background: #c42b1c;
  color: #fff;
}
.terminal-window :deep(.el-tabs__content) { flex: 1; min-height: 0; }
.terminal-window :deep(.el-tab-pane) { height: 100%; }
.term-host { height: 100%; padding: 4px 0 4px 8px; box-sizing: border-box; }
</style>
