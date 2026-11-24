import { mount, type VueWrapper } from '@vue/test-utils'
import { Link, Rocket } from '@lucide/vue'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { nextTick } from 'vue'
import { setLanguage } from '@/i18n'
import { navigate } from '@/router'
import { resetAppState } from '@/test/fakeApi'
import { stubViewport } from '@/test/viewport'
import AppShell from './AppShell.vue'
import PageHeader from './PageHeader.vue'
import PageToolbar from './PageToolbar.vue'

const navItems = [
  { fragment: '/demarrage', labelKey: 'nav.demarrage', icon: Rocket },
  { fragment: '/liens', labelKey: 'nav.liens', icon: Link, badge: 3 },
  { fragment: '/etapes', labelKey: 'nav.etapes', icon: Link, badge: 0 },
]

const mounted: VueWrapper[] = []
function mountShell(width: number) {
  stubViewport(width)
  const wrapper = mount(AppShell, {
    props: { navItems },
    slots: { default: '<p data-testid="content">contenu</p>' },
    attachTo: document.body,
  })
  mounted.push(wrapper as VueWrapper)
  return wrapper
}

describe('AppShell', () => {
  beforeEach(resetAppState)
  afterEach(() => {
    mounted.splice(0).forEach((wrapper) => wrapper.unmount())
    vi.unstubAllGlobals()
  })

  it('marks the current page with aria-current', () => {
    navigate('/liens')
    const wrapper = mountShell(1280)

    const gotCurrent = wrapper
      .findAll('nav a[aria-current="page"] [data-testid="nav-label"]')
      .map((label) => label.text())

    expect(gotCurrent).toEqual(['Liens'])
  })

  it('labels the nav landmark and exposes main', () => {
    const wrapper = mountShell(1280)

    expect(wrapper.get('nav').attributes('aria-label')).toBe('Navigation principale')
    expect(wrapper.find('main#main-content').exists()).toBe(true)
    expect(wrapper.get('[data-testid="content"]').text()).toBe('contenu')
  })

  it('collapses to icons between 768 and 1023px and becomes a drawer below 768px', async () => {
    const wide = mountShell(1280)
    expect(wide.get('aside').attributes('data-mode')).toBe('wide')

    const compact = mountShell(900)
    expect(compact.get('aside').attributes('data-mode')).toBe('compact')
    expect(compact.get('nav a').find('.sr-only').text()).toBe('Démarrage')

    const narrow = mountShell(390)
    expect(narrow.find('aside').exists()).toBe(false)
    expect(narrow.find('nav').exists()).toBe(false)
    await narrow.get('[data-testid="drawer-open"]').trigger('click')
    await nextTick()
    const drawerLinks = Array.from(document.body.querySelectorAll('[role="dialog"] nav a'))
    expect(drawerLinks.map((link) => link.getAttribute('href'))).toEqual([
      '#/demarrage',
      '#/liens',
      '#/etapes',
    ])
  })

  it('closes the drawer when a page is chosen', async () => {
    const wrapper = mountShell(390)
    await wrapper.get('[data-testid="drawer-open"]').trigger('click')
    await nextTick()
    expect(document.body.querySelector('[role="dialog"][data-state="open"]')).not.toBeNull()

    navigate('/etapes')
    await nextTick()
    await nextTick()

    expect(document.body.querySelector('[role="dialog"][data-state="open"]')).toBeNull()
  })

  it('lets the sidebar scroll so a long nav is never clipped', () => {
    const wrapper = mountShell(1280)

    expect(wrapper.get('aside').classes()).toContain('overflow-y-auto')
  })

  it('hides zero badges', () => {
    const wrapper = mountShell(1280)

    const gotBadges = wrapper.findAll('[data-testid="nav-badge"]').map((badge) => badge.text())

    expect(gotBadges).toEqual(['3'])
  })

  it('moves the focus to the content from the skip link without changing the route', async () => {
    navigate('/liens')
    const wrapper = mountShell(1280)

    await wrapper.get('[data-testid="skip-link"]').trigger('click')

    expect(document.activeElement?.id).toBe('main-content')
    expect(window.location.hash).toBe('#/liens')
  })
})

describe('AppShell logos', () => {
  beforeEach(resetAppState)
  afterEach(() => {
    mounted.splice(0).forEach((wrapper) => wrapper.unmount())
    vi.unstubAllGlobals()
  })

  it('shows the full logo when wide, the emblem when compact, both with the translated alt', () => {
    const wide = mountShell(1280)
    const compact = mountShell(900)

    expect(wide.get('aside img').attributes()).toMatchObject({
      src: '/branding/logo-complet.webp',
      alt: 'Logo de Necromanciarr',
      width: '144',
      height: '144',
    })
    expect(compact.get('aside img').attributes()).toMatchObject({
      src: '/branding/embleme.webp',
      alt: 'Logo de Necromanciarr',
    })
  })

  it('uses the English alt text when the language is English', () => {
    setLanguage('en')
    const wrapper = mountShell(1280)

    expect(wrapper.get('aside img').attributes('alt')).toBe('Necromanciarr logo')
  })

  it('keeps the product name next to the emblem on the mobile bar', () => {
    const wrapper = mountShell(390)

    expect(wrapper.get('header').text()).toContain('Necromanciarr')
  })
})

describe('PageHeader and PageToolbar', () => {
  it('renders the translated title as the page heading', () => {
    const wrapper = mount(PageHeader, { props: { titleKey: 'nav.etapes' } })

    expect(wrapper.get('h1').text()).toBe('Étapes')
    expect(wrapper.element.tagName).toBe('HEADER')
  })

  it('places actions and filters in their own slots', () => {
    const wrapper = mount(PageToolbar, {
      slots: {
        actions: '<button data-scope="toolbar">a</button>',
        filters: '<input aria-label="f" />',
      },
    })

    expect(wrapper.get('[data-slot="toolbar-actions"] button').attributes('data-scope')).toBe(
      'toolbar',
    )
    expect(wrapper.find('[data-slot="toolbar-filters"] input').exists()).toBe(true)
  })
})
