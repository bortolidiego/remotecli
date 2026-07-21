import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import { VitePWA } from 'vite-plugin-pwa'

export default defineConfig({
  server: {
    host: '127.0.0.1',
    port: 5173,
    proxy: {
      '/api': {
        target: 'http://127.0.0.1:24109',
        changeOrigin: true,
      },
      '/health': {
        target: 'http://127.0.0.1:24109',
        changeOrigin: true,
      },
    },
  },
  build: {
    outDir: '../../internal/web/dist',
    emptyOutDir: true,
    sourcemap: true,
  },
  plugins: [
    react(),
    VitePWA({
      // Força o SW a pegar versão nova assim que o servidor muda (evita PWA antiga no iPhone)
      registerType: 'autoUpdate',
      manifest: {
        name: 'Remote CliControl',
        short_name: 'RemoteCLI',
        description: 'Controle remoto de CLIs no Mac',
        theme_color: '#141413',
        background_color: '#141413',
        display: 'standalone',
        start_url: '/',
        icons: [
          { src: '/icon.svg', sizes: 'any', type: 'image/svg+xml', purpose: 'any maskable' },
        ],
      },
      workbox: {
        globPatterns: ['**/*.{js,css,html,ico,png,svg,webmanifest}'],
        // Não cachear index/API para sempre puxar o build novo do Mac
        navigateFallback: '/index.html',
        runtimeCaching: [
          {
            urlPattern: ({ request }) => request.mode === 'navigate',
            handler: 'NetworkFirst',
            options: {
              cacheName: 'pages',
              networkTimeoutSeconds: 3,
            },
          },
          {
            urlPattern: /\/assets\/.*/i,
            handler: 'NetworkFirst',
            options: {
              cacheName: 'assets',
              networkTimeoutSeconds: 3,
            },
          },
        ],
      },
      devOptions: {
        enabled: false,
      },
    }),
  ],
})
