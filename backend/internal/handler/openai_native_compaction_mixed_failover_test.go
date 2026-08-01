package handler

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/server/middleware"
	"github.com/TokenFlux/TokenRouter/internal/service"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type nativeCompactionQuarantineCall struct {
	key             service.OpenAINativeCompactionCapabilityKey
	until           *time.Time
	semanticFailure string
}

type nativeCompactionCapabilityRepoStub struct {
	service.OpenAINativeCompactionCapabilityRepository

	mu    sync.Mutex
	calls []nativeCompactionQuarantineCall
}

func (r *nativeCompactionCapabilityRepoStub) SetQuarantine(
	_ context.Context,
	key service.OpenAINativeCompactionCapabilityKey,
	until *time.Time,
	semanticFailure string,
) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	call := nativeCompactionQuarantineCall{key: key, semanticFailure: semanticFailure}
	if until != nil {
		untilCopy := *until
		call.until = &untilCopy
	}
	r.calls = append(r.calls, call)
	return true, nil
}

func (r *nativeCompactionCapabilityRepoStub) snapshot() []nativeCompactionQuarantineCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]nativeCompactionQuarantineCall(nil), r.calls...)
}

type nativeCompactionAttemptRepoStub struct {
	mu   sync.Mutex
	rows map[service.AttemptID]service.UpstreamAttemptAttribution
}

func newNativeCompactionAttemptRepoStub() *nativeCompactionAttemptRepoStub {
	return &nativeCompactionAttemptRepoStub{rows: make(map[service.AttemptID]service.UpstreamAttemptAttribution)}
}

func (r *nativeCompactionAttemptRepoStub) Upsert(
	_ context.Context,
	attribution service.UpstreamAttemptAttribution,
) (service.UpstreamAttemptAttribution, error) {
	if err := attribution.Validate(); err != nil {
		return service.UpstreamAttemptAttribution{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if current, ok := r.rows[attribution.AttemptID]; ok && current.StateVersion >= attribution.StateVersion {
		return current, nil
	}
	r.rows[attribution.AttemptID] = attribution
	return attribution, nil
}

func (r *nativeCompactionAttemptRepoStub) Get(
	_ context.Context,
	attemptID service.AttemptID,
) (*service.UpstreamAttemptAttribution, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	row, ok := r.rows[attemptID]
	if !ok {
		return nil, nil
	}
	return &row, nil
}

func (r *nativeCompactionAttemptRepoStub) terminalRows() []service.UpstreamAttemptAttribution {
	r.mu.Lock()
	defer r.mu.Unlock()
	rows := make([]service.UpstreamAttemptAttribution, 0, len(r.rows))
	for _, row := range r.rows {
		if row.State == service.UpstreamAttemptStateTerminal {
			rows = append(rows, row)
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].AccountID < rows[j].AccountID })
	return rows
}

type nativeCompactionUsageLogRepoStub struct {
	service.UsageLogRepository

	mu   sync.Mutex
	logs []service.UsageLog
}

func (r *nativeCompactionUsageLogRepoStub) Create(_ context.Context, usageLog *service.UsageLog) (bool, error) {
	if usageLog == nil {
		return false, nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.logs = append(r.logs, *usageLog)
	return true, nil
}

func (r *nativeCompactionUsageLogRepoStub) snapshot() []service.UsageLog {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]service.UsageLog(nil), r.logs...)
}

func nativeCompactionMixedAccount(t *testing.T, id int64, accountType string, priority int) service.Account {
	t.Helper()
	credentials := map[string]any{}
	switch accountType {
	case service.AccountTypeAPIKey:
		credentials["api_key"] = "fixture-api-key"
		credentials["base_url"] = "https://chatgpt.com/backend-api/codex/responses"
		credentials["model_mapping"] = map[string]any{"gpt-5.1": "gpt-5.4"}
	case service.AccountTypeOAuth:
		credentials["access_token"] = "fixture-oauth-token"
	default:
		t.Fatalf("unsupported fixture account type: %s", accountType)
	}
	account := service.Account{
		ID:          id,
		Name:        "native-compaction-" + accountType,
		Platform:    service.PlatformOpenAI,
		Type:        accountType,
		Status:      service.StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Priority:    priority,
		Credentials: credentials,
		Extra:       map[string]any{"openai_responses_supported": true},
	}
	key, err := service.ResolveOpenAINativeCompactionCapabilityKey(&account, "gpt-5.4")
	require.NoError(t, err)
	now := time.Now().UTC()
	source := service.OpenAINativeCompactionCapabilitySourceProbe
	var checkedAt *time.Time
	if accountType == service.AccountTypeOAuth {
		source = service.OpenAINativeCompactionCapabilitySourceTrustedOfficial
	} else {
		checkedAt = &now
	}
	account.OpenAINativeCompactionCapabilities = []service.OpenAINativeCompactionCapability{{
		Key:       key,
		Supported: true,
		Mode:      service.OpenAINativeCompactionCapabilityModeAuto,
		Source:    source,
		CheckedAt: checkedAt,
	}}
	return account
}

func nativeCompactionIncompatibleAccount(t *testing.T, id int64, priority int) service.Account {
	t.Helper()
	account := nativeCompactionMixedAccount(t, id, service.AccountTypeAPIKey, priority)
	account.Name = "native-compaction-incompatible-domain"
	account.Credentials["base_url"] = "https://incompatible.example.test/v1"
	key, err := service.ResolveOpenAINativeCompactionCapabilityKey(&account, "gpt-5.4")
	require.NoError(t, err)
	now := time.Now().UTC()
	account.OpenAINativeCompactionCapabilities = []service.OpenAINativeCompactionCapability{{
		Key:       key,
		Supported: true,
		Mode:      service.OpenAINativeCompactionCapabilityModeAuto,
		Source:    service.OpenAINativeCompactionCapabilitySourceProbe,
		CheckedAt: &now,
	}}
	return account
}

func newNativeCompactionMixedFailoverHandler(
	t *testing.T,
	cfg *config.Config,
	accountRepo service.AccountRepository,
	upstream service.HTTPUpstream,
	capabilityRepo service.OpenAINativeCompactionCapabilityRepository,
	attemptRepo service.UpstreamAttemptAttributionRepository,
	usageLogRepo service.UsageLogRepository,
	gatewayCaches ...service.GatewayCache,
) *OpenAIGatewayHandler {
	t.Helper()
	var gatewayCache service.GatewayCache
	if len(gatewayCaches) > 0 {
		gatewayCache = gatewayCaches[0]
	}
	rateLimitService := service.NewRateLimitService(accountRepo, nil, cfg, nil, nil)
	billingCacheService := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
	t.Cleanup(billingCacheService.Stop)
	gatewayService := service.ProvideOpenAIGatewayService(
		accountRepo,
		usageLogRepo,
		nil,
		nil,
		nil,
		nil,
		gatewayCache,
		cfg,
		nil,
		nil,
		service.NewBillingService(cfg, nil),
		rateLimitService,
		billingCacheService,
		upstream,
		nil,
		&service.DeferredService{},
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		capabilityRepo,
		nil,
		nil,
		attemptRepo,
	)
	cache := &concurrencyCacheMock{
		acquireUserSlotFn:    func(context.Context, int64, int, string) (bool, error) { return true, nil },
		acquireAccountSlotFn: func(context.Context, int64, int, string) (bool, error) { return true, nil },
	}
	return &OpenAIGatewayHandler{
		gatewayService:      gatewayService,
		billingCacheService: billingCacheService,
		apiKeyService:       &service.APIKeyService{},
		concurrencyHelper:   NewConcurrencyHelper(service.NewConcurrencyService(cache), SSEPingFormatNone, time.Second),
		maxAccountSwitches:  3,
		cfg:                 cfg,
	}
}

func TestOpenAIResponsesNativeCompactionMixedPoolSemanticFailover(t *testing.T) {
	gin.SetMode(gin.TestMode)
	testCases := []struct {
		name       string
		firstType  string
		secondType string
	}{
		{name: "api_key_to_oauth", firstType: service.AccountTypeAPIKey, secondType: service.AccountTypeOAuth},
		{name: "oauth_to_api_key", firstType: service.AccountTypeOAuth, secondType: service.AccountTypeAPIKey},
	}

	for index, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			firstID := int64(9950 + index*10)
			incompatibleID := firstID + 1
			secondID := firstID + 2
			firstAccount := nativeCompactionMixedAccount(t, firstID, testCase.firstType, 1)
			incompatibleAccount := nativeCompactionIncompatibleAccount(t, incompatibleID, 2)
			secondAccount := nativeCompactionMixedAccount(t, secondID, testCase.secondType, 3)
			firstDomain, err := service.ResolveOpenAINativeCompatibilityDomainForRequest(t.Context(), &firstAccount, "gpt-5.1")
			require.NoError(t, err)
			secondDomain, err := service.ResolveOpenAINativeCompatibilityDomainForRequest(t.Context(), &secondAccount, "gpt-5.1")
			require.NoError(t, err)
			require.True(t, firstDomain.CompatibleWith(secondDomain), "fixture accounts must share an explicit compatibility domain")

			incompatibleDomain, err := service.ResolveOpenAINativeCompatibilityDomainForRequest(t.Context(), &incompatibleAccount, "gpt-5.1")
			require.NoError(t, err)
			require.False(t, firstDomain.CompatibleWith(incompatibleDomain))

			accountRepo := &openAIWSFailoverHandlerAccountRepoStub{accounts: []service.Account{firstAccount, incompatibleAccount, secondAccount}}
			capabilityRepo := &nativeCompactionCapabilityRepoStub{}
			attemptRepo := newNativeCompactionAttemptRepoStub()
			usageLogRepo := &nativeCompactionUsageLogRepoStub{}
			upstream := &openAIHandlerHTTPUpstreamStub{do: func(req *http.Request, accountID int64) (*http.Response, error) {
				requestBody, readErr := io.ReadAll(req.Body)
				require.NoError(t, readErr)
				require.True(t, gjson.GetBytes(requestBody, "stream").Bool())
				require.Equal(t, "gpt-5.4", gjson.GetBytes(requestBody, "model").String())
				input := gjson.GetBytes(requestBody, "input").Array()
				require.NotEmpty(t, input)
				require.Equal(t, "compaction_trigger", input[len(input)-1].Get("type").String())
				require.Contains(t, strings.ToLower(req.Header.Get("x-codex-beta-features")), "remote_compaction_v2")

				body := strings.Join([]string{
					`event: response.output_text.delta`,
					`data: {"type":"response.output_text.delta","delta":"FIRST_ACCOUNT_MUST_NOT_LEAK"}`,
					``,
					`event: response.completed`,
					`data: {"type":"response.completed","response":{"id":"resp_invalid_first","status":"completed","output":[],"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}}}`,
					``,
				}, "\n")
				requestID := "req-invalid-first"
				if accountID == secondID {
					body = strings.Join([]string{
						`event: response.created`,
						`data: {"type":"response.created","response":{"id":"resp_valid_second","status":"in_progress"}}`,
						``,
						`event: response.output_item.done`,
						`data: {"type":"response.output_item.done","output_index":0,"item":{"id":"cmp_valid_second","type":"compaction","status":"completed","encrypted_content":"fixture-encrypted-state"}}`,
						``,
						`event: response.completed`,
						`data: {"type":"response.completed","response":{"id":"resp_valid_second","status":"completed","output":[],"usage":{"input_tokens":3,"output_tokens":1,"total_tokens":4}}}`,
						``,
					}, "\n")
					requestID = "req-valid-second"
				}
				return &http.Response{
					StatusCode: http.StatusOK,
					Header: http.Header{
						"Content-Type": []string{"text/event-stream"},
						"X-Request-Id": []string{requestID},
					},
					Body: io.NopCloser(strings.NewReader(body)),
				}, nil
			}}

			cfg := newOpenAIHTTPFailoverTestConfig()
			cfg.Gateway.OpenAINativeCompaction.MaxProcessStagedBytes = 8 << 20
			cfg.Gateway.OpenAINativeCompaction.MaxRequestCumulativeBytes = 8 << 20
			cfg.Gateway.OpenAINativeCompaction.SemanticQuarantineSeconds = 60
			handler := newNativeCompactionMixedFailoverHandler(t, cfg, accountRepo, upstream, capabilityRepo, attemptRepo, usageLogRepo)

			groupID := int64(4210 + index)
			body := []byte(`{"model":"gpt-5.1","stream":true,"input":[{"type":"input_text","text":"fixture"},{"type":"compaction_trigger"}]}`)
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/openai/v1/responses", bytes.NewReader(body))
			c.Request.Header.Set("Content-Type", "application/json")
			c.Request.Header.Set("User-Agent", "codex_cli_rs/fixture")
			c.Request.Header.Set("x-codex-beta-features", "remote_compaction_v2")
			bindOpenAIHTTPFailoverTestAuth(c, groupID)

			handler.Responses(c)

			require.Equal(t, []int64{firstID, secondID}, upstream.AccountIDs())
			wire := recorder.Body.String()
			require.NotContains(t, wire, "FIRST_ACCOUNT_MUST_NOT_LEAK")
			require.NotContains(t, wire, "resp_invalid_first")
			require.Contains(t, wire, "resp_valid_second")
			require.Equal(t, 1, strings.Count(wire, `"type":"compaction"`))
			require.Less(t, strings.Index(wire, `"type":"response.created"`), strings.Index(wire, `"type":"response.output_item.done"`))
			require.Less(t, strings.Index(wire, `"type":"response.output_item.done"`), strings.Index(wire, `"type":"response.completed"`))

			quarantineCalls := capabilityRepo.snapshot()
			require.Len(t, quarantineCalls, 1)
			require.Equal(t, firstID, quarantineCalls[0].key.AccountID)
			require.Equal(t, firstDomain.UpstreamFingerprint, quarantineCalls[0].key.UpstreamFingerprint)
			require.Equal(t, "gpt-5.4", quarantineCalls[0].key.EffectiveModel)
			require.Equal(t, string(service.OpenAINativeCompactionZeroCompaction), quarantineCalls[0].semanticFailure)
			require.NotNil(t, quarantineCalls[0].until)
			require.True(t, quarantineCalls[0].until.After(time.Now()))

			terminalRows := attemptRepo.terminalRows()
			require.Len(t, terminalRows, 2)
			terminalByAccount := map[int64]service.UpstreamAttemptAttribution{}
			for _, row := range terminalRows {
				terminalByAccount[row.AccountID] = row
			}
			firstTerminal := terminalByAccount[firstID]
			require.Equal(t, string(service.OpenAINativeCompactionZeroCompaction), firstTerminal.Semantic.Outcome)
			require.False(t, firstTerminal.DeliveryCommitted)
			require.True(t, firstTerminal.SafeToFailover)
			require.True(t, firstTerminal.Usage.Observed)
			secondTerminal := terminalByAccount[secondID]
			require.Equal(t, string(service.OpenAINativeCompactionValid), secondTerminal.Semantic.Outcome)
			require.Equal(t, int64(1), secondTerminal.Semantic.CompactionItemCount)
			require.True(t, secondTerminal.DeliveryCommitted)
			require.False(t, secondTerminal.SafeToFailover)
			require.True(t, secondTerminal.Usage.Observed)

			usageLogs := usageLogRepo.snapshot()
			require.Len(t, usageLogs, 1, "only the committed semantic winner may create customer usage")
			require.Equal(t, secondID, usageLogs[0].AccountID)
			require.Equal(t, "req-valid-second", usageLogs[0].RequestID)
			require.Equal(t, service.UpstreamResponseID("resp_valid_second"), secondTerminal.UpstreamResponseID)

			logState, ok := getOpenAINativeCompactionLogState(c)
			require.True(t, ok)
			require.True(t, logState.SettlementAllowed)
			require.Equal(t, 1, logState.Telemetry.FailoverCount)
			require.Equal(t, testCase.secondType, logState.Telemetry.FinalAccountType)
			require.Equal(t, service.OpenAINativeCompactionValid, logState.Telemetry.Outcome)
			require.Equal(t, 1, logState.Telemetry.CompactionItemCount)
			require.True(t, logState.Telemetry.SemanticOutputCommitted)
			require.True(t, logState.Telemetry.AttributionPersisted)
		})
	}
}

func TestOpenAIResponsesNativeCompactionAllSemanticFailuresFallBackToLegacyCompact(t *testing.T) {
	gin.SetMode(gin.TestMode)
	first := nativeCompactionMixedAccount(t, 9960, service.AccountTypeAPIKey, 1)
	first.Extra["openai_compact_supported"] = true
	unknown := service.Account{
		ID: 9961, Name: "responses-or-cc-only-unknown-native", Platform: service.PlatformOpenAI,
		Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true,
		Concurrency: 1, Priority: 2,
		Credentials: map[string]any{"api_key": "unknown", "base_url": "https://chatgpt.com/backend-api/codex/responses"},
		Extra:       map[string]any{"openai_responses_supported": true},
	}
	second := nativeCompactionMixedAccount(t, 9962, service.AccountTypeAPIKey, 3)
	second.Extra["openai_compact_supported"] = true
	accountRepo := &openAIWSFailoverHandlerAccountRepoStub{accounts: []service.Account{first, unknown, second}}
	capabilityRepo := &nativeCompactionCapabilityRepoStub{}
	attemptRepo := newNativeCompactionAttemptRepoStub()
	usageLogRepo := &nativeCompactionUsageLogRepoStub{}
	upstream := &openAIHandlerHTTPUpstreamStub{do: func(req *http.Request, accountID int64) (*http.Response, error) {
		if strings.HasSuffix(req.URL.Path, "/responses/compact") {
			requestBody, readErr := io.ReadAll(req.Body)
			require.NoError(t, readErr)
			require.False(t, gjson.GetBytes(requestBody, "stream").Exists())
			require.NotContains(t, strings.ToLower(req.Header.Get("x-codex-beta-features")), "remote_compaction_v2")
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body: io.NopCloser(strings.NewReader(
					`{"id":"resp_legacy_fallback","output":[{"id":"cmp_legacy_fallback","type":"compaction","status":"completed","encrypted_content":"legacy-state"}],"usage":{"input_tokens":3,"output_tokens":1,"total_tokens":4}}`,
				)),
			}, nil
		}
		body := strings.Join([]string{
			`event: response.output_text.delta`,
			`data: {"type":"response.output_text.delta","delta":"FAILED_ACCOUNT_MUST_NOT_LEAK"}`,
			``,
			`event: response.completed`,
			`data: {"type":"response.completed","response":{"id":"resp_invalid_` + strconv.FormatInt(accountID, 10) + `","status":"completed","output":[],"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}}}`,
			``,
		}, "\n")
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       io.NopCloser(strings.NewReader(body)),
		}, nil
	}}
	cfg := newOpenAIHTTPFailoverTestConfig()
	cfg.Gateway.OpenAINativeCompaction.MaxProcessStagedBytes = 8 << 20
	cfg.Gateway.OpenAINativeCompaction.MaxRequestCumulativeBytes = 8 << 20
	cfg.Gateway.OpenAINativeCompaction.SemanticQuarantineSeconds = 60
	h := newNativeCompactionMixedFailoverHandler(t, cfg, accountRepo, upstream, capabilityRepo, attemptRepo, usageLogRepo)

	body := []byte(`{"model":"gpt-5.1","stream":true,"input":[{"type":"compaction_trigger"}]}`)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/openai/v1/responses", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Request.Header.Set("x-codex-beta-features", "remote_compaction_v2")
	bindOpenAIHTTPFailoverTestAuth(c, 4219)

	h.Responses(c)

	require.Equal(t, []int64{first.ID, second.ID, first.ID}, upstream.AccountIDs(), "unknown native capability must be excluded before dispatch and legacy selection must start clean")
	wire := recorder.Body.String()
	require.NotContains(t, wire, "FAILED_ACCOUNT_MUST_NOT_LEAK")
	require.NotContains(t, wire, "resp_invalid_")
	require.Contains(t, recorder.Header().Get("Content-Type"), "text/event-stream")
	require.Contains(t, wire, "resp_legacy_fallback")
	require.Contains(t, wire, "cmp_legacy_fallback")
	require.Equal(t, 1, strings.Count(wire, `"type":"response.output_item.done"`))
	require.Equal(t, 1, strings.Count(wire, `"type":"response.completed"`))
	require.NotContains(t, wire, `"type":"response.failed"`)
	require.Len(t, capabilityRepo.snapshot(), 2)
	require.Len(t, attemptRepo.terminalRows(), 2)
	for _, row := range attemptRepo.terminalRows() {
		require.Equal(t, string(service.OpenAINativeCompactionZeroCompaction), row.Semantic.Outcome)
		require.False(t, row.DeliveryCommitted)
		require.True(t, row.SafeToFailover)
	}
	require.Eventually(t, func() bool { return len(usageLogRepo.snapshot()) == 1 }, time.Second, 10*time.Millisecond)
	require.Equal(t, first.ID, usageLogRepo.snapshot()[0].AccountID)
}

func TestOpenAIResponsesWebSocketNativeCompactionSemanticFailoverSkipsIncompatibleCandidate(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var upstreamIDsMu sync.Mutex
	var upstreamIDs []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, &coderws.AcceptOptions{CompressionMode: coderws.CompressionContextTakeover})
		if err != nil {
			return
		}
		defer func() { _ = conn.CloseNow() }()

		readCtx, cancelRead := context.WithTimeout(r.Context(), 3*time.Second)
		_, payload, readErr := conn.Read(readCtx)
		cancelRead()
		if readErr != nil {
			return
		}
		requestID := r.Header.Get("Authorization")
		upstreamIDsMu.Lock()
		upstreamIDs = append(upstreamIDs, requestID)
		upstreamIDsMu.Unlock()

		events := []string{
			`{"type":"response.output_text.delta","delta":"WS_FIRST_ACCOUNT_MUST_NOT_LEAK"}`,
			`{"type":"response.completed","response":{"id":"resp_ws_invalid_first","status":"completed","output":[],"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}}}`,
		}
		if strings.Contains(requestID, "sk-second") {
			events = []string{
				`{"type":"response.created","response":{"id":"resp_ws_valid_second","status":"in_progress"}}`,
				`{"type":"response.output_item.done","output_index":0,"item":{"id":"cmp_ws_valid_second","type":"compaction","status":"completed","encrypted_content":"fixture-encrypted-state"}}`,
				`{"type":"response.completed","response":{"id":"resp_ws_valid_second","status":"completed","output":[],"usage":{"input_tokens":3,"output_tokens":1,"total_tokens":4}}}`,
			}
		}
		_ = payload
		for _, event := range events {
			writeCtx, cancelWrite := context.WithTimeout(r.Context(), 3*time.Second)
			if writeErr := conn.Write(writeCtx, coderws.MessageText, []byte(event)); writeErr != nil {
				cancelWrite()
				return
			}
			cancelWrite()
		}
		_ = conn.Close(coderws.StatusNormalClosure, "done")
	}))
	defer upstream.Close()

	first := nativeCompactionMixedAccount(t, 9970, service.AccountTypeAPIKey, 1)
	incompatible := nativeCompactionIncompatibleAccount(t, 9971, 2)
	second := nativeCompactionMixedAccount(t, 9972, service.AccountTypeAPIKey, 3)
	first.Credentials["api_key"] = "sk-first"
	second.Credentials["api_key"] = "sk-second"
	for _, account := range []*service.Account{&first, &second} {
		account.Credentials["base_url"] = upstream.URL
		account.Extra["openai_apikey_responses_websockets_v2_enabled"] = true
		account.Extra["openai_apikey_responses_websockets_v2_mode"] = service.OpenAIWSIngressModePassthrough
		key, err := service.ResolveOpenAINativeCompactionCapabilityKey(account, "gpt-5.4")
		require.NoError(t, err)
		account.OpenAINativeCompactionCapabilities[0].Key = key
	}

	accountRepo := &openAIWSFailoverHandlerAccountRepoStub{accounts: []service.Account{first, incompatible, second}}
	capabilityRepo := &nativeCompactionCapabilityRepoStub{}
	attemptRepo := newNativeCompactionAttemptRepoStub()
	usageLogRepo := &nativeCompactionUsageLogRepoStub{}
	cfg := newOpenAIHTTPFailoverTestConfig()
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.ModeRouterV2Enabled = true
	cfg.Gateway.OpenAIWS.DialTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 3
	cfg.Gateway.OpenAINativeCompaction.MaxProcessStagedBytes = 8 << 20
	cfg.Gateway.OpenAINativeCompaction.MaxRequestCumulativeBytes = 8 << 20
	cfg.Gateway.OpenAINativeCompaction.SemanticQuarantineSeconds = 60
	h := newNativeCompactionMixedFailoverHandler(t, cfg, accountRepo, nil, capabilityRepo, attemptRepo, usageLogRepo)

	groupID := int64(4220)
	apiKey := &service.APIKey{
		ID:      1820,
		GroupID: &groupID,
		User:    &service.User{ID: 1720, Status: service.StatusActive},
		Group:   &service.Group{ID: groupID, Platform: service.PlatformOpenAI, Status: service.StatusActive},
	}
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyAPIKey), apiKey)
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: apiKey.User.ID, Concurrency: 1})
		c.Next()
	})
	router.GET("/openai/v1/responses", h.ResponsesWebSocket)
	handlerServer := httptest.NewServer(router)
	defer handlerServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := coderws.Dial(dialCtx, "ws"+strings.TrimPrefix(handlerServer.URL, "http")+"/openai/v1/responses", &coderws.DialOptions{
		HTTPHeader:      http.Header{"x-codex-beta-features": []string{"remote_compaction_v2"}},
		CompressionMode: coderws.CompressionContextTakeover,
	})
	cancelDial()
	require.NoError(t, err)
	defer func() { _ = clientConn.CloseNow() }()

	writeCtx, cancelWrite := context.WithTimeout(context.Background(), 3*time.Second)
	err = clientConn.Write(writeCtx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-5.1","stream":true,"input":[{"type":"input_text","text":"fixture"},{"type":"compaction_trigger"}]}`))
	cancelWrite()
	require.NoError(t, err)

	readCtx, cancelRead := context.WithTimeout(context.Background(), 8*time.Second)
	var received []byte
	var terminalEvent []byte
	for {
		_, event, readErr := clientConn.Read(readCtx)
		if readErr != nil {
			cancelRead()
			require.NoError(t, readErr)
		}
		received = append(received, event...)
		if gjson.GetBytes(event, "type").String() == "response.completed" {
			terminalEvent = append([]byte(nil), event...)
			break
		}
	}
	cancelRead()

	require.Contains(t, string(received), "resp_ws_valid_second")
	require.NotContains(t, string(received), "WS_FIRST_ACCOUNT_MUST_NOT_LEAK")
	require.NotContains(t, string(received), "resp_ws_invalid_first")
	require.Equal(t, 1, strings.Count(string(received), `"type":"compaction"`))
	require.Equal(t, "response.completed", gjson.GetBytes(terminalEvent, "type").String())

	upstreamIDsMu.Lock()
	gotUpstreamIDs := append([]string(nil), upstreamIDs...)
	upstreamIDsMu.Unlock()
	require.Equal(t, []string{"Bearer sk-first", "Bearer sk-second"}, gotUpstreamIDs)
	require.Len(t, capabilityRepo.snapshot(), 1)
	require.Equal(t, first.ID, capabilityRepo.snapshot()[0].key.AccountID)
	var terminalRows []service.UpstreamAttemptAttribution
	require.Eventually(t, func() bool {
		terminalRows = attemptRepo.terminalRows()
		return len(terminalRows) == 2
	}, time.Second, 10*time.Millisecond)
	rowsByAccount := map[int64]service.UpstreamAttemptAttribution{}
	for _, row := range terminalRows {
		rowsByAccount[row.AccountID] = row
	}
	require.Equal(t, string(service.OpenAINativeCompactionZeroCompaction), rowsByAccount[first.ID].Semantic.Outcome)
	require.True(t, rowsByAccount[first.ID].SafeToFailover)
	require.False(t, rowsByAccount[first.ID].DeliveryCommitted)
	require.Equal(t, string(service.OpenAINativeCompactionValid), rowsByAccount[second.ID].Semantic.Outcome)
	require.True(t, rowsByAccount[second.ID].DeliveryCommitted)
	require.Eventually(t, func() bool { return len(usageLogRepo.snapshot()) == 1 }, time.Second, 10*time.Millisecond)
	require.Equal(t, second.ID, usageLogRepo.snapshot()[0].AccountID)
}
