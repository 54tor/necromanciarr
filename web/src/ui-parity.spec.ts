import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { Component } from 'vue'
import { API_ACTIONS, UI_TRIGGERS } from '@/api/actions'
import { installFakeApi, resetAppState } from '@/test/fakeApi'
import {
  busyStatus,
  conflictProposal,
  dedupeDetails,
  doctorReport,
  maintenanceProposal,
  proposal,
  repairDetails,
} from '@/test/fixtures'
import DedupeView from '@/views/DedupeView.vue'
import LinksView from '@/views/LinksView.vue'
import OnboardingView from '@/views/OnboardingView.vue'
import ProposalsView from '@/views/ProposalsView.vue'
import StagesView from '@/views/StagesView.vue'

vi.mock('vue-sonner', () => ({ toast: { error: vi.fn(), success: vi.fn() } }))

const VIEWS: Record<string, Component> = {
  OnboardingView,
  ProposalsView,
  DedupeView,
  LinksView,
  StagesView,
}

async function actionsRenderedBy(view: Component): Promise<Set<string>> {
  installFakeApi({
    'GET /api/doctor': doctorReport,
    'GET /api/status': busyStatus,
    'GET /api/jobs': [],
    'GET /api/proposals': (call) => {
      const kind = call.query.get('kind')
      if (kind === 'dedupe') return [maintenanceProposal(1, dedupeDetails)]
      if (kind === 'repair') return [{ ...maintenanceProposal(2, repairDetails), kind: 'repair' }]
      return [proposal(), conflictProposal()]
    },
  })
  const wrapper = mount(view)
  await flushPromises()
  const found = new Set(
    wrapper.findAll('[data-action]').map((element) => element.attributes('data-action') ?? ''),
  )
  wrapper.unmount()
  return found
}

describe('UiParity', () => {
  beforeEach(resetAppState)
  afterEach(() => vi.unstubAllGlobals())

  it('UiParity_everyApiActionHasAButton', async () => {
    expect(API_ACTIONS.length).toBeGreaterThan(0)
    const rendered: Record<string, Set<string>> = {}
    for (const [name, view] of Object.entries(VIEWS)) rendered[name] = await actionsRenderedBy(view)

    for (const { action } of API_ACTIONS) {
      const declaredView = UI_TRIGGERS[action] ?? ''
      expect(declaredView, `${action} has no declared UI trigger`).toBeDefined()
      expect(
        rendered[declaredView]?.has(action),
        `${action} has no button in ${declaredView}`,
      ).toBe(true)
    }
  })

  it('UiParity_theTriggerMapDeclaresNoUnknownAction', () => {
    const knownActions = new Set(API_ACTIONS.map((entry) => entry.action))

    const unknownActions = Object.keys(UI_TRIGGERS).filter((action) => !knownActions.has(action))

    expect(unknownActions).toEqual([])
    for (const view of Object.values(UI_TRIGGERS)) expect(Object.keys(VIEWS)).toContain(view)
  })

  it('UiParity_theActionTableMatchesTheDocumentedRoutes', () => {
    const gotRoutes = API_ACTIONS.map((entry) => `${entry.method} ${entry.path}`)

    expect(gotRoutes).toContain('GET /api/doctor')
    expect(gotRoutes).toContain('POST /api/stages/{stage}/run')
    expect(new Set(API_ACTIONS.map((entry) => entry.action)).size).toBe(API_ACTIONS.length)
  })
})
