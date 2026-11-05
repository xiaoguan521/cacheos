<script setup lang="ts">
import { computed } from 'vue'

const props = defineProps<{ diff: string }>()

const lines = computed(() =>
  props.diff
    .split('\n')
    .filter((l) => !l.startsWith('---') && !l.startsWith('+++'))
    .map((l) => ({ t: l.startsWith('@@') ? 'hunk' : l[0] === '+' ? 'add' : l[0] === '-' ? 'del' : 'ctx', l })),
)
</script>

<template>
  <pre class="diff code"><span v-for="(x, i) in lines" :key="i" :class="x.t">{{ x.l }}
</span></pre>
</template>

<style scoped>
.diff {
  margin: 0;
  padding: 8px 0;
  max-height: 420px;
  overflow: auto;
  background: #fbfcfc;
  border: 1px solid #e1e7e6;
  border-radius: 6px;
  line-height: 1.6;
}
.diff span {
  display: block;
  padding: 0 14px;
  white-space: pre;
}
.add {
  background: #e6f3ec;
  color: #1f6f43;
}
.del {
  background: #f8e3e1;
  color: #a3332d;
}
.hunk {
  color: #0b5f66;
  background: #eef6f6;
}
.ctx {
  color: #4a5a5e;
}
</style>
