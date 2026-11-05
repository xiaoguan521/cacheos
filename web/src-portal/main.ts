import { createApp } from 'vue'
import { createPinia } from 'pinia'
import Antdv from 'antdv-next'
import App from './App.vue'
import router from './router'
import '../src/styles/base.css'

createApp(App).use(createPinia()).use(router).use(Antdv).mount('#app')
