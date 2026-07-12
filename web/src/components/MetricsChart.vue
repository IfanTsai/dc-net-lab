<script setup lang="ts">
// MetricsChart renders one time series block of the server metrics
// history as an ECharts line chart. Series colors come from the
// validated categorical palette (assigned by slot order, never
// cycled); collector gaps (missed sweeps) break the line instead of
// being interpolated.
import { onBeforeUnmount, onMounted, ref, watch } from 'vue'
import * as echarts from 'echarts/core'
import { LineChart } from 'echarts/charts'
import { GridComponent, LegendComponent, TooltipComponent } from 'echarts/components'
import { CanvasRenderer } from 'echarts/renderers'
import type { EChartsCoreOption } from 'echarts/core'

echarts.use([LineChart, GridComponent, LegendComponent, TooltipComponent, CanvasRenderer])

export interface ChartSeries {
  name: string
  // [unix ms, value]; points are sorted by time.
  points: [number, number][]
  dashed?: boolean
}

const props = defineProps<{
  title: string
  series: ChartSeries[]
  // unit drives axis and tooltip formatting.
  unit: 'percent' | 'bytes' | 'bytesRate' | 'count'
}>()

// Categorical slots 1-3 of the validated palette, assigned in order.
const palette = ['#2a78d6', '#1baf7a', '#eda100']

const el = ref<HTMLDivElement | null>(null)
let chart: echarts.ECharts | null = null
let resizer: ResizeObserver | null = null

function fmtValue(v: number): string {
  if (props.unit === 'percent') return `${v.toFixed(1)}%`
  if (props.unit === 'count') return v >= 1000 ? `${(v / 1000).toFixed(1)}k` : v.toFixed(v >= 10 ? 0 : 1)

  const suffix = props.unit === 'bytesRate' ? '/s' : ''
  let n = v
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB']
  let i = 0
  while (n >= 1024 && i < units.length - 1) {
    n /= 1024
    i++
  }
  return `${n >= 100 || i === 0 ? Math.round(n) : n.toFixed(1)} ${units[i]}${suffix}`
}

// withGaps inserts a null between samples more than one missed sweep
// apart so the line breaks over collector gaps.
const gapMs = 45_000

function withGaps(points: [number, number][]): [number, number | null][] {
  const out: [number, number | null][] = []
  for (let i = 0; i < points.length; i++) {
    if (i > 0 && points[i][0] - points[i - 1][0] > gapMs) {
      out.push([points[i - 1][0] + 1, null])
    }
    out.push(points[i])
  }
  return out
}

function option(): EChartsCoreOption {
  return {
    animation: false,
    grid: { left: 44, right: 8, top: 24, bottom: 34 },
    legend:
      props.series.length > 1
        ? {
            bottom: 0,
            itemWidth: 12,
            itemHeight: 2,
            icon: 'rect',
            textStyle: { color: '#52514e', fontSize: 11 },
          }
        : undefined,
    tooltip: {
      trigger: 'axis',
      axisPointer: { type: 'line' },
      valueFormatter: (v: unknown) => (typeof v === 'number' ? fmtValue(v) : '—'),
      textStyle: { fontSize: 11 },
    },
    xAxis: {
      type: 'time',
      axisLine: { lineStyle: { color: '#d4d4d0' } },
      axisLabel: { color: '#7a7974', fontSize: 10, hideOverlap: true },
      splitLine: { show: false },
    },
    yAxis: {
      type: 'value',
      max: props.unit === 'percent' ? 100 : undefined,
      axisLabel: {
        color: '#7a7974',
        fontSize: 10,
        formatter: (v: number) => fmtValue(v),
      },
      splitLine: { lineStyle: { color: '#eeeeea' } },
    },
    series: props.series.map((s, i) => ({
      name: s.name,
      type: 'line',
      data: withGaps(s.points),
      showSymbol: false,
      connectNulls: false,
      lineStyle: { width: 2, type: s.dashed ? 'dashed' : 'solid' },
      itemStyle: { color: palette[i % palette.length] },
      emphasis: { disabled: true },
    })),
  }
}

function render() {
  if (!chart) return
  chart.setOption(option(), { notMerge: true })
}

onMounted(() => {
  if (!el.value) return
  chart = echarts.init(el.value)
  render()
  resizer = new ResizeObserver(() => chart?.resize())
  resizer.observe(el.value)
})

onBeforeUnmount(() => {
  resizer?.disconnect()
  chart?.dispose()
  chart = null
})

watch(() => props.series, render, { deep: true })
</script>

<template>
  <div class="chart-block">
    <div class="chart-title">{{ title }}</div>
    <div ref="el" class="chart-canvas" />
  </div>
</template>

<style scoped>
.chart-block { margin-bottom: 12px; }
.chart-title { font-size: 12px; color: var(--el-text-color-secondary); margin-bottom: 2px; }
.chart-canvas { width: 100%; height: 180px; }
</style>
