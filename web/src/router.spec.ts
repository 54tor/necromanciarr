import { mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it } from 'vitest'
import { defineComponent } from 'vue'
import { ROUTES, currentPath, navigate, pathFromHash, useHashRouter } from './router'

describe('Router', () => {
  beforeEach(() => {
    window.location.hash = ''
  })

  it('Router_parsesKnownHashesAndFallsBackToProposals', () => {
    for (const route of ROUTES) expect(pathFromHash(`#${route.path}`)).toBe(route.path)

    expect(pathFromHash('')).toBe('/propositions')
    expect(pathFromHash('#/inconnu')).toBe('/propositions')
  })

  it('Router_navigateUpdatesTheHashAndTheCurrentPath', () => {
    navigate('/liens')

    expect(window.location.hash).toBe('#/liens')
    expect(currentPath.value).toBe('/liens')
  })

  it('Router_followsHashchangeAndStopsAfterUnmount', () => {
    const wrapper = mount(defineComponent({ setup: () => useHashRouter(), template: '<div />' }))

    window.location.hash = '#/etapes'
    window.dispatchEvent(new HashChangeEvent('hashchange'))
    expect(currentPath.value).toBe('/etapes')

    wrapper.unmount()
    window.location.hash = '#/liens'
    window.dispatchEvent(new HashChangeEvent('hashchange'))
    expect(currentPath.value).toBe('/etapes')
  })

  it('Router_declaresTheFiveTabsInOrder', () => {
    const gotKeys = ROUTES.map((route) => route.navKey)

    expect(gotKeys).toEqual(['demarrage', 'propositions', 'deduplication', 'liens', 'etapes'])
  })
})
