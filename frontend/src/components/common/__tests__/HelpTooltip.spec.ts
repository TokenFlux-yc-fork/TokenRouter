import { afterEach, describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { nextTick } from 'vue'
import HelpTooltip from '@/components/common/HelpTooltip.vue'

function getTooltipElement(): HTMLDivElement {
  const tooltip = document.body.querySelector('[role="tooltip"]')
  if (!(tooltip instanceof HTMLDivElement)) {
    throw new Error('tooltip element not found')
  }
  return tooltip
}

describe('HelpTooltip', () => {
  afterEach(() => {
    vi.restoreAllMocks()
    document.body.innerHTML = ''
  })

  it('keeps the existing hover interaction by default', async () => {
    const wrapper = mount(HelpTooltip, {
      attachTo: document.body,
      props: {
        content: 'hover details',
      },
    })

    const trigger = wrapper.get('.group')
    const tooltip = getTooltipElement()

    expect(tooltip.style.display).toBe('none')

    await trigger.trigger('mouseenter')
    await nextTick()
    expect(tooltip.style.display).not.toBe('none')

    await trigger.trigger('mouseleave')
    await nextTick()
    expect(tooltip.style.display).toBe('none')

    wrapper.unmount()
  })

  it('supports click-to-toggle details and closes on outside click', async () => {
    const wrapper = mount(HelpTooltip, {
      attachTo: document.body,
      props: {
        content: 'click details',
        trigger: 'click',
      },
    })

    const trigger = wrapper.get('.group')
    const tooltip = getTooltipElement()

    expect(tooltip.style.display).toBe('none')

    await trigger.trigger('click')
    await nextTick()
    expect(tooltip.style.display).not.toBe('none')
    expect(tooltip.textContent).toContain('click details')

    const closeButton = tooltip.querySelector('button[aria-label="Close"]')
    if (!(closeButton instanceof HTMLButtonElement)) {
      throw new Error('close button not found')
    }
    closeButton.click()
    await nextTick()
    expect(tooltip.style.display).toBe('none')

    await trigger.trigger('click')
    await nextTick()
    expect(tooltip.style.display).not.toBe('none')

    document.body.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    await nextTick()
    expect(tooltip.style.display).toBe('none')

    wrapper.unmount()
  })

  it('uses viewport coordinates and follows the trigger after scrolling', async () => {
    vi.spyOn(window, 'scrollX', 'get').mockReturnValue(240)
    vi.spyOn(window, 'scrollY', 'get').mockReturnValue(600)

    const wrapper = mount(HelpTooltip, {
      attachTo: document.body,
      props: {
        content: 'scroll details',
      },
    })

    const trigger = wrapper.get('.group')
    const getBoundingClientRect = vi.fn(() => ({
      top: 180,
      left: 120,
      width: 60,
      height: 20,
      right: 180,
      bottom: 200,
      x: 120,
      y: 180,
      toJSON: () => ({}),
    }))
    trigger.element.getBoundingClientRect = getBoundingClientRect

    await trigger.trigger('mouseenter')
    await nextTick()

    const tooltip = getTooltipElement()
    expect(tooltip.style.top).toBe('calc(172px)')
    expect(tooltip.style.left).toBe('150px')

    getBoundingClientRect.mockReturnValue({
      top: 80,
      left: 40,
      width: 60,
      height: 20,
      right: 100,
      bottom: 100,
      x: 40,
      y: 80,
      toJSON: () => ({}),
    })
    window.dispatchEvent(new Event('scroll'))
    await nextTick()

    expect(tooltip.style.top).toBe('calc(72px)')
    expect(tooltip.style.left).toBe('70px')

    wrapper.unmount()
  })
})
