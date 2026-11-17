import { createApp } from 'vue'
import App from './App.vue'
import router from './router.js'
import { init, state } from './store.js'
import './style.css'
import './motion.css'

init()
  .catch((e) => {
    state.fatal = e?.message || String(e)
  })
  .finally(() => {
    createApp(App).use(router).mount('#app')
  })
