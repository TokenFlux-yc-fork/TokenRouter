package service

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/errors"
	"github.com/TokenFlux/TokenRouter/internal/pkg/httpclient"
)

// chatgptCodexModelsURL 是 ChatGPT Codex 模型清单接口，测试可临时替换为本地服务。
var chatgptCodexModelsURL = "https://chatgpt.com/backend-api/codex/models"

const codexModelsManifestBodyLimit int64 = 8 << 20

// CodexModelsManifest 保存上游原始清单及缓存元数据，避免网关耦合清单结构。
type CodexModelsManifest struct {
	Body        []byte
	ETag        string
	NotModified bool
}

// SelectCodexModelsAccount 选择凭据可用于 ChatGPT Codex 后端的可调度账号。
// OAuth 类型约束在账号完成快照补全和数据库复查后执行，避免轻量快照状态滞后；
// 账号选中后再通过 TokenProvider 验证实际凭据。
func (s *OpenAIGatewayService) SelectCodexModelsAccount(ctx context.Context, groupID *int64) (*Account, error) {
	excludedIDs := make(map[int64]struct{})
	selectionCtx := s.withOpenAIQuotaAutoPauseContext(ctx)
	for {
		account, selectErr := s.selectAccountForModelWithExclusionsMatching(
			selectionCtx,
			groupID,
			PlatformOpenAI,
			"",
			"",
			excludedIDs,
			false,
			0,
			"",
			func(account *Account) bool { return account != nil && account.IsOpenAIOAuth() },
		)
		if selectErr != nil {
			return nil, selectErr
		}

		credentialAccount, resolveErr := resolveCredentialAccount(ctx, s.accountRepo, account)
		if resolveErr == nil && credentialAccount != nil && credentialAccount.IsOpenAIOAuth() {
			accessToken, _, tokenErr := s.GetAccessToken(ctx, credentialAccount)
			if tokenErr == nil && strings.TrimSpace(accessToken) != "" {
				return account, nil
			}
			if isContextDoneError(ctx, tokenErr) {
				return nil, contextDoneError(ctx, tokenErr)
			}
		} else if isContextDoneError(ctx, resolveErr) {
			return nil, contextDoneError(ctx, resolveErr)
		}

		// 账号可能在调度与凭据校验之间发生变更；按最终补全后的凭据判定并继续调度。
		excludedIDs[account.ID] = struct{}{}
	}
}

// FetchCodexModelsManifest 使用账号的 OAuth 凭据获取实时 Codex 模型清单。
// 清单正文保持原样透传，以兼容 Codex 客户端持续演进的字段结构。
func (s *OpenAIGatewayService) FetchCodexModelsManifest(ctx context.Context, account *Account, clientVersion, ifNoneMatch string) (*CodexModelsManifest, error) {
	if account == nil {
		return nil, infraerrors.New(http.StatusInternalServerError, "OPENAI_CODEX_MODELS_ACCOUNT_REQUIRED", "account is required")
	}
	credAccount, err := resolveCredentialAccount(ctx, s.accountRepo, account)
	if err != nil {
		return nil, infraerrors.Newf(http.StatusInternalServerError, "OPENAI_CODEX_MODELS_CREDENTIALS_FAILED", "resolve credential account: %v", err)
	}
	if credAccount == nil || !credAccount.IsOpenAIOAuth() {
		return nil, infraerrors.New(http.StatusBadGateway, "OPENAI_CODEX_MODELS_ACCOUNT_UNSUPPORTED", "account does not support the Codex models backend")
	}
	accessToken, _, err := s.GetAccessToken(ctx, credAccount)
	if err != nil {
		return nil, infraerrors.Newf(http.StatusBadGateway, "OPENAI_CODEX_MODELS_TOKEN_FAILED", "get Codex backend access token: %v", err)
	}
	if accessToken = strings.TrimSpace(accessToken); accessToken == "" {
		return nil, infraerrors.New(http.StatusBadGateway, "OPENAI_CODEX_MODELS_TOKEN_MISSING", "account has no Codex backend access token")
	}

	clientVersion = strings.TrimSpace(clientVersion)
	if clientVersion == "" {
		clientVersion = openAICodexProbeVersion
	}
	requestURL := chatgptCodexModelsURL + "?client_version=" + url.QueryEscape(clientVersion)

	reqCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, infraerrors.Newf(http.StatusInternalServerError, "OPENAI_CODEX_MODELS_REQUEST_FAILED", "create Codex models request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Originator", "codex_cli_rs")
	req.Header.Set("Version", clientVersion)
	req.Header.Set("User-Agent", codexCLIUserAgent)
	if ifNoneMatch = strings.TrimSpace(ifNoneMatch); ifNoneMatch != "" {
		req.Header.Set("If-None-Match", ifNoneMatch)
	}
	setOpenAIChatGPTAccountHeaders(req.Header, credAccount)

	proxyURL := ""
	if account.ProxyID != nil && account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}
	client, err := httpclient.GetClient(httpclient.Options{
		ProxyURL:              proxyURL,
		Timeout:               15 * time.Second,
		ResponseHeaderTimeout: 10 * time.Second,
	})
	if err != nil {
		return nil, infraerrors.Newf(http.StatusInternalServerError, "OPENAI_CODEX_MODELS_PROXY_INVALID", "invalid proxy configuration: %v", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, infraerrors.Newf(http.StatusBadGateway, "OPENAI_CODEX_MODELS_UPSTREAM_FAILED", "Codex models manifest request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotModified {
		return &CodexModelsManifest{ETag: resp.Header.Get("ETag"), NotModified: true}, nil
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		message := strings.TrimSpace(string(body))
		if message == "" {
			message = resp.Status
		}
		return nil, infraerrors.Newf(http.StatusBadGateway, "OPENAI_CODEX_MODELS_UPSTREAM_FAILED", "Codex models manifest upstream error %d: %s", resp.StatusCode, message)
	}

	body, err := readUpstreamResponseBodyLimited(resp.Body, codexModelsManifestBodyLimit)
	if err != nil {
		return nil, infraerrors.Newf(http.StatusBadGateway, "OPENAI_CODEX_MODELS_UPSTREAM_FAILED", "read Codex models manifest response: %v", err)
	}
	return &CodexModelsManifest{Body: body, ETag: resp.Header.Get("ETag")}, nil
}
