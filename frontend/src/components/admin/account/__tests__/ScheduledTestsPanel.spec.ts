import { beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'

const {
  listByAccountMock,
  listAccountResultsMock,
  createMock,
  updateMock,
  deleteMock,
  listResultsMock,
  showErrorMock,
  showSuccessMock
} = vi.hoisted(() => ({
  listByAccountMock: vi.fn(),
  listAccountResultsMock: vi.fn(),
  createMock: vi.fn(),
  updateMock: vi.fn(),
  deleteMock: vi.fn(),
  listResultsMock: vi.fn(),
  showErrorMock: vi.fn(),
  showSuccessMock: vi.fn()
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    scheduledTests: {
      listByAccount: listByAccountMock,
      listAccountResults: listAccountResultsMock,
      create: createMock,
      update: updateMock,
      delete: deleteMock,
      listResults: listResultsMock
    }
  }
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showError: showErrorMock,
    showSuccess: showSuccessMock
  })
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => key
    })
  }
})

import ScheduledTestsPanel from '../ScheduledTestsPanel.vue'

const BaseDialogStub = defineComponent({
  name: 'BaseDialog',
  props: { show: { type: Boolean, default: false } },
  template: '<div v-if="show"><slot /></div>'
})

const InputStub = defineComponent({
  name: 'InputStub',
  props: { modelValue: { type: [String, Number], default: '' } },
  emits: ['update:modelValue'],
  template: `
    <input
      class="input-stub"
      :value="modelValue"
      @input="$emit('update:modelValue', $event.target.value)"
    />
  `
})

const SelectStub = defineComponent({
  name: 'SelectStub',
  props: {
    modelValue: { type: [String, Number], default: '' },
    options: { type: Array, default: () => [] }
  },
  emits: ['update:modelValue'],
  template: `
    <select
      class="select-stub"
      :value="modelValue"
      @change="$emit('update:modelValue', $event.target.value)"
    >
      <option value=""></option>
      <option v-for="option in options" :key="option.value" :value="option.value">
        {{ option.label }}
      </option>
    </select>
  `
})

const ToggleStub = defineComponent({
  name: 'Toggle',
  props: { modelValue: { type: Boolean, default: false } },
  emits: ['update:modelValue'],
  template: `
    <input
      class="toggle-stub"
      type="checkbox"
      :checked="modelValue"
      @change="$emit('update:modelValue', $event.target.checked)"
    />
  `
})

const HelpTooltipStub = defineComponent({
  name: 'HelpTooltip',
  template: '<span><slot name="trigger" /><slot /></span>'
})

const basePlan = {
  id: 7,
  account_id: 42,
  model_id: 'gpt-plan',
  cron_expression: '*/30 * * * *',
  enabled: true,
  max_results: 100,
  auto_recover: false,
  account_circuit_breaker_enabled: true,
  failure_threshold: 3,
  success_threshold: 2,
  failure_cooldown_minutes: 5,
  timeout_seconds: 30,
  last_run_at: null,
  next_run_at: null,
  created_at: '2026-07-10T00:00:00Z',
  updated_at: '2026-07-10T00:00:00Z'
}

function mountPanel() {
  return mount(ScheduledTestsPanel, {
    props: {
      show: false,
      accountId: 42,
      modelOptions: [
        { value: 'gpt-plan', label: 'GPT Plan' },
        { value: 'gpt-new', label: 'GPT New' }
      ]
    },
    global: {
      stubs: {
        BaseDialog: BaseDialogStub,
        ConfirmDialog: true,
        HelpTooltip: HelpTooltipStub,
        Select: SelectStub,
        Input: InputStub,
        Toggle: ToggleStub,
        Icon: true
      }
    }
  })
}

describe('ScheduledTestsPanel', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    listByAccountMock.mockResolvedValue([basePlan])
    listAccountResultsMock.mockResolvedValue([
      {
        id: 81,
        plan_id: 7,
        account_id: 42,
        model_id: 'gpt-recent',
        cron_expression: '*/30 * * * *',
        status: 'failed',
        response_text: '',
        error_message: 'upstream status 503',
        latency_ms: 125,
        started_at: '2026-07-10T01:00:00Z',
        finished_at: '2026-07-10T01:00:01Z',
        created_at: '2026-07-10T01:00:01Z'
      }
    ])
    createMock.mockResolvedValue(basePlan)
    updateMock.mockResolvedValue(basePlan)
    deleteMock.mockResolvedValue(undefined)
    listResultsMock.mockResolvedValue([])
  })

  it('loads plans and account-wide recent results when opened', async () => {
    const wrapper = mountPanel()

    await wrapper.setProps({ show: true })
    await flushPromises()

    expect(listByAccountMock).toHaveBeenCalledWith(42)
    expect(listAccountResultsMock).toHaveBeenCalledWith(42, 20)
    expect(wrapper.text()).toContain('gpt-plan')
    expect(wrapper.text()).toContain('gpt-recent')
    expect(wrapper.text()).toContain('plan #7')
    expect(wrapper.text()).toContain('125ms')
  })

  it('sends all account circuit-breaker settings when creating a plan', async () => {
    const wrapper = mountPanel()
    await wrapper.setProps({ show: true })
    await flushPromises()

    const addButton = wrapper.findAll('button').find((button) =>
      button.text().includes('admin.scheduledTests.addPlan')
    )
    expect(addButton).toBeDefined()
    await addButton!.trigger('click')

    await wrapper.find('select.select-stub').setValue('gpt-new')
    const inputs = wrapper.findAll('input.input-stub')
    expect(inputs).toHaveLength(2)
    await inputs[0].setValue('*/5 * * * *')
    await inputs[1].setValue('40')

    const toggles = wrapper.findAll('input.toggle-stub')
    expect(toggles).toHaveLength(4)
    await toggles[2].setValue(true)

    const breakerInputs = wrapper.findAll('input.input-stub')
    expect(breakerInputs).toHaveLength(6)
    await breakerInputs[2].setValue('4')
    await breakerInputs[3].setValue('3')
    await breakerInputs[4].setValue('8')
    await breakerInputs[5].setValue('45')

    const saveButton = wrapper.findAll('button').find((button) =>
      button.text().includes('common.save')
    )
    expect(saveButton).toBeDefined()
    await saveButton!.trigger('click')
    await flushPromises()

    expect(createMock).toHaveBeenCalledWith({
      account_id: 42,
      model_id: 'gpt-new',
      cron_expression: '*/5 * * * *',
      enabled: true,
      max_results: 40,
      auto_recover: false,
      account_circuit_breaker_enabled: true,
      failure_threshold: 4,
      success_threshold: 3,
      failure_cooldown_minutes: 8,
      timeout_seconds: 45
    })
  })
})
