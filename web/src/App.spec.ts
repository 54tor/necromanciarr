import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import App from './App.vue'

describe('App', () => {
  it('renders the product name', () => {
    const wrapper = mount(App)

    const gotValue = wrapper.text()

    expect(gotValue).toContain('Necromanciarr')
  })
})
