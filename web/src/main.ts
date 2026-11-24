import { createApp } from 'vue'
import App from './App.vue'
import { i18n, initialLanguage, setLanguage } from './i18n'
import 'vue-sonner/style.css'
import './styles/globals.css'

// Le jeton n'est saisi qu'après le premier rendu : la langue du navigateur sert jusqu'au premier /api/status.
setLanguage(initialLanguage(navigator.languages))
createApp(App).use(i18n).mount('#app')
