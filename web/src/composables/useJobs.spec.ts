import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent } from 'vue'
import { setLanguage } from '@/i18n'
import { installFakeApi, resetAppState } from '@/test/fakeApi'
import { job } from '@/test/fixtures'
import { jobs, useJobs } from './useJobs'

const toastSuccess = vi.fn()
vi.mock('vue-sonner', () => ({
  toast: { error: vi.fn(), success: (message: string) => toastSuccess(message) },
}))

async function finishRunningJob(): Promise<void> {
  installFakeApi({ 'GET /api/jobs': () => [job({ id: 1, state: 'done' })] })
  jobs.value = [job({ id: 1, state: 'running' })]
  let refresh: () => Promise<void> = async () => undefined
  const host = defineComponent({
    setup() {
      refresh = useJobs().refresh
      return () => null
    },
  })
  const wrapper = mount(host)
  await refresh()
  await flushPromises()
  wrapper.unmount()
}

describe('useJobs finished toast', () => {
  beforeEach(() => {
    resetAppState()
    toastSuccess.mockClear()
  })
  afterEach(() => vi.unstubAllGlobals())

  it('announces a finished job in French', async () => {
    await finishRunningJob()

    expect(toastSuccess).toHaveBeenCalledWith('Tâche terminée.')
  })

  it('announces a finished job in English', async () => {
    setLanguage('en')

    await finishRunningJob()

    expect(toastSuccess).toHaveBeenCalledWith('Task done.')
  })
})
