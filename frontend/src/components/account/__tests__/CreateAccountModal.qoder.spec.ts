import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'

const {
  createAccountMock,
  generateQoderAuthUrlMock,
  exchangeQoderCodeMock,
  pollQoderAuthMock,
  showErrorMock,
  getSettingsMock,
  getWebSearchEmulationConfigMock,
  listTLSProfilesMock,
  listTLSRoutersMock
} = vi.hoisted(() => ({
  createAccountMock: vi.fn(),
  generateQoderAuthUrlMock: vi.fn(),
  exchangeQoderCodeMock: vi.fn(),
  pollQoderAuthMock: vi.fn(),
  showErrorMock: vi.fn(),
  getSettingsMock: vi.fn(),
  getWebSearchEmulationConfigMock: vi.fn(),
  listTLSProfilesMock: vi.fn(),
  listTLSRoutersMock: vi.fn()
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    accounts: {
      create: createAccountMock,
      checkMixedChannelRisk: vi.fn()
    },
    settings: {
      getSettings: getSettingsMock,
      getWebSearchEmulationConfig: getWebSearchEmulationConfigMock,
      getOpenAIOAuthImportDefaults: vi.fn()
    },
    tlsFingerprintProfiles: {
      list: listTLSProfilesMock
    },
    tlsFingerprintRouters: {
      list: listTLSRoutersMock
    },
    qoder: {
      generateAuthUrl: generateQoderAuthUrlMock,
      exchangeCode: exchangeQoderCodeMock,
      poll: pollQoderAuthMock
    }
  }
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showError: showErrorMock,
    showSuccess: vi.fn(),
    showInfo: vi.fn(),
    showWarning: vi.fn()
  })
}))

vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({
    isSimpleMode: true
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

import CreateAccountModal from '../CreateAccountModal.vue'

const BaseDialogStub = defineComponent({
  name: 'BaseDialog',
  props: {
    show: {
      type: Boolean,
      default: false
    }
  },
  emits: ['close'],
  template: '<div v-if="show"><slot /><slot name="footer" /></div>'
})

const ModelWhitelistSelectorStub = defineComponent({
  name: 'ModelWhitelistSelector',
  props: {
    modelValue: {
      type: Array,
      default: () => []
    },
    models: {
      type: Array,
      default: () => []
    }
  },
  emits: ['update:modelValue'],
  template: `
    <div>
      <button
        type="button"
        data-testid="rewrite-to-qoder-defaults"
        @click="$emit('update:modelValue', ['claude-opus-4-6', 'auto'])"
      >
        rewrite qoder
      </button>
      <span data-testid="model-whitelist-value">
        {{ Array.isArray(modelValue) ? modelValue.join(',') : '' }}
      </span>
    </div>
  `
})

const OAuthAuthorizationFlowStub = defineComponent({
  name: 'OAuthAuthorizationFlow',
  props: {
    authUrl: { type: String, default: '' },
    sessionId: { type: String, default: '' },
    loading: { type: Boolean, default: false }
  },
  emits: ['generate-url'],
  data: () => ({
    authCode: 'http://localhost:1455/callback?code=code&state=state-value',
    oauthState: '',
    inputMethod: 'manual'
  }),
  methods: {
    reset() {
      this.authCode = ''
      this.oauthState = ''
      this.inputMethod = 'manual'
    }
  },
  template: `
    <div>
      <button type="button" data-testid="generate-qoder-auth-url" @click="$emit('generate-url')">
        generate
      </button>
      <span data-testid="qoder-oauth-session">{{ sessionId }}</span>
    </div>
  `
})

function mountModal() {
  return mount(CreateAccountModal, {
    props: {
      show: true,
      proxies: [],
      groups: []
    },
    global: {
      stubs: {
        BaseDialog: BaseDialogStub,
        ConfirmDialog: true,
        Select: true,
        Icon: true,
        ProxySelector: true,
        ProxyAdBanner: true,
        GroupSelector: true,
        OAuthAuthorizationFlow: OAuthAuthorizationFlowStub,
        QuotaLimitCard: true,
        ModelWhitelistSelector: ModelWhitelistSelectorStub
      }
    }
  })
}

function createDeferred<T>() {
  let resolve!: (value: T | PromiseLike<T>) => void
  const promise = new Promise<T>((resolvePromise) => {
    resolve = resolvePromise
  })
  return { promise, resolve }
}

async function openQoderOAuthStep(wrapper: ReturnType<typeof mountModal>) {
  await wrapper.get('[data-testid="create-account-platform-qoder"]').trigger('click')
  await flushPromises()

  const nameInput = wrapper
    .findAll('input')
    .find((input) => input.attributes('type') === 'text' && input.attributes('required') !== undefined)
  expect(nameInput).toBeTruthy()
  await nameInput!.setValue('Qoder OAuth')
  await wrapper.get('form#create-account-form').trigger('submit.prevent')
  await flushPromises()
}

function findButtonByText(wrapper: ReturnType<typeof mountModal>, text: string) {
  const button = wrapper.findAll('button').find((candidate) => candidate.text().includes(text))
  expect(button).toBeTruthy()
  return button!
}

async function fillQoderManualForm(wrapper: ReturnType<typeof mountModal>) {
  await wrapper.get('[data-testid="create-account-platform-qoder"]').trigger('click')
  await flushPromises()

  const inputs = wrapper.findAll('input')
  const nameInput = inputs.find((input) => input.attributes('type') === 'text' && input.attributes('required') !== undefined)
  expect(nameInput).toBeTruthy()
  await nameInput!.setValue('Qoder COSY')

  const manualButton = wrapper
    .findAll('button')
    .find((button) => button.text().includes('admin.accounts.qoder.accountType.manualTitle'))
  expect(manualButton).toBeTruthy()
  await manualButton!.trigger('click')
  await flushPromises()

  const visibleInputs = wrapper.findAll('input')
  await visibleInputs.find((input) => input.attributes('placeholder') === 'dt-...')!.setValue('token')
  await visibleInputs.find((input) => input.attributes('placeholder') === 'machine_id')!.setValue('machine')
  await visibleInputs.find((input) => input.attributes('placeholder') === 'uid or aid')!.setValue('uid')
  await wrapper.get('[data-testid="create-qoder-refresh-token"]').setValue('refresh-token')
}

describe('CreateAccountModal Qoder model restriction', () => {
  beforeEach(() => {
    createAccountMock.mockReset()
    getSettingsMock.mockReset()
    getWebSearchEmulationConfigMock.mockReset()
    listTLSProfilesMock.mockReset()
    listTLSRoutersMock.mockReset()
    generateQoderAuthUrlMock.mockReset()
    exchangeQoderCodeMock.mockReset()
    pollQoderAuthMock.mockReset()
    showErrorMock.mockReset()

    createAccountMock.mockResolvedValue({})
    getSettingsMock.mockResolvedValue({ account_quota_notify_enabled: false })
    getWebSearchEmulationConfigMock.mockResolvedValue({ enabled: false, providers: [] })
    listTLSProfilesMock.mockResolvedValue([])
    listTLSRoutersMock.mockResolvedValue([])
    pollQoderAuthMock.mockResolvedValue({ status: 'pending' })
    vi.spyOn(window, 'open').mockReturnValue(null)
  })

  afterEach(() => {
    vi.useRealTimers()
    vi.restoreAllMocks()
  })

  it('does not persist generated Qoder model mappings on default manual create', async () => {
    const wrapper = mountModal()
    await fillQoderManualForm(wrapper)

    await wrapper.get('form#create-account-form').trigger('submit.prevent')
    await flushPromises()

    expect(createAccountMock).toHaveBeenCalledTimes(1)
    const payload = createAccountMock.mock.calls[0]?.[0]
    expect(payload.platform).toBe('qoder')
    expect(payload.type).toBe('cosy')
    expect(payload.credentials.model_mapping).toBeUndefined()
    expect(payload.credentials.model_whitelist).toBeUndefined()
    expect(payload.credentials.site).toBe('cn')
    expect(payload.credentials.refresh_mode).toBe('qodercn20')
    expect(payload.credentials.refresh_token).toBe('refresh-token')
  })

  it('creates Qoder manual account with PAT bootstrap without token fields', async () => {
    const wrapper = mountModal()
    await wrapper.get('[data-testid="create-account-platform-qoder"]').trigger('click')
    await flushPromises()

    const inputs = wrapper.findAll('input')
    const nameInput = inputs.find((input) => input.attributes('type') === 'text' && input.attributes('required') !== undefined)
    expect(nameInput).toBeTruthy()
    await nameInput!.setValue('Qoder PAT')

    const manualButton = wrapper
      .findAll('button')
      .find((button) => button.text().includes('admin.accounts.qoder.accountType.manualTitle'))
    expect(manualButton).toBeTruthy()
    await manualButton!.trigger('click')
    await flushPromises()

    const patInput = wrapper.findAll('input').find((input) => input.attributes('placeholder') === 'pat-...')
    expect(patInput).toBeTruthy()
    await patInput!.setValue('pat-123')

    await wrapper.get('form#create-account-form').trigger('submit.prevent')
    await flushPromises()

    expect(createAccountMock).toHaveBeenCalledTimes(1)
    const payload = createAccountMock.mock.calls[0]?.[0]
    expect(payload.platform).toBe('qoder')
    expect(payload.type).toBe('cosy')
    expect(payload.credentials).toMatchObject({ pat: 'pat-123' })
    expect(payload.credentials.site).toBe('cn')
    expect(payload.credentials.refresh_mode).toBeUndefined()
    expect(payload.credentials.refresh_token).toBeUndefined()
    expect(payload.credentials.security_oauth_token).toBeUndefined()
    expect(payload.credentials.machine_id).toBeUndefined()
  })

  it('exposes selected state and switch semantics for Qoder controls', async () => {
    const wrapper = mountModal()
    await wrapper.get('[data-testid="create-account-platform-qoder"]').trigger('click')
    await flushPromises()

    const oauthButton = wrapper
      .findAll('button')
      .find((button) => button.text().includes('admin.accounts.qoder.accountType.oauthTitle'))
    const manualButton = wrapper
      .findAll('button')
      .find((button) => button.text().includes('admin.accounts.qoder.accountType.manualTitle'))
    expect(oauthButton?.attributes('aria-pressed')).toBe('true')
    expect(manualButton?.attributes('aria-pressed')).toBe('false')

    await manualButton!.trigger('click')
    expect(oauthButton?.attributes('aria-pressed')).toBe('false')
    expect(manualButton?.attributes('aria-pressed')).toBe('true')

    const tlsToggle = wrapper.get('[data-testid="create-qoder-tls-fingerprint-toggle"]')
    expect(tlsToggle.attributes('role')).toBe('switch')
    expect(tlsToggle.attributes('aria-checked')).toBe('false')
    expect(tlsToggle.attributes('aria-label'))
      .toBe('admin.accounts.quotaControl.tlsFingerprint.label')
    await tlsToggle.trigger('click')
    expect(tlsToggle.attributes('aria-checked')).toBe('true')
  })

  it('creates manual credentials for the China site without a site selector', async () => {
    const wrapper = mountModal()
    await fillQoderManualForm(wrapper)
    expect(wrapper.find('[data-testid="create-qoder-site-global"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="create-qoder-site-cn"]').exists()).toBe(false)
    await wrapper.get('form#create-account-form').trigger('submit.prevent')
    await flushPromises()

    const payload = createAccountMock.mock.calls[0]?.[0]
    expect(payload.credentials).toMatchObject({
      site: 'cn',
      refresh_mode: 'qodercn20',
      security_oauth_token: 'token',
      refresh_token: 'refresh-token',
      machine_id: 'machine'
    })
  })

  it('requires a refresh token for direct Qoder CN credentials', async () => {
    const wrapper = mountModal()
    await fillQoderManualForm(wrapper)
    await wrapper.get('[data-testid="create-qoder-refresh-token"]').setValue('')

    await wrapper.get('form#create-account-form').trigger('submit.prevent')
    await flushPromises()

    expect(createAccountMock).not.toHaveBeenCalled()
    expect(showErrorMock).toHaveBeenCalledWith('admin.accounts.qoder.pleaseEnterRefreshToken')
  })

  it('keeps site selection unavailable while a manual account creation request is pending', async () => {
    const deferred = createDeferred<Record<string, never>>()
    createAccountMock.mockReturnValueOnce(deferred.promise)
    const wrapper = mountModal()
    await fillQoderManualForm(wrapper)

    await wrapper.get('form#create-account-form').trigger('submit.prevent')
    await flushPromises()

    expect(wrapper.find('[data-testid="create-qoder-site-global"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="create-qoder-site-cn"]').exists()).toBe(false)
    expect(createAccountMock).toHaveBeenCalledTimes(1)

    deferred.resolve({})
    await flushPromises()
    wrapper.unmount()
  })

  it('persists Qoder TLS fingerprint settings on manual create without OpenAI router', async () => {
    const wrapper = mountModal()
    await fillQoderManualForm(wrapper)

    await wrapper.get('[data-testid="create-qoder-tls-fingerprint-toggle"]').trigger('click')
    await wrapper.get('form#create-account-form').trigger('submit.prevent')
    await flushPromises()

    expect(createAccountMock).toHaveBeenCalledTimes(1)
    const payload = createAccountMock.mock.calls[0]?.[0]
    expect(payload.extra).toEqual({
      enable_tls_fingerprint: true
    })
  })

  it('persists Qoder model_mapping after explicit mapping edit on manual create', async () => {
    const wrapper = mountModal()
    await fillQoderManualForm(wrapper)

    const addMappingButton = wrapper.findAll('button').find(button => button.text().includes('admin.accounts.addMapping'))
    expect(addMappingButton).toBeTruthy()
    await addMappingButton!.trigger('click')

    const requestInput = wrapper.findAll('input').find(input => input.attributes('placeholder') === 'admin.accounts.requestModel')
    const targetInput = wrapper.findAll('input').find(input => input.attributes('placeholder') === 'admin.accounts.actualModel')
    expect(requestInput).toBeTruthy()
    expect(targetInput).toBeTruthy()
    await requestInput!.setValue('glm-5.2')
    await targetInput!.setValue('gm51model')

    await wrapper.get('form#create-account-form').trigger('submit.prevent')
    await flushPromises()

    expect(createAccountMock).toHaveBeenCalledTimes(1)
    const payload = createAccountMock.mock.calls[0]?.[0]
    expect(payload.credentials.model_mapping).toEqual({ 'glm-5.2': 'gm51model' })
    expect(payload.credentials.model_whitelist).toEqual([])
  })

  it('continues Qoder OAuth polling after a transient failure', async () => {
    vi.useFakeTimers()
    generateQoderAuthUrlMock.mockResolvedValueOnce({
      auth_url: 'https://qoder.com.cn/device',
      session_id: 'session-id',
      state: 'state-value',
      expires_in: 600,
      interval: 2
    })
    pollQoderAuthMock
      .mockRejectedValueOnce({ status: 503, message: 'service unavailable' })
      .mockResolvedValueOnce({
        status: 'completed',
        token_info: {
          security_oauth_token: 'cn-token',
          machine_id: 'cn-machine',
          uid: 'cn-user'
        }
      })
    const wrapper = mountModal()
    await openQoderOAuthStep(wrapper)

    await wrapper.get('[data-testid="generate-qoder-auth-url"]').trigger('click')
    await flushPromises()

    expect(pollQoderAuthMock).toHaveBeenCalledTimes(1)
    expect(showErrorMock).not.toHaveBeenCalled()

    await vi.advanceTimersByTimeAsync(2000)
    await flushPromises()

    expect(pollQoderAuthMock).toHaveBeenCalledTimes(2)
    expect(createAccountMock).toHaveBeenCalledTimes(1)
    expect(showErrorMock).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('stops Qoder OAuth polling after a terminal failure', async () => {
    vi.useFakeTimers()
    generateQoderAuthUrlMock.mockResolvedValueOnce({
      auth_url: 'https://qoder.com.cn/device',
      session_id: 'session-id',
      state: 'state-value',
      expires_in: 600,
      interval: 2
    })
    pollQoderAuthMock.mockRejectedValueOnce({
      status: 400,
      message: 'authorization session expired'
    })
    const popup = {
      opener: window,
      close: vi.fn(),
      focus: vi.fn(),
      location: { href: 'about:blank' }
    }
    vi.mocked(window.open).mockReturnValueOnce(popup as unknown as Window)
    const wrapper = mountModal()
    await openQoderOAuthStep(wrapper)

    await wrapper.get('[data-testid="generate-qoder-auth-url"]').trigger('click')
    await flushPromises()
    await vi.advanceTimersByTimeAsync(10_000)

    expect(pollQoderAuthMock).toHaveBeenCalledTimes(1)
    expect(createAccountMock).not.toHaveBeenCalled()
    expect(showErrorMock).not.toHaveBeenCalled()
    expect(popup.close).toHaveBeenCalledTimes(1)
    wrapper.unmount()
  })

  it('stops Qoder OAuth polling after the automatic polling deadline', async () => {
    vi.useFakeTimers()
    generateQoderAuthUrlMock.mockResolvedValueOnce({
      auth_url: 'https://qoder.com.cn/device',
      session_id: 'session-id',
      state: 'state-value',
      expires_in: 600,
      interval: 601
    })
    pollQoderAuthMock.mockResolvedValue({ status: 'pending' })
    const popup = {
      opener: window,
      close: vi.fn(),
      focus: vi.fn(),
      location: { href: 'about:blank' }
    }
    vi.mocked(window.open).mockReturnValueOnce(popup as unknown as Window)
    const wrapper = mountModal()
    await openQoderOAuthStep(wrapper)

    await wrapper.get('[data-testid="generate-qoder-auth-url"]').trigger('click')
    await flushPromises()
    expect(pollQoderAuthMock).toHaveBeenCalledTimes(1)

    await vi.advanceTimersByTimeAsync(600_001)
    await vi.advanceTimersByTimeAsync(601_000)

    expect(pollQoderAuthMock).toHaveBeenCalledTimes(1)
    expect(createAccountMock).not.toHaveBeenCalled()
    expect(popup.close).toHaveBeenCalledTimes(1)
    wrapper.unmount()
  })

  it('stops Qoder polling and closes its popup when unmounted', async () => {
    vi.useFakeTimers()
    generateQoderAuthUrlMock.mockResolvedValueOnce({
      auth_url: 'https://qoder.com.cn/device',
      session_id: 'session-id',
      state: 'state-value',
      expires_in: 600,
      interval: 2
    })
    pollQoderAuthMock.mockResolvedValue({ status: 'pending' })
    const popup = {
      opener: window,
      close: vi.fn(),
      focus: vi.fn(),
      location: { href: 'about:blank' }
    }
    vi.mocked(window.open).mockReturnValueOnce(popup as unknown as Window)
    const wrapper = mountModal()
    await openQoderOAuthStep(wrapper)

    await wrapper.get('[data-testid="generate-qoder-auth-url"]').trigger('click')
    await flushPromises()
    expect(pollQoderAuthMock).toHaveBeenCalledTimes(1)

    wrapper.unmount()
    await vi.advanceTimersByTimeAsync(10_000)

    expect(popup.close).toHaveBeenCalledTimes(1)
    expect(pollQoderAuthMock).toHaveBeenCalledTimes(1)
  })

  it('discards a generated OAuth session after returning to the account form', async () => {
    const deferred = createDeferred<{
      auth_url: string
      session_id: string
      state: string
      expires_in: number
      interval: number
    }>()
    generateQoderAuthUrlMock.mockReturnValueOnce(deferred.promise)

    const wrapper = mountModal()
    await openQoderOAuthStep(wrapper)
    await wrapper.get('[data-testid="generate-qoder-auth-url"]').trigger('click')
    expect(generateQoderAuthUrlMock).toHaveBeenCalledWith({ site: 'cn' })

    await findButtonByText(wrapper, 'common.back').trigger('click')
    deferred.resolve({
      auth_url: 'https://qoder.com.cn/old-session',
      session_id: 'old-session',
      state: 'old-state',
      expires_in: 600,
      interval: 2
    })
    await flushPromises()

    expect(pollQoderAuthMock).not.toHaveBeenCalled()
    expect(createAccountMock).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('does not create an account when an exchange completes after returning', async () => {
    generateQoderAuthUrlMock.mockResolvedValueOnce({
      auth_url: 'https://qoder.com.cn/device',
      session_id: 'global-session',
      state: 'state-value',
      expires_in: 600,
      interval: 2
    })
    const deferred = createDeferred<{
      security_oauth_token: string
      machine_id: string
      site: 'cn'
    }>()
    exchangeQoderCodeMock.mockReturnValueOnce(deferred.promise)

    const wrapper = mountModal()
    await openQoderOAuthStep(wrapper)
    await wrapper.get('[data-testid="generate-qoder-auth-url"]').trigger('click')
    await flushPromises()
    await findButtonByText(wrapper, 'admin.accounts.oauth.completeAuth').trigger('click')
    expect(exchangeQoderCodeMock).toHaveBeenCalledTimes(1)

    await findButtonByText(wrapper, 'common.back').trigger('click')
    deferred.resolve({
      security_oauth_token: 'old-cn-token',
      machine_id: 'old-machine',
      site: 'cn'
    })
    await flushPromises()

    expect(createAccountMock).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('keeps navigation and duplicate exchange disabled while account creation is pending', async () => {
    generateQoderAuthUrlMock.mockResolvedValueOnce({
      auth_url: 'https://qoder.com.cn/device',
      session_id: 'global-session',
      state: 'state-value',
      expires_in: 600,
      interval: 2
    })
    exchangeQoderCodeMock.mockResolvedValue({
      security_oauth_token: 'cn-token',
      machine_id: 'cn-machine',
      site: 'cn'
    })
    const createDeferredRequest = createDeferred<Record<string, never>>()
    createAccountMock.mockReturnValueOnce(createDeferredRequest.promise)

    const wrapper = mountModal()
    await openQoderOAuthStep(wrapper)
    await wrapper.get('[data-testid="generate-qoder-auth-url"]').trigger('click')
    await flushPromises()
    await findButtonByText(wrapper, 'admin.accounts.oauth.completeAuth').trigger('click')
    await flushPromises()

    expect(createAccountMock).toHaveBeenCalledTimes(1)
    const backButton = findButtonByText(wrapper, 'common.back')
    expect(backButton.attributes('disabled')).toBeDefined()
    await backButton.trigger('click')
    expect(wrapper.find('[data-testid="create-qoder-site-cn"]').exists()).toBe(false)

    const exchangeButton = findButtonByText(wrapper, 'admin.accounts.oauth.verifying')
    expect(exchangeButton.attributes('disabled')).toBeDefined()
    await exchangeButton.trigger('click')
    expect(exchangeQoderCodeMock).toHaveBeenCalledTimes(1)
    expect(createAccountMock).toHaveBeenCalledTimes(1)

    createDeferredRequest.resolve({})
    await flushPromises()
    wrapper.unmount()
  })

  it('ignores dialog close events until a pending Qoder account creation succeeds', async () => {
    generateQoderAuthUrlMock.mockResolvedValueOnce({
      auth_url: 'https://qoder.com.cn/device',
      session_id: 'global-session',
      state: 'state-value',
      expires_in: 600,
      interval: 2
    })
    exchangeQoderCodeMock.mockResolvedValueOnce({
      security_oauth_token: 'cn-token',
      machine_id: 'cn-machine',
      site: 'cn'
    })
    const createDeferredRequest = createDeferred<Record<string, never>>()
    createAccountMock.mockReturnValueOnce(createDeferredRequest.promise)

    const wrapper = mountModal()
    await openQoderOAuthStep(wrapper)
    await wrapper.get('[data-testid="generate-qoder-auth-url"]').trigger('click')
    await flushPromises()
    await findButtonByText(wrapper, 'admin.accounts.oauth.completeAuth').trigger('click')
    await flushPromises()

    expect(createAccountMock).toHaveBeenCalledTimes(1)
    expect(wrapper.getComponent(OAuthAuthorizationFlowStub).props('loading')).toBe(true)
    await wrapper.get('[data-testid="generate-qoder-auth-url"]').trigger('click')
    await flushPromises()
    expect(generateQoderAuthUrlMock).toHaveBeenCalledTimes(1)
    wrapper.findAllComponents(BaseDialogStub)[0]!.vm.$emit('close')
    await flushPromises()
    expect(wrapper.emitted('close')).toBeUndefined()
    expect(wrapper.emitted('created')).toBeUndefined()

    createDeferredRequest.resolve({})
    await flushPromises()
    expect(wrapper.emitted('created')).toHaveLength(1)
    expect(wrapper.emitted('close')).toHaveLength(1)
    wrapper.unmount()
  })

  it('keeps the current OAuth session retryable after account creation fails', async () => {
    generateQoderAuthUrlMock.mockResolvedValueOnce({
      auth_url: 'https://qoder.com.cn/device',
      session_id: 'global-session',
      state: 'state-value',
      expires_in: 600,
      interval: 2
    })
    exchangeQoderCodeMock.mockResolvedValue({
      security_oauth_token: 'cn-token',
      machine_id: 'cn-machine',
      site: 'cn'
    })
    createAccountMock
      .mockRejectedValueOnce({ status: 400, message: 'invalid Qoder CN credentials' })
      .mockResolvedValueOnce({})

    const wrapper = mountModal()
    await openQoderOAuthStep(wrapper)
    await wrapper.get('[data-testid="generate-qoder-auth-url"]').trigger('click')
    await flushPromises()
    await findButtonByText(wrapper, 'admin.accounts.oauth.completeAuth').trigger('click')
    await flushPromises()

    expect(createAccountMock).toHaveBeenCalledTimes(1)
    expect(wrapper.emitted('created')).toBeUndefined()
    expect(wrapper.get('[data-testid="qoder-oauth-session"]').text()).toBe('global-session')
    expect(showErrorMock).toHaveBeenCalledWith('invalid Qoder CN credentials')

    await findButtonByText(wrapper, 'admin.accounts.oauth.completeAuth').trigger('click')
    await flushPromises()
    expect(exchangeQoderCodeMock).toHaveBeenCalledTimes(2)
    expect(createAccountMock).toHaveBeenCalledTimes(2)
    expect(wrapper.emitted('created')).toHaveLength(1)
    wrapper.unmount()
  })

  it('keeps the current OAuth session retryable after local creation validation fails', async () => {
    generateQoderAuthUrlMock.mockResolvedValueOnce({
      auth_url: 'https://qoder.com.cn/device',
      session_id: 'global-session',
      state: 'state-value',
      expires_in: 600,
      interval: 2
    })
    exchangeQoderCodeMock.mockResolvedValue({
      security_oauth_token: 'cn-token',
      machine_id: 'cn-machine',
      site: 'cn'
    })

    const wrapper = mountModal()
    await openQoderOAuthStep(wrapper)
    const setupState = (wrapper.vm as any).$?.setupState
    setupState.tempUnschedEnabled = true
    setupState.tempUnschedRules = []
    await wrapper.get('[data-testid="generate-qoder-auth-url"]').trigger('click')
    await flushPromises()
    await findButtonByText(wrapper, 'admin.accounts.oauth.completeAuth').trigger('click')
    await flushPromises()

    expect(exchangeQoderCodeMock).toHaveBeenCalledTimes(1)
    expect(createAccountMock).not.toHaveBeenCalled()
    expect(wrapper.emitted('created')).toBeUndefined()

    setupState.tempUnschedEnabled = false
    await findButtonByText(wrapper, 'admin.accounts.oauth.completeAuth').trigger('click')
    await flushPromises()
    expect(exchangeQoderCodeMock).toHaveBeenCalledTimes(2)
    expect(createAccountMock).toHaveBeenCalledTimes(1)
    expect(wrapper.emitted('created')).toHaveLength(1)
    wrapper.unmount()
  })

  it('does not let an old generate request close a reused popup owned by a new flow', async () => {
    const firstGenerate = createDeferred<{
      auth_url: string
      session_id: string
      state: string
      expires_in: number
      interval: number
    }>()
    const secondGenerate = createDeferred<{
      auth_url: string
      session_id: string
      state: string
      expires_in: number
      interval: number
    }>()
    const thirdGenerate = createDeferred<{
      auth_url: string
      session_id: string
      state: string
      expires_in: number
      interval: number
    }>()
    generateQoderAuthUrlMock
      .mockReturnValueOnce(firstGenerate.promise)
      .mockReturnValueOnce(secondGenerate.promise)
      .mockReturnValueOnce(thirdGenerate.promise)

    const reusedPopup = {
      close: vi.fn(),
      focus: vi.fn(),
      location: { href: 'about:blank' }
    }
    const replacementPopup = {
      close: vi.fn(),
      focus: vi.fn(),
      location: { href: 'about:blank' }
    }
    vi.mocked(window.open)
      .mockReturnValueOnce(reusedPopup as unknown as Window)
      .mockReturnValueOnce(reusedPopup as unknown as Window)
      .mockReturnValueOnce(replacementPopup as unknown as Window)

    const wrapper = mountModal()
    await openQoderOAuthStep(wrapper)
    await wrapper.get('[data-testid="generate-qoder-auth-url"]').trigger('click')
    await findButtonByText(wrapper, 'common.back').trigger('click')
    expect(reusedPopup.close).toHaveBeenCalledTimes(1)

    await wrapper.get('form#create-account-form').trigger('submit.prevent')
    await wrapper.get('[data-testid="generate-qoder-auth-url"]').trigger('click')
    firstGenerate.resolve({
      auth_url: 'https://qoder.com.cn/old-device',
      session_id: 'old-session',
      state: 'old-state',
      expires_in: 600,
      interval: 2
    })
    await flushPromises()
    expect(reusedPopup.close).toHaveBeenCalledTimes(1)

    secondGenerate.resolve({
      auth_url: 'https://qoder.com.cn/current-device',
      session_id: 'current-session',
      state: 'current-state',
      expires_in: 600,
      interval: 2
    })
    await flushPromises()
    expect(reusedPopup.location.href).toBe('https://qoder.com.cn/current-device')

    await wrapper.get('[data-testid="generate-qoder-auth-url"]').trigger('click')
    expect(reusedPopup.close).toHaveBeenCalledTimes(2)
    thirdGenerate.resolve({
      auth_url: 'https://qoder.com.cn/replacement-device',
      session_id: 'replacement-session',
      state: 'replacement-state',
      expires_in: 600,
      interval: 2
    })
    await flushPromises()
    expect(replacementPopup.location.href).toBe('https://qoder.com.cn/replacement-device')

    wrapper.findAllComponents(BaseDialogStub)[0]!.vm.$emit('close')
    await flushPromises()
    expect(replacementPopup.close).toHaveBeenCalledTimes(1)
    wrapper.unmount()
  })

  it('detaches the popup opener before navigating to Qoder CN', async () => {
    generateQoderAuthUrlMock.mockResolvedValueOnce({
      auth_url: 'https://qoder.com.cn/device/selectAccounts',
      session_id: 'session-id',
      state: 'state-value',
      expires_in: 600,
      interval: 2
    })

    let openerAtNavigation: Window | null | undefined
    let navigatedTo = 'about:blank'
    const popup = {
      opener: window as Window | null,
      close: vi.fn(),
      focus: vi.fn(),
      location: {} as Location
    }
    Object.defineProperty(popup.location, 'href', {
      configurable: true,
      get: () => navigatedTo,
      set: (value: string) => {
        openerAtNavigation = popup.opener
        navigatedTo = value
      }
    })
    vi.mocked(window.open).mockReturnValueOnce(popup as unknown as Window)

    const wrapper = mountModal()
    await openQoderOAuthStep(wrapper)
    await wrapper.get('[data-testid="generate-qoder-auth-url"]').trigger('click')
    await flushPromises()

    expect(openerAtNavigation).toBeNull()
    expect(navigatedTo).toBe('https://qoder.com.cn/device/selectAccounts')
    wrapper.unmount()
  })
})
