import { flushPromises, mount } from '@vue/test-utils'
import { h } from 'vue'
import { afterEach, describe, expect, it } from 'vitest'
import { setLanguage } from '@/i18n'
import Dialog from './Dialog.vue'
import DialogContent from './DialogContent.vue'
import DialogFooter from './DialogFooter.vue'
import DialogScrollContent from './DialogScrollContent.vue'
import DialogTitle from './DialogTitle.vue'

async function openDialogText(
  content: typeof DialogContent | typeof DialogScrollContent | typeof DialogFooter,
) {
  const wrapper = mount(Dialog, {
    props: { open: true },
    slots: {
      default: () =>
        content === DialogFooter
          ? h(DialogContent, { showCloseButton: false }, () => [
              h(DialogTitle, () => 't'),
              h(DialogFooter, { showCloseButton: true }),
            ])
          : h(content, null, () => h(DialogTitle, () => 't')),
    },
    attachTo: document.body,
  })
  await flushPromises()
  const text = document.body.textContent ?? ''
  wrapper.unmount()
  return text
}

describe('dialog close label follows the language', () => {
  afterEach(() => setLanguage('fr'))

  it.each([
    ['DialogContent', DialogContent],
    ['DialogScrollContent', DialogScrollContent],
    ['DialogFooter', DialogFooter],
  ])('%s: fr then en', async (_name, component) => {
    setLanguage('fr')
    const frenchText = await openDialogText(component)
    setLanguage('en')
    const englishText = await openDialogText(component)

    expect(frenchText).toContain('Fermer')
    expect(englishText).toContain('Close')
    expect(englishText).not.toContain('Fermer')
  })
})
