import { computed, ref } from 'vue'
import { api } from '@/api/endpoints'
import type { ConfigSnapshot } from '@/api/types'
import { useApi } from '@/composables/useApi'

/** useConfig garde la configuration relue : chaque écriture rend la vue entière, qui remplace la précédente. */
export function useConfig() {
  const { call, pending } = useApi()
  const snapshot = ref<ConfigSnapshot>()
  const loading = ref(false)
  const loadFailed = ref(false)
  const restartRequired = computed(() => snapshot.value?.restartRequired === true)

  async function load(): Promise<void> {
    loading.value = true
    const loaded = await call(() => api.getConfig())
    loading.value = false
    loadFailed.value = loaded === undefined
    if (loaded) snapshot.value = loaded
  }

  async function write(task: () => Promise<ConfigSnapshot>): Promise<boolean> {
    const written = await call(task)
    if (written) snapshot.value = written
    return written !== undefined
  }

  return {
    snapshot,
    loading,
    loadFailed,
    pending,
    restartRequired,
    load,
    saveSetting: (key: string, value: unknown) => write(() => api.putConfigSetting(key, value)),
    unsetSetting: (key: string) => write(() => api.unsetConfigSetting(key)),
    saveItem: (list: string, itemId: string, element: Record<string, unknown>) =>
      write(() => api.putConfigItem(list, itemId, element)),
    deleteItem: (list: string, itemId: string) => write(() => api.deleteConfigItem(list, itemId)),
    unsetItem: (list: string, itemId: string) => write(() => api.unsetConfigItem(list, itemId)),
  }
}
