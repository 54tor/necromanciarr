import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { Component } from 'vue'
import { API_ACTIONS, ROW_SCOPED_ACTIONS, UI_TRIGGERS } from '@/api/actions'
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
import SourcesView from '@/views/SourcesView.vue'
import StagesView from '@/views/StagesView.vue'
import VolumesView from '@/views/VolumesView.vue'

vi.mock('vue-sonner', () => ({ toast: { error: vi.fn(), success: vi.fn() } }))

const VIEWS: Record<string, Component> = {
  OnboardingView,
  ProposalsView,
  DedupeView,
  LinksView,
  StagesView,
  SourcesView,
  VolumesView,
}

async function actionsRenderedBy(view: Component): Promise<Set<string>> {
  installFakeApi({
    'POST /api/doctor/run': doctorReport,
    'GET /api/status': busyStatus,
    'GET /api/jobs': [],
    'GET /api/sources': [
      {
        order: 2,
        source: 'torznab:a',
        type: 'torznab',
        tracker: '',
        state: 'ok',
        queries: { used: 0, cap: 200 },
        downloads: { used: 0, cap: 50 },
        last_error: '',
        caps_cached: false,
      },
    ],
    'GET /api/volumes': [
      {
        volume: 'seedbox',
        type: 'sftp',
        address: 'seedbox.example.com:22',
        state: 'ok',
        capabilities: null,
        probed_at: null,
      },
    ],
    'GET /api/proposals': (call) => {
      const kind = call.query.get('kind')
      if (kind === 'dedupe') return [maintenanceProposal(1, dedupeDetails)]
      if (kind === 'repair') return [{ ...maintenanceProposal(2, repairDetails), kind: 'repair' }]
      return [proposal(), conflictProposal(), proposal({ id: 3, kind: 'partial_annex' })]
    },
  })
  const wrapper = mount(view, { attachTo: document.body })
  await flushPromises()
  const found = new Set<string>()
  // Un onglet inactif n'est pas monté : chaque onglet est ouvert à son tour pour que ses boutons soient comptés.
  const tabCount = wrapper.findAll('[role="tab"]').length
  for (let tabPosition = 0; tabPosition < Math.max(tabCount, 1); tabPosition++) {
    if (tabCount > 0) {
      await wrapper.findAll('[role="tab"]')[tabPosition]!.trigger('mousedown', { button: 0 })
      await flushPromises()
    }
    for (const element of wrapper.findAll('[data-action]')) {
      const action = element.attributes('data-action') ?? ''
      found.add(action)
      if (element.attributes('data-scope') === 'row') found.add(`${action}#row`)
    }
    for (const element of wrapper.findAll('[data-scope]'))
      seenScopes.add(element.attributes('data-scope') ?? '')
  }
  wrapper.unmount()
  return found
}

/** `toolbar` : action de page, posée dans PageToolbar. */
const ALLOWED_SCOPES = ['row', 'bulk', 'toolbar']
const seenScopes = new Set<string>()

describe('UiParity', () => {
  beforeEach(resetAppState)
  afterEach(() => vi.unstubAllGlobals())

  it('UiParity_everyApiActionHasAButton', async () => {
    expect(API_ACTIONS.length).toBeGreaterThan(0)
    const rendered: Record<string, Set<string>> = {}
    for (const [name, view] of Object.entries(VIEWS)) rendered[name] = await actionsRenderedBy(view)

    for (const action of API_ACTIONS) {
      const declaredView = UI_TRIGGERS[action] ?? ''
      expect(declaredView, `${action} has no declared UI trigger`).not.toBe('')
      expect(
        rendered[declaredView]?.has(action),
        `${action} has no button in ${declaredView}`,
      ).toBe(true)
      if (ROW_SCOPED_ACTIONS.includes(action)) {
        expect(
          rendered[declaredView]?.has(`${action}#row`),
          `${action} has no per-row button`,
        ).toBe(true)
      }
    }
  })

  it('UiParity_everyDataScopeIsRowBulkOrToolbar', async () => {
    for (const view of Object.values(VIEWS)) await actionsRenderedBy(view)

    expect([...seenScopes].filter((scope) => !ALLOWED_SCOPES.includes(scope))).toEqual([])
    expect(seenScopes.has('row')).toBe(true)
  })

  it('UiParity_theTriggerMapDeclaresNoUnknownAction', () => {
    const unknownActions = Object.keys(UI_TRIGGERS).filter(
      (action) => !API_ACTIONS.includes(action),
    )

    expect(unknownActions).toEqual([])
    for (const view of Object.values(UI_TRIGGERS)) expect(Object.keys(VIEWS)).toContain(view)
  })
})
