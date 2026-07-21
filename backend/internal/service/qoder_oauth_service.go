package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/errors"
	"github.com/TokenFlux/TokenRouter/internal/pkg/httpclient"
	"github.com/TokenFlux/TokenRouter/internal/pkg/qoder"
)

const (
	qoderOAuthSessionTTL     = 10 * time.Minute
	qoderOAuthPollInterval   = 2
	qoderOAuthDefaultTimeout = 20 * time.Second
)

var errQoderOAuthPollRejected = infraerrors.BadRequest(
	"QODER_OAUTH_POLL_REJECTED",
	"Qoder authorization session was rejected; start a new authorization session",
)

var errQoderOAuthCompletedResponseInvalid = infraerrors.BadRequest(
	"QODER_OAUTH_RESPONSE_INVALID",
	"Qoder authorization completed with an incomplete response; start a new authorization session",
)

type qoderOAuthClient interface {
	PollDeviceToken(ctx context.Context, nonce, verifier string) (*qoder.DeviceTokenResponse, bool, error)
	GetUserInfo(ctx context.Context, token string) (*qoder.UserInfo, error)
	GetOrganizationTags(ctx context.Context, token, organizationID string) (*qoder.OrganizationTags, error)
	GetDataPolicy(ctx context.Context, identity *qoder.AuthIdentity, machine *qoder.MachineIdentity) (bool, error)
}

type qoderOAuthClientFactory func(profile qoder.Profile, proxyURL string) (qoderOAuthClient, error)

type qoderOAuthSession struct {
	State              string
	Nonce              string
	CodeVerifier       string
	Machine            *qoder.MachineIdentity
	AuthURL            string
	Site               qoder.Site
	Profile            qoder.Profile
	ProxyURL           string
	CreatedAt          time.Time
	Completing         bool
	CompleteCh         chan struct{}
	CompletedTokenInfo *QoderTokenInfo
}

type qoderOAuthSessionStore struct {
	mu       sync.RWMutex
	sessions map[string]*qoderOAuthSession
	stopCh   chan struct{}
	doneCh   chan struct{}
	stopOnce sync.Once
	stopped  bool
}

func newQoderOAuthSessionStore() *qoderOAuthSessionStore {
	store := &qoderOAuthSessionStore{
		sessions: make(map[string]*qoderOAuthSession),
		stopCh:   make(chan struct{}),
		doneCh:   make(chan struct{}),
	}
	go store.cleanup()
	return store
}

func (s *qoderOAuthSessionStore) Set(sessionID string, session *qoderOAuthSession) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return false
	}
	if _, ok := s.sessions[sessionID]; ok {
		s.invalidateSessionLocked(sessionID)
	}
	s.sessions[sessionID] = session
	return true
}

func (s *qoderOAuthSessionStore) Get(sessionID string) (*qoderOAuthSession, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	session, ok := s.sessions[sessionID]
	if !ok {
		return nil, false
	}
	if time.Since(session.CreatedAt) > qoderOAuthSessionTTL {
		return nil, false
	}
	return session, true
}

func (s *qoderOAuthSessionStore) BeginCompletion(sessionID, state string) (*qoderOAuthSession, *QoderTokenInfo, <-chan struct{}, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, nil, nil, infraerrors.BadRequest("QODER_OAUTH_SESSION_INVALID", "qoder oauth session_id is required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	session, ok := s.sessions[sessionID]
	if !ok || time.Since(session.CreatedAt) > qoderOAuthSessionTTL {
		if ok {
			s.invalidateSessionLocked(sessionID)
		}
		return nil, nil, nil, infraerrors.BadRequest("QODER_OAUTH_SESSION_INVALID", "qoder oauth session not found or expired")
	}
	if strings.TrimSpace(state) == "" || strings.TrimSpace(state) != session.State {
		return nil, nil, nil, infraerrors.BadRequest("QODER_OAUTH_SESSION_INVALID", "qoder oauth state is invalid")
	}
	if session.CompletedTokenInfo != nil {
		return session, session.CompletedTokenInfo, nil, nil
	}
	if session.Completing {
		if session.CompleteCh == nil {
			session.CompleteCh = make(chan struct{})
		}
		return nil, nil, session.CompleteCh, nil
	}
	session.Completing = true
	session.CompleteCh = make(chan struct{})
	return session, nil, nil, nil
}

func (s *qoderOAuthSessionStore) FinishCompletion(sessionID string, tokenInfo *QoderTokenInfo) {
	s.mu.Lock()
	defer s.mu.Unlock()

	session, ok := s.sessions[strings.TrimSpace(sessionID)]
	if !ok {
		return
	}
	if tokenInfo != nil {
		session.CompletedTokenInfo = tokenInfo
	}
	s.signalCompletionLocked(session)
}

func (s *qoderOAuthSessionStore) signalCompletionLocked(session *qoderOAuthSession) {
	if session == nil {
		return
	}
	session.Completing = false
	if session.CompleteCh != nil {
		close(session.CompleteCh)
		session.CompleteCh = nil
	}
}

func (s *qoderOAuthSessionStore) invalidateSessionLocked(sessionID string) {
	session, ok := s.sessions[sessionID]
	if !ok {
		return
	}
	delete(s.sessions, sessionID)
	s.signalCompletionLocked(session)
}

func (s *qoderOAuthSessionStore) cleanupExpired(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return
	}
	for id, session := range s.sessions {
		if now.Sub(session.CreatedAt) > qoderOAuthSessionTTL {
			s.invalidateSessionLocked(id)
		}
	}
}

func (s *qoderOAuthSessionStore) Stop() {
	s.stopOnce.Do(func() {
		s.mu.Lock()
		s.stopped = true
		for id := range s.sessions {
			s.invalidateSessionLocked(id)
		}
		close(s.stopCh)
		s.mu.Unlock()
		<-s.doneCh
	})
}

func (s *qoderOAuthSessionStore) cleanup() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	defer close(s.doneCh)
	for {
		select {
		case <-s.stopCh:
			return
		case <-ticker.C:
			s.cleanupExpired(time.Now())
		}
	}
}

type QoderOAuthService struct {
	sessionStore  *qoderOAuthSessionStore
	proxyRepo     ProxyRepository
	clientFactory qoderOAuthClientFactory
}

func NewQoderOAuthService(proxyRepo ProxyRepository) *QoderOAuthService {
	svc := &QoderOAuthService{
		sessionStore: newQoderOAuthSessionStore(),
		proxyRepo:    proxyRepo,
	}
	svc.clientFactory = svc.defaultClientFactory
	return svc
}

type QoderAuthURLResult struct {
	AuthURL   string `json:"auth_url"`
	SessionID string `json:"session_id"`
	State     string `json:"state"`
	ExpiresIn int64  `json:"expires_in"`
	Interval  int    `json:"interval"`
	Site      string `json:"site"`
}

type QoderExchangeCodeInput struct {
	SessionID   string
	State       string
	Code        string
	CallbackURL string
	ProxyID     *int64
}

type QoderTokenInfo struct {
	SecurityOauthToken string         `json:"security_oauth_token"`
	RefreshToken       string         `json:"refresh_token,omitempty"`
	MachineID          string         `json:"machine_id"`
	MachineToken       string         `json:"machine_token,omitempty"`
	MachineType        string         `json:"machine_type,omitempty"`
	UID                string         `json:"uid,omitempty"`
	AID                string         `json:"aid,omitempty"`
	OrganizationID     string         `json:"organization_id,omitempty"`
	OrganizationName   string         `json:"organization_name,omitempty"`
	OrganizationTags   []string       `json:"organization_tags,omitempty"`
	Name               string         `json:"name,omitempty"`
	UserType           string         `json:"user_type,omitempty"`
	DataPolicy         string         `json:"data_policy,omitempty"`
	Site               string         `json:"site"`
	RefreshMode        string         `json:"refresh_mode"`
	ExpiresAt          string         `json:"expires_at,omitempty"`
	Extra              map[string]any `json:"extra,omitempty"`
}

func (s *QoderOAuthService) GenerateAuthURL(ctx context.Context, proxyID *int64) (*QoderAuthURLResult, error) {
	return s.GenerateAuthURLForSite(ctx, qoder.SiteCN, proxyID)
}

// GenerateAuthURLForSite 为指定站点创建并冻结 OAuth 会话。
func (s *QoderOAuthService) GenerateAuthURLForSite(ctx context.Context, site qoder.Site, proxyID *int64) (*QoderAuthURLResult, error) {
	profile, err := qoder.ProfileForSite(site)
	if err != nil {
		return nil, err
	}
	req, err := qoder.NewDeviceAuthRequestForProfile(profile)
	if err != nil {
		return nil, fmt.Errorf("generate qoder device auth request: %w", err)
	}
	sessionID := qoder.RandomHex(32)
	state := qoder.RandomToken(32)

	proxyURL, err := s.resolveProxyURL(ctx, proxyID)
	if err != nil {
		return nil, err
	}

	// 国内站必须复用授权 URL 中的 UUID machine_id。
	machine := qoder.NewMachineForSite(profile.Site)
	machine.MachineID = req.MachineID
	machine.MachineToken = req.MachineID
	machine.MachineType = "5"
	session := &qoderOAuthSession{
		State:        state,
		Nonce:        req.Nonce,
		CodeVerifier: req.CodeVerifier,
		Machine:      machine,
		AuthURL:      req.AuthorizationURL(),
		Site:         profile.Site,
		Profile:      profile,
		ProxyURL:     proxyURL,
		CreatedAt:    time.Now(),
	}
	if !s.sessionStore.Set(sessionID, session) {
		return nil, errors.New("qoder oauth service is stopped")
	}

	return &QoderAuthURLResult{
		AuthURL:   session.AuthURL,
		SessionID: sessionID,
		State:     state,
		ExpiresIn: int64(qoderOAuthSessionTTL / time.Second),
		Interval:  qoderOAuthPollInterval,
		Site:      string(profile.Site),
	}, nil
}

func (s *QoderOAuthService) ExchangeCode(ctx context.Context, input *QoderExchangeCodeInput) (*QoderTokenInfo, error) {
	if input == nil {
		return nil, errors.New("qoder oauth input is required")
	}
	if err := normalizeQoderExchangeInput(input); err != nil {
		return nil, err
	}
	tokenInfo, pending, err := s.completeSession(ctx, input.SessionID, input.State, input.ProxyID)
	if err != nil {
		return nil, err
	}
	if pending {
		return nil, errors.New("qoder authorization is still pending; finish authorization in the browser and try again")
	}
	return tokenInfo, nil
}

func (s *QoderOAuthService) Poll(ctx context.Context, sessionID, state string, proxyID *int64) (*QoderPollResult, error) {
	tokenInfo, pending, err := s.completeSession(ctx, sessionID, state, proxyID)
	if err != nil {
		var appErr *infraerrors.ApplicationError
		if errors.As(err, &appErr) {
			return nil, err
		}
		var upstreamErr *qoder.OpenAPIError
		if errors.As(err, &upstreamErr) && (upstreamErr.StatusCode == http.StatusBadRequest ||
			upstreamErr.StatusCode == http.StatusUnauthorized || upstreamErr.StatusCode == http.StatusForbidden) {
			return nil, errQoderOAuthPollRejected.WithCause(err)
		}
		return nil, infraerrors.ServiceUnavailable(
			"QODER_OAUTH_POLL_UNAVAILABLE",
			"Qoder authorization status is temporarily unavailable",
		).WithCause(err)
	}
	if pending {
		return &QoderPollResult{Status: "pending"}, nil
	}
	return &QoderPollResult{
		Status:    "completed",
		TokenInfo: tokenInfo,
	}, nil
}

type QoderPollResult struct {
	Status    string          `json:"status"`
	TokenInfo *QoderTokenInfo `json:"token_info,omitempty"`
}

func (s *QoderOAuthService) completeSession(ctx context.Context, sessionID, state string, proxyID *int64) (*QoderTokenInfo, bool, error) {
	// proxyID 仅为旧客户端兼容字段；OAuth 会话必须使用创建时冻结的代理。
	_ = proxyID
	for {
		session, cachedTokenInfo, waitCh, err := s.sessionStore.BeginCompletion(sessionID, state)
		if err != nil {
			return nil, false, err
		}
		if cachedTokenInfo != nil {
			return cachedTokenInfo, false, nil
		}
		if waitCh != nil {
			select {
			case <-ctx.Done():
				return nil, false, ctx.Err()
			case <-waitCh:
				continue
			}
		}
		tokenInfo, pending, err := s.completeSessionOnce(ctx, session)
		if err != nil || pending {
			s.sessionStore.FinishCompletion(sessionID, nil)
			return nil, pending, err
		}
		s.sessionStore.FinishCompletion(sessionID, tokenInfo)
		return tokenInfo, false, nil
	}
}

func (s *QoderOAuthService) completeSessionOnce(ctx context.Context, session *qoderOAuthSession) (*QoderTokenInfo, bool, error) {
	if session == nil || session.Site != qoder.SiteCN {
		return nil, false, ErrQoderCNReauthorizationRequired
	}
	client, err := s.clientFactory(session.Profile, session.ProxyURL)
	if err != nil {
		return nil, false, err
	}

	tokenResp, ready, err := client.PollDeviceToken(ctx, session.Nonce, session.CodeVerifier)
	if err != nil {
		return nil, false, err
	}
	if !ready {
		return nil, true, nil
	}

	accessToken := tokenResp.DeviceLoginTokenValue()
	expiresAt, validateErr := tokenResp.ValidateDeviceLogin(time.Now())
	if validateErr != nil {
		return nil, false, errQoderOAuthCompletedResponseInvalid.WithCause(validateErr)
	}
	if strings.TrimSpace(tokenResp.RefreshToken) == "" {
		return nil, false, errQoderOAuthCompletedResponseInvalid.WithCause(errors.New("qoder device login refresh token is missing"))
	}
	userInfo, userErr := client.GetUserInfo(ctx, accessToken)
	if userErr != nil {
		userInfo = nil
	}
	identity := qoder.BuildIdentityFromDeviceToken(userInfo, tokenResp)
	if identity == nil || firstNonEmptyQoder(identity.UID, identity.AID) == "" {
		return nil, false, errQoderOAuthCompletedResponseInvalid.WithCause(errors.New("qoder device login user identity is missing"))
	}
	orgErr := populateQoderOrganizationTags(ctx, client, accessToken, identity)
	if agreed, policyErr := client.GetDataPolicy(ctx, identity, session.Machine); policyErr == nil {
		identity.DataPolicyAgreed = &agreed
	}
	return buildQoderTokenInfoForSite(identity, session.Machine, qoder.SiteCN, qoder.RefreshModeQoderCN20, expiresAt, userErr, orgErr), false, nil
}

func normalizeQoderExchangeInput(input *QoderExchangeCodeInput) error {
	if input == nil {
		return errors.New("qoder oauth input is required")
	}
	callbackState, callbackCode := parseQoderCallback(input.CallbackURL)
	if strings.TrimSpace(input.State) == "" && callbackState != "" {
		input.State = callbackState
	}
	if strings.TrimSpace(input.Code) == "" && callbackCode != "" {
		input.Code = callbackCode
	}
	if callbackState != "" && strings.TrimSpace(input.State) != "" && callbackState != strings.TrimSpace(input.State) {
		return errors.New("qoder oauth callback state does not match request state")
	}
	return nil
}

func parseQoderCallback(raw string) (state string, code string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", ""
	}
	u, err := url.Parse(raw)
	if err == nil && u != nil {
		values := u.Query()
		if u.Fragment != "" {
			if fragmentValues, fragmentErr := url.ParseQuery(u.Fragment); fragmentErr == nil {
				for key, vals := range fragmentValues {
					if len(vals) > 0 && values.Get(key) == "" {
						values.Set(key, vals[0])
					}
				}
			}
		}
		state = strings.TrimSpace(values.Get("state"))
		code = strings.TrimSpace(values.Get("code"))
		if state != "" || code != "" {
			return state, code
		}
	}
	if strings.Contains(raw, "=") {
		values, err := url.ParseQuery(strings.TrimPrefix(raw, "?"))
		if err == nil {
			return strings.TrimSpace(values.Get("state")), strings.TrimSpace(values.Get("code"))
		}
	}
	return "", raw
}

func (s *QoderOAuthService) resolveProxyURL(ctx context.Context, proxyID *int64) (string, error) {
	if proxyID == nil {
		return "", nil
	}
	if s.proxyRepo == nil {
		return "", errors.New("proxy repository is not configured")
	}
	proxy, err := s.proxyRepo.GetByID(ctx, *proxyID)
	if err != nil {
		return "", fmt.Errorf("get proxy: %w", err)
	}
	if proxy == nil {
		return "", errors.New("proxy not found")
	}
	return proxy.URL(), nil
}

func (s *QoderOAuthService) defaultClientFactory(profile qoder.Profile, proxyURL string) (qoderOAuthClient, error) {
	client, err := httpclient.GetClient(httpclient.Options{
		ProxyURL: strings.TrimSpace(proxyURL),
		Timeout:  qoderOAuthDefaultTimeout,
	})
	if err != nil {
		return nil, err
	}
	return qoder.NewOAuthClientForProfile(profile, client), nil
}

func populateQoderOrganization(ctx context.Context, client qoderOAuthClient, token string, identity *qoder.AuthIdentity) error {
	return populateQoderOrganizationTags(ctx, client, token, identity)
}

func populateQoderOrganizationTags(ctx context.Context, client qoderOAuthClient, token string, identity *qoder.AuthIdentity) error {
	if client == nil || identity == nil {
		return nil
	}
	if len(identity.OrganizationTags) > 0 {
		return nil
	}
	organizationID := strings.TrimSpace(identity.OrganizationID)
	if organizationID == "" {
		return nil
	}
	tags, err := client.GetOrganizationTags(ctx, token, organizationID)
	if err != nil {
		return err
	}
	if tags == nil {
		return nil
	}
	identity.OrganizationTags = qoderOrganizationTags(tags.Tags)
	return nil
}

func buildQoderTokenInfo(identity *qoder.AuthIdentity, machine *qoder.MachineIdentity, userErr error, orgErr error) *QoderTokenInfo {
	return buildQoderTokenInfoForSite(identity, machine, qoder.SiteCN, qoder.RefreshModeQoderCN20, time.Time{}, userErr, orgErr)
}

func buildQoderTokenInfoForSite(
	identity *qoder.AuthIdentity,
	machine *qoder.MachineIdentity,
	site qoder.Site,
	refreshMode string,
	expiresAt time.Time,
	userErr error,
	orgErr error,
) *QoderTokenInfo {
	if identity == nil {
		identity = &qoder.AuthIdentity{UserType: "personal_standard"}
	}
	if machine == nil {
		machine = qoder.NewMachineForSite(site)
	}
	extra := map[string]any{}
	if userErr != nil {
		extra["userinfo_warning"] = sanitizedQoderOAuthWarning("userinfo_unavailable", "Qoder user info could not be loaded")
	}
	if orgErr != nil {
		extra["organization_warning"] = sanitizedQoderOAuthWarning("organization_unavailable", "Qoder organization info could not be loaded")
	}
	if len(extra) == 0 {
		extra = nil
	}
	tokenInfo := &QoderTokenInfo{
		SecurityOauthToken: strings.TrimSpace(identity.SecurityOauthToken),
		RefreshToken:       strings.TrimSpace(identity.RefreshToken),
		MachineID:          strings.TrimSpace(machine.MachineID),
		MachineToken:       strings.TrimSpace(machine.MachineToken),
		MachineType:        strings.TrimSpace(machine.MachineType),
		UID:                strings.TrimSpace(identity.UID),
		AID:                strings.TrimSpace(identity.AID),
		OrganizationID:     strings.TrimSpace(identity.OrganizationID),
		OrganizationName:   strings.TrimSpace(identity.OrganizationName),
		OrganizationTags:   qoderOrganizationTags(identity.OrganizationTags),
		Name:               strings.TrimSpace(identity.Name),
		UserType:           firstNonEmptyQoder(identity.UserType, "personal_standard"),
		DataPolicy:         qoderDataPolicyFromIdentity(identity),
		Site:               string(site),
		RefreshMode:        refreshMode,
		Extra:              extra,
	}
	if !expiresAt.IsZero() {
		tokenInfo.ExpiresAt = expiresAt.UTC().Format(time.RFC3339)
	}
	return tokenInfo
}

func (s *QoderOAuthService) BuildAccountCredentials(tokenInfo *QoderTokenInfo) map[string]any {
	credentials := map[string]any{}
	if tokenInfo == nil {
		return credentials
	}
	if tokenInfo.SecurityOauthToken != "" {
		credentials["security_oauth_token"] = tokenInfo.SecurityOauthToken
	}
	if tokenInfo.RefreshToken != "" {
		credentials["refresh_token"] = tokenInfo.RefreshToken
	}
	if tokenInfo.MachineID != "" {
		credentials["machine_id"] = tokenInfo.MachineID
	}
	if tokenInfo.MachineToken != "" {
		credentials["machine_token"] = tokenInfo.MachineToken
	}
	if tokenInfo.MachineType != "" {
		credentials["machine_type"] = tokenInfo.MachineType
	}
	if tokenInfo.UID != "" {
		credentials["uid"] = tokenInfo.UID
	}
	if tokenInfo.AID != "" {
		credentials["aid"] = tokenInfo.AID
	}
	if tokenInfo.OrganizationID != "" {
		credentials["organization_id"] = tokenInfo.OrganizationID
	}
	if tokenInfo.OrganizationName != "" {
		credentials["organization_name"] = tokenInfo.OrganizationName
	}
	if len(tokenInfo.OrganizationTags) > 0 {
		credentials["organization_tags"] = qoderOrganizationTags(tokenInfo.OrganizationTags)
	}
	if tokenInfo.Name != "" {
		credentials["name"] = tokenInfo.Name
	}
	if tokenInfo.UserType != "" {
		credentials["user_type"] = tokenInfo.UserType
	}
	if tokenInfo.DataPolicy == "agree" || tokenInfo.DataPolicy == "disagree" {
		credentials["data_policy"] = tokenInfo.DataPolicy
	}
	if tokenInfo.Site != "" {
		credentials["site"] = tokenInfo.Site
	}
	if tokenInfo.RefreshMode != "" {
		credentials["refresh_mode"] = tokenInfo.RefreshMode
	}
	if tokenInfo.ExpiresAt != "" {
		credentials["expires_at"] = tokenInfo.ExpiresAt
	}
	if len(tokenInfo.Extra) > 0 {
		credentials["extra"] = tokenInfo.Extra
	}
	return credentials
}

func qoderDataPolicyFromIdentity(identity *qoder.AuthIdentity) string {
	if identity == nil || identity.DataPolicyAgreed == nil {
		return ""
	}
	if *identity.DataPolicyAgreed {
		return "agree"
	}
	return "disagree"
}

func sanitizedQoderOAuthWarning(code, message string) map[string]string {
	return map[string]string{
		"code":    code,
		"message": message,
	}
}

func (s *QoderOAuthService) Stop() {
	if s != nil && s.sessionStore != nil {
		s.sessionStore.Stop()
	}
}
