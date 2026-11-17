import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

// 构建产物输出到 Go 的内嵌目录，服务端和本地客户端共用同一份前端。
export default defineConfig({
  plugins: [vue()],
  base: '/',
  build: {
    outDir: '../internal/webui/dist',
    emptyOutDir: true,
    assetsDir: 'assets',
    sourcemap: false,
    chunkSizeWarningLimit: 800,
  },
  server: {
    // 开发时把本地客户端的接口代理过来：先运行 veilfin client -listen 127.0.0.1:8096
    proxy: {
      '/api': 'http://127.0.0.1:8096',
      '/b': 'http://127.0.0.1:8096',
      '/config.json': 'http://127.0.0.1:8096',
    },
  },
})
