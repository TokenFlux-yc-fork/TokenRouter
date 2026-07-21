import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'
import type { Account } from '@/types'
import ReAuthAccountModal from '../ReAuthAccountModal.vue'

const {
  showSuccessMock,
  showErrorMock,
  showInfoMock,
  updateAccountMock,
  clearErrorMock,
  applyOAuthCredentialsMock,
  exchangeAuthCodeMock,
  buildCredentialsMock,
  buildExtraInfoMock,
  generateQoderAuthUrlMock,
  pollQoderAuthorizationMock,
  buildQoderCredentialsMock,
  resetQoderStateMock,
  invalidateQoderRequestsMock,
  qoderPollIntervalRef,
  qoderPollFailureRef
} = vi.hoisted(() => ({
  showSuccessMock: vi.fn(),
  showErrorMock: vi.fn(),
  showInfoMock: vi.fn(),
  updateAccountMock: vi.fn(),
  clearErrorMock: vi.fn(),
  applyOAuthCredentialsMock: vi.fn(),
  exchangeAuthCodeMock: vi.fn(),
  buildCredentialsMock: vi.fn(),
  buildExtraInfoMock: vi.fn(),
  generateQoderAuthUrlMock: vi.fn(),
  pollQoderAuthorizationMock: vi.fn(),
  buildQoderCredentialsMock: vi.fn(),
  resetQoderStateMock: vi.fn(),
  invalidateQoderRequestsMock: vi.fn(),
  qoderPollIntervalRef: { value: 2 },
  qoderPollFailureRef: { value: 'none' as 'none' | 'transient' | 'terminal' }
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showSuccess: showSuccessMock,
    showError: showErrorMock,
    showInfo: showInfoMock
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

vi.mock('@/api/admin', () => ({
  adminAPI: {
    accounts: {
      update: updateAccountMock,
      clearError: clearErrorMock,
      applyOAuthCredentials: applyOAuthCredentialsMock
    }
  }
}))

vi.mock('@/composables/useAccountOAuth', () => ({
  useAccountOAuth: () => ({
    authUrl: { value: '' },
    sessionId: { value: '' },
    loading: { value: false },
    error: { value: '' },
    resetState: vi.fn(),
    generateAuthUrl: vi.fn(),
    buildExtraInfo: vi.fn()
  })
}))

vi.mock('@/composables/useOpenAIOAuth', () => ({
  useOpenAIOAuth: () => ({
    authUrl: { value: 'https://auth.example.test' },
    sessionId: { value: 'session-1' },
    oauthState: { value: 'state-1' },
    loading: { value: false },
    error: { value: '' },
    resetState: vi.fn(),
    generateAuthUrl: vi.fn(),
    exchangeAuthCode: exchangeAuthCodeMock,
    buildCredentials: buildCredentialsMock,
    buildExtraInfo: buildExtraInfoMock
  })
}))

vi.mock('@/composables/useGeminiOAuth', () => ({
  useGeminiOAuth: () => ({
    authUrl: { value: '' },
    sessionId: { value: '' },
    state: { value: '' },
    loading: { value: false },
    error: { value: '' },
    resetState: vi.fn(),
    generateAuthUrl: vi.fn(),
    exchangeAuthCode: vi.fn(),
    buildCredentials: vi.fn()
  })
}))

vi.mock('@/composables/useAntigravityOAuth', () => ({
  useAntigravityOAuth: () => ({
    authUrl: { value: '' },
    sessionId: { value: '' },
    state: { value: '' },
    loading: { value: false },
    error: { value: '' },
    resetState: vi.fn(),
    generateAuthUrl: vi.fn(),
    exchangeAuthCode: vi.fn(),
    buildCredentials: vi.fn()
  })
}))

vi.mock('@/composables/useQoderOAuth', () => ({
  useQoderOAuth: () => ({
    authUrl: { value: 'https://qoder.example.test/device' },
    sessionId: { value: 'qoder-session' },
    state: { value: 'qoder-state' },
    loading: { value: false },
    polling: { value: false },
    error: { value: '' },
    pollInterval: qoderPollIntervalRef,
    pollFailure: qoderPollFailureRef,
    invalidatePendingRequests: invalidateQoderRequestsMock,
    resetState: resetQoderStateMock,
    generateAuthUrl: generateQoderAuthUrlMock,
    pollAuthorization: pollQoderAuthorizationMock,
    buildCredentials: buildQoderCredentialsMock
  })
}))

const BaseDialogStub = defineComponent({
  name: 'BaseDialog',
  props: {
    show: {
      type: Boolean,
      default: false
    },
    closeOnEscape: {
      type: Boolean,
      default: true
    }
  },
  emits: ['close'],
  template: '<div v-if="show"><slot /><slot name="footer" /></div>'
})

const OAuthAuthorizationFlowStub = defineComponent({
  name: 'OAuthAuthorizationFlow',
  emits: ['generate-url'],
  setup(_, { expose }) {
    expose({
      authCode: 'auth-code-1',
      oauthState: 'state-1',
      projectId: '',
      sessionKey: '',
      inputMethod: 'manual',
      reset: vi.fn()
    })
    return {}
  },
  template: '<div data-testid="oauth-flow" />'
})

function openAIAccount(): Account {
  return {
    id: 101,
    name: 'OpenAI OAuth',
    platform: 'openai',
    type: 'oauth',
    credentials: {},
    extra: {
      tls_fingerprint_router_id: 9
    },
    proxy_id: 12,
    concurrency: 1,
    priority: 0,
    status: 'error',
    error_message: 'old error',
    last_used_at: null,
    expires_at: null,
    auto_pause_on_expired: false,
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z',
    schedulable: true,
    rate_limited_at: null,
    rate_limit_reset_at: null,
    overload_until: null,
    temp_unschedulable_until: null,
    temp_unschedulable_reason: null,
    session_window_start: null,
    session_window_end: null,
    session_window_status: null,
    tls_fingerprint_router_id: 9
  }
}

function qoderAccount(): Account {
  return {
    ...openAIAccount(),
    id: 202,
    name: 'Qoder COSY',
    platform: 'qoder',
    type: 'cosy',
    credentials: { site: 'global' },
    proxy_id: 18,
    tls_fingerprint_router_id: null,
    extra: {}
  }
}

function createDeferred<T>() {
  let resolve!: (value: T | PromiseLike<T>) => void
  const promise = new Promise<T>((resolvePromise) => {
    resolve = resolvePromise
  })
  return { promise, resolve }
}

function mountModal(account: Account = qoderAccount()) {
  return mount(ReAuthAccountModal, {
    props: { show: true, account },
    global: {
      stubs: {
        BaseDialog: BaseDialogStub,
        OAuthAuthorizationFlow: OAuthAuthorizationFlowStub,
        Icon: true
      }
    }
  })
}

describe('admin/account/ReAuthAccountModal', () => {
  beforeEach(() => {
    showSuccessMock.mockReset()
    showErrorMock.mockReset()
    showInfoMock.mockReset()
    updateAccountMock.mockReset()
    clearErrorMock.mockReset()
    applyOAuthCredentialsMock.mockReset()
    exchangeAuthCodeMock.mockReset()
    buildCredentialsMock.mockReset()
    buildExtraInfoMock.mockReset()
    generateQoderAuthUrlMock.mockReset()
    pollQoderAuthorizationMock.mockReset()
    buildQoderCredentialsMock.mockReset()
    resetQoderStateMock.mockReset()
    invalidateQoderRequestsMock.mockReset()
    qoderPollIntervalRef.value = 2
    qoderPollFailureRef.value = 'none'

    exchangeAuthCodeMock.mockResolvedValue({
      access_token: 'new-access-token',
      refresh_token: 'new-refresh-token',
      email: 'user@example.test'
    })
    buildCredentialsMock.mockReturnValue({
      access_token: 'new-access-token',
      refresh_token: 'new-refresh-token'
    })
    buildExtraInfoMock.mockReturnValue({
      email: 'user@example.test'
    })
    applyOAuthCredentialsMock.mockResolvedValue({
      ...openAIAccount(),
      status: 'active',
      error_message: null
    })
    buildQoderCredentialsMock.mockReturnValue({
      security_oauth_token: 'cn-access-token',
      machine_id: 'cn-machine-id',
      site: 'cn',
      refresh_mode: 'qodercn20'
    })
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  it('OpenAI 重新授权使用增量合并接口保留 TLS Router 绑定', async () => {
    const wrapper = mountModal(openAIAccount())
    await flushPromises()

    await wrapper.find('button.btn-primary').trigger('click')
    await flushPromises()

    expect(exchangeAuthCodeMock).toHaveBeenCalledWith(
      'auth-code-1',
      'session-1',
      'state-1',
      12,
      9
    )
    expect(applyOAuthCredentialsMock).toHaveBeenCalledWith(101, {
      type: 'oauth',
      credentials: {
        access_token: 'new-access-token',
        refresh_token: 'new-refresh-token'
      },
      extra: {
        email: 'user@example.test'
      }
    })
    expect(updateAccountMock).not.toHaveBeenCalled()
    expect(clearErrorMock).not.toHaveBeenCalled()
    expect(wrapper.emitted('reauthorized')?.[0]?.[0]).toMatchObject({
      id: 101,
      status: 'active',
      error_message: null
    })
  })

  it('Qoder 完成设备授权后写入 CN 凭据并清除旧错误', async () => {
    const updated = { ...qoderAccount(), status: 'active' as const, error_message: null }
    pollQoderAuthorizationMock.mockResolvedValueOnce({
      status: 'completed',
      token_info: {
        security_oauth_token: 'cn-access-token',
        machine_id: 'cn-machine-id',
        site: 'cn'
      }
    })
    applyOAuthCredentialsMock.mockResolvedValueOnce(updated)

    const wrapper = mountModal()
    await flushPromises()

    await wrapper.find('button.btn-primary').trigger('click')
    await flushPromises()

    expect(pollQoderAuthorizationMock).toHaveBeenCalledWith(
      {
        sessionId: 'qoder-session',
        state: 'qoder-state'
      },
      { notifyError: true }
    )
    expect(applyOAuthCredentialsMock).toHaveBeenCalledWith(202, {
      type: 'cosy',
      credentials: {
        security_oauth_token: 'cn-access-token',
        machine_id: 'cn-machine-id',
        site: 'cn',
        refresh_mode: 'qodercn20'
      }
    })
    expect(updateAccountMock).not.toHaveBeenCalled()
    expect(clearErrorMock).not.toHaveBeenCalled()
    expect(wrapper.emitted('reauthorized')?.[0]?.[0]).toEqual(updated)
  })

  it('Qoder 设备授权未完成时不更新账号', async () => {
    pollQoderAuthorizationMock.mockResolvedValueOnce({ status: 'pending' })

    const wrapper = mountModal()
    await flushPromises()

    await wrapper.find('button.btn-primary').trigger('click')
    await flushPromises()

    expect(showInfoMock).toHaveBeenCalledWith('admin.accounts.oauth.qoder.authorizationPending')
    expect(updateAccountMock).not.toHaveBeenCalled()
    expect(clearErrorMock).not.toHaveBeenCalled()
  })

  it('Qoder 生成授权链接后自动轮询并原子保存凭据', async () => {
    vi.useFakeTimers()
    const updated = { ...qoderAccount(), status: 'active' as const, error_message: null }
    generateQoderAuthUrlMock.mockResolvedValueOnce(true)
    pollQoderAuthorizationMock
      .mockResolvedValueOnce({ status: 'pending' })
      .mockResolvedValueOnce({
        status: 'completed',
        token_info: {
          security_oauth_token: 'cn-access-token',
          machine_id: 'cn-machine-id'
        }
      })
    applyOAuthCredentialsMock.mockResolvedValueOnce(updated)
    const wrapper = mountModal()

    wrapper.findComponent(OAuthAuthorizationFlowStub).vm.$emit('generate-url')
    await flushPromises()
    expect(pollQoderAuthorizationMock).toHaveBeenCalledTimes(1)
    expect(showInfoMock).not.toHaveBeenCalled()

    await vi.advanceTimersByTimeAsync(2000)
    await flushPromises()

    expect(pollQoderAuthorizationMock).toHaveBeenCalledTimes(2)
    expect(applyOAuthCredentialsMock).toHaveBeenCalledTimes(1)
    expect(wrapper.emitted('reauthorized')?.[0]?.[0]).toEqual(updated)
  })

  it('Qoder 自动轮询遇到瞬时错误后继续并保存凭据', async () => {
    vi.useFakeTimers()
    const updated = { ...qoderAccount(), status: 'active' as const, error_message: null }
    generateQoderAuthUrlMock.mockResolvedValueOnce(true)
    pollQoderAuthorizationMock
      .mockImplementationOnce(async () => {
        qoderPollFailureRef.value = 'transient'
        return null
      })
      .mockImplementationOnce(async () => {
        qoderPollFailureRef.value = 'none'
        return {
          status: 'completed',
          token_info: {
            security_oauth_token: 'cn-access-token',
            machine_id: 'cn-machine-id'
          }
        }
      })
    applyOAuthCredentialsMock.mockResolvedValueOnce(updated)
    const wrapper = mountModal()

    wrapper.findComponent(OAuthAuthorizationFlowStub).vm.$emit('generate-url')
    await flushPromises()

    expect(pollQoderAuthorizationMock).toHaveBeenCalledTimes(1)
    expect(pollQoderAuthorizationMock).toHaveBeenLastCalledWith(
      { sessionId: 'qoder-session', state: 'qoder-state' },
      { notifyError: false }
    )

    await vi.advanceTimersByTimeAsync(2000)
    await flushPromises()

    expect(pollQoderAuthorizationMock).toHaveBeenCalledTimes(2)
    expect(applyOAuthCredentialsMock).toHaveBeenCalledTimes(1)
    expect(wrapper.emitted('reauthorized')?.[0]?.[0]).toEqual(updated)
  })

  it('Qoder 自动轮询遇到终止错误后停止', async () => {
    vi.useFakeTimers()
    generateQoderAuthUrlMock.mockResolvedValueOnce(true)
    pollQoderAuthorizationMock.mockImplementationOnce(async () => {
      qoderPollFailureRef.value = 'terminal'
      return null
    })
    const wrapper = mountModal()

    wrapper.findComponent(OAuthAuthorizationFlowStub).vm.$emit('generate-url')
    await flushPromises()
    await vi.advanceTimersByTimeAsync(10_000)

    expect(pollQoderAuthorizationMock).toHaveBeenCalledTimes(1)
    expect(applyOAuthCredentialsMock).not.toHaveBeenCalled()
  })

  it('Qoder 保存凭据时显示扁平化的授权冲突错误', async () => {
    pollQoderAuthorizationMock.mockResolvedValueOnce({
      status: 'completed',
      token_info: {
        security_oauth_token: 'cn-access-token',
        machine_id: 'cn-machine-id'
      }
    })
    applyOAuthCredentialsMock.mockRejectedValueOnce({
      status: 409,
      reason: 'QODER_AUTHORIZATION_CONFLICT',
      message: 'authorization changed concurrently'
    })
    const wrapper = mountModal()

    await wrapper.find('button.btn-primary').trigger('click')
    await flushPromises()

    expect(showErrorMock).toHaveBeenCalledWith('authorization changed concurrently')
    expect(wrapper.emitted('reauthorized')).toBeUndefined()
  })

  it('Qoder 自动轮询有截止时间', async () => {
    vi.useFakeTimers()
    qoderPollIntervalRef.value = 601
    generateQoderAuthUrlMock.mockResolvedValueOnce(true)
    pollQoderAuthorizationMock.mockResolvedValue({ status: 'pending' })
    const wrapper = mountModal()

    wrapper.findComponent(OAuthAuthorizationFlowStub).vm.$emit('generate-url')
    await flushPromises()
    expect(pollQoderAuthorizationMock).toHaveBeenCalledTimes(1)

    await vi.advanceTimersByTimeAsync(600_001)
    expect(pollQoderAuthorizationMock).toHaveBeenCalledTimes(1)
  })

  it('关闭弹窗后停止 Qoder 自动轮询', async () => {
    vi.useFakeTimers()
    generateQoderAuthUrlMock.mockResolvedValueOnce(true)
    pollQoderAuthorizationMock.mockResolvedValue({ status: 'pending' })
    const wrapper = mountModal()

    wrapper.findComponent(OAuthAuthorizationFlowStub).vm.$emit('generate-url')
    await flushPromises()
    wrapper.findComponent(BaseDialogStub).vm.$emit('close')
    await vi.advanceTimersByTimeAsync(10_000)

    expect(wrapper.emitted('close')).toHaveLength(1)
    expect(pollQoderAuthorizationMock).toHaveBeenCalledTimes(1)
    expect(invalidateQoderRequestsMock).toHaveBeenCalled()
  })

  it('卸载弹窗后停止 Qoder 自动轮询', async () => {
    vi.useFakeTimers()
    generateQoderAuthUrlMock.mockResolvedValueOnce(true)
    pollQoderAuthorizationMock.mockResolvedValue({ status: 'pending' })
    const wrapper = mountModal()

    wrapper.findComponent(OAuthAuthorizationFlowStub).vm.$emit('generate-url')
    await flushPromises()
    expect(pollQoderAuthorizationMock).toHaveBeenCalledTimes(1)

    wrapper.unmount()
    await vi.advanceTimersByTimeAsync(10_000)

    expect(pollQoderAuthorizationMock).toHaveBeenCalledTimes(1)
    expect(invalidateQoderRequestsMock).toHaveBeenCalled()
  })

  it('Qoder 保存期间阻止关闭和重复提交', async () => {
    const updated = { ...qoderAccount(), status: 'active' as const, error_message: null }
    const saveRequest = createDeferred<Account>()
    pollQoderAuthorizationMock.mockResolvedValue({
      status: 'completed',
      token_info: {
        security_oauth_token: 'cn-access-token',
        machine_id: 'cn-machine-id'
      }
    })
    applyOAuthCredentialsMock.mockReturnValueOnce(saveRequest.promise)
    const wrapper = mountModal()

    await wrapper.find('button.btn-primary').trigger('click')
    await flushPromises()

    const submitButton = wrapper.get('button.btn-primary')
    expect(submitButton.attributes('disabled')).toBeDefined()
    expect(wrapper.findComponent(BaseDialogStub).props('closeOnEscape')).toBe(false)
    wrapper.findComponent(BaseDialogStub).vm.$emit('close')
    await submitButton.trigger('click')
    expect(wrapper.emitted('close')).toBeUndefined()
    expect(pollQoderAuthorizationMock).toHaveBeenCalledTimes(1)
    expect(applyOAuthCredentialsMock).toHaveBeenCalledTimes(1)

    saveRequest.resolve(updated)
    await flushPromises()
    expect(wrapper.emitted('reauthorized')?.[0]?.[0]).toEqual(updated)
    expect(wrapper.emitted('close')).toHaveLength(1)
  })

  it('切换账号后忽略旧 Qoder 保存请求的界面结果', async () => {
    const saveRequest = createDeferred<Account>()
    pollQoderAuthorizationMock.mockResolvedValueOnce({
      status: 'completed',
      token_info: {
        security_oauth_token: 'cn-access-token',
        machine_id: 'cn-machine-id'
      }
    })
    applyOAuthCredentialsMock.mockReturnValueOnce(saveRequest.promise)
    const wrapper = mountModal()

    await wrapper.find('button.btn-primary').trigger('click')
    await flushPromises()
    expect(applyOAuthCredentialsMock).toHaveBeenCalledTimes(1)

    await wrapper.setProps({ account: { ...qoderAccount(), id: 203, name: 'Next Qoder' } })
    saveRequest.resolve({ ...qoderAccount(), status: 'active', error_message: null })
    await flushPromises()

    expect(wrapper.emitted('reauthorized')).toBeUndefined()
    expect(wrapper.emitted('close')).toBeUndefined()
    expect(showSuccessMock).not.toHaveBeenCalled()
  })

  it('切换账号会使尚未完成的 Qoder 轮询结果失效', async () => {
    const pollRequest = createDeferred<{
      status: 'completed'
      token_info: { security_oauth_token: string; machine_id: string }
    }>()
    pollQoderAuthorizationMock.mockReturnValueOnce(pollRequest.promise)
    const wrapper = mountModal()

    await wrapper.find('button.btn-primary').trigger('click')
    await wrapper.setProps({ account: { ...qoderAccount(), id: 203, name: 'Next Qoder' } })
    pollRequest.resolve({
      status: 'completed',
      token_info: {
        security_oauth_token: 'stale-token',
        machine_id: 'stale-machine'
      }
    })
    await flushPromises()

    expect(applyOAuthCredentialsMock).not.toHaveBeenCalled()
    expect(invalidateQoderRequestsMock).toHaveBeenCalled()
    expect(wrapper.emitted('reauthorized')).toBeUndefined()
  })
})
