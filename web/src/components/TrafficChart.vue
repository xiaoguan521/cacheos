<script setup lang="ts">
import { computed } from 'vue'
import VChart from 'vue-echarts'
import { use } from 'echarts/core'
import { LineChart, BarChart } from 'echarts/charts'
import { GridComponent, TooltipComponent, LegendComponent } from 'echarts/components'
import { CanvasRenderer } from 'echarts/renderers'
import dayjs from 'dayjs'
import { palette } from '../styles/theme'

use([LineChart, BarChart, GridComponent, TooltipComponent, LegendComponent, CanvasRenderer])

const props = defineProps<{ series: { minute: number; requests: number; s5xx: number; s4xx: number; p95_ms: number }[] }>()

const option = computed(() => ({
  animation: false,
  grid: { left: 48, right: 52, top: 56, bottom: 28 },
  tooltip: { trigger: 'axis' },
  legend: { top: 0, left: 0, itemWidth: 12, itemHeight: 8, textStyle: { color: palette.muted } },
  xAxis: {
    type: 'category',
    data: props.series.map((p) => dayjs.unix(p.minute).format('HH:mm')),
    axisLine: { lineStyle: { color: '#C9D3D2' } },
    axisLabel: { color: palette.muted },
  },
  yAxis: [
    { type: 'value', name: '请求/分钟', nameTextStyle: { color: palette.muted }, splitLine: { lineStyle: { color: '#EEF2F1' } }, axisLabel: { color: palette.muted } },
    { type: 'value', name: 'P95 毫秒', nameTextStyle: { color: palette.muted }, splitLine: { show: false }, axisLabel: { color: palette.muted } },
  ],
  series: [
    { name: '请求', type: 'bar', stack: 'req', barMaxWidth: 14, itemStyle: { color: palette.signal }, data: props.series.map((p) => p.requests - p.s5xx) },
    { name: '5xx', type: 'bar', stack: 'req', barMaxWidth: 14, itemStyle: { color: palette.fault }, data: props.series.map((p) => p.s5xx) },
    { name: 'P95 延迟', type: 'line', yAxisIndex: 1, smooth: false, symbol: 'circle', symbolSize: 4, connectNulls: false, lineStyle: { color: palette.lane, width: 2 }, data: props.series.map((p) => (p.requests ? Math.round(p.p95_ms) : null)) },
  ],
}))
</script>

<template>
  <v-chart :option="option" autoresize style="height: 240px" />
</template>
