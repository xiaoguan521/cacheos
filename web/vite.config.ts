import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

// 构建产物输出到 web/dist，由 sycache-console 通过 go:embed 内嵌。
export default defineConfig({
  plugins: [vue()],
  // 内网部署，整包加载；antdv-next 全量注册约 1.9 MB
  build: { outDir: 'dist', emptyOutDir: true, chunkSizeWarningLimit: 2500 },
  server: {
    proxy: { '/api': 'http://127.0.0.1:8443' },
  },
})
