<script setup lang="ts">
defineProps<{ title: string; hint?: string }>()
</script>

<template>
  <div class="auth">
    <aside class="side">
      <div class="lanes" aria-hidden="true">
        <span v-for="i in 7" :key="i" :style="{ '--d': i }" />
      </div>
      <div class="pitch">
        <div class="product">EdgeTraffic OS</div>
        <p>改配置、校验、下发、生效、回滚，<br />在一个控制台里完成。</p>
      </div>
    </aside>
    <main class="main">
      <div class="box">
        <h1>{{ title }}</h1>
        <p v-if="hint" class="hint">{{ hint }}</p>
        <slot />
      </div>
    </main>
  </div>
</template>

<style scoped>
.auth {
  display: grid;
  grid-template-columns: minmax(320px, 42%) 1fr;
  min-height: 100vh;
  background: #fff;
}
.side {
  position: relative;
  overflow: hidden;
  background: #16323a;
  color: #fff;
  display: flex;
  align-items: flex-end;
  padding: 48px;
}
/* 七条流量车道，长度不同、缓慢流动：页面唯一的动效 */
.lanes {
  position: absolute;
  inset: 64px 48px auto 48px;
  display: grid;
  gap: 18px;
}
.lanes span {
  height: 6px;
  border-radius: 3px;
  background: linear-gradient(90deg, rgba(14, 124, 134, 0) 0%, #0e7c86 40%, #2f9e5b 100%);
  width: calc(40% + var(--d) * 8%);
  opacity: calc(0.35 + var(--d) * 0.08);
  animation: flow 6s ease-in-out infinite alternate;
  animation-delay: calc(var(--d) * -0.7s);
}
.lanes span:nth-child(5) {
  background: linear-gradient(90deg, rgba(217, 162, 27, 0) 0%, #d9a21b 100%);
}
@keyframes flow {
  from {
    transform: translateX(-6%);
  }
  to {
    transform: translateX(6%);
  }
}
.product {
  font-size: 30px;
  font-weight: 650;
  letter-spacing: 0.02em;
}
.pitch p {
  margin: 12px 0 0;
  font-size: 16px;
  line-height: 1.7;
  color: #b9c9cc;
}
.main {
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 32px;
}
.box {
  width: 100%;
  max-width: 380px;
}
h1 {
  margin: 0 0 6px;
  font-size: 24px;
  color: #16323a;
}
.hint {
  margin: 0 0 24px;
  color: #6b7c80;
  line-height: 1.6;
}
@media (max-width: 760px) {
  .auth {
    grid-template-columns: 1fr;
  }
  .side {
    display: none;
  }
}
</style>
