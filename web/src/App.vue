<script setup lang="ts">
import zhCN from 'antdv-next/locale/zh_CN'
import { theme } from './styles/theme'
</script>

<template>
  <a-config-provider :locale="zhCN" :theme="theme">
    <a-app>
      <router-view />
    </a-app>
  </a-config-provider>
</template>
