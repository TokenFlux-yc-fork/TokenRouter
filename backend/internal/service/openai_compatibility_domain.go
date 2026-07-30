package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net"
	"net/url"
	"strings"
)

type OpenAIUpstreamProvider string
type OpenAIUpstreamFingerprint string

// OpenAICompatibilityDomain is the fail-closed boundary for reusing state
// across requests, retries, HTTP transports, and WebSocket connections.
type OpenAICompatibilityDomain struct {
	Provider            OpenAIUpstreamProvider
	UpstreamFingerprint OpenAIUpstreamFingerprint
	EffectiveModel      string
	ContractVersion     string
}

func (d OpenAICompatibilityDomain) Valid() bool {
	return strings.TrimSpace(string(d.Provider)) != "" &&
		strings.TrimSpace(string(d.UpstreamFingerprint)) != "" &&
		strings.TrimSpace(d.EffectiveModel) != "" &&
		strings.TrimSpace(d.ContractVersion) != ""
}

func (d OpenAICompatibilityDomain) CompatibleWith(other OpenAICompatibilityDomain) bool {
	return d.Valid() && other.Valid() &&
		d.Provider == other.Provider &&
		d.UpstreamFingerprint == other.UpstreamFingerprint &&
		d.EffectiveModel == other.EffectiveModel &&
		d.ContractVersion == other.ContractVersion
}

// ResolveOpenAINativeCompatibilityDomainForRequest constructs the native-v2
// compatibility boundary after all request and account model mappings. It only
// uses account configuration and the canonical endpoint; response payloads,
// including encrypted_content, are deliberately outside this boundary.
func ResolveOpenAINativeCompatibilityDomainForRequest(
	ctx context.Context,
	account *Account,
	routingModel string,
) (OpenAICompatibilityDomain, error) {
	if account == nil || !account.IsOpenAICompatible() {
		return OpenAICompatibilityDomain{}, errors.New("openai native compatibility domain requires an OpenAI-compatible account")
	}

	effectiveModel := resolveOpenAIAccountUpstreamModelForRequest(
		account,
		routingModel,
		false,
		openAIHTTPPassthroughRoutingFromContext(ctx),
	)
	key, err := ResolveOpenAINativeCompactionCapabilityKey(account, effectiveModel)
	if err != nil {
		return OpenAICompatibilityDomain{}, err
	}
	domain := OpenAICompatibilityDomain{
		Provider:            OpenAIUpstreamProvider(strings.ToLower(strings.TrimSpace(account.Platform))),
		UpstreamFingerprint: key.UpstreamFingerprint,
		EffectiveModel:      key.EffectiveModel,
		ContractVersion:     key.ContractVersion,
	}
	if !domain.Valid() {
		return OpenAICompatibilityDomain{}, errors.New("openai native compatibility domain is unknown")
	}
	return domain, nil
}

// ResolveOpenAICanonicalResponsesEndpoint returns the bare Responses endpoint
// used to identify native compaction capability. It deliberately drops URL
// components that must not affect capability identity or escape into logs.
func ResolveOpenAICanonicalResponsesEndpoint(account *Account) (string, error) {
	if account == nil {
		return "", errors.New("openai responses endpoint account is nil")
	}
	if account.Type == AccountTypeOAuth {
		return chatgptCodexURL, nil
	}

	rawURL := strings.TrimSpace(account.GetOpenAIBaseURL())
	if rawURL == "" {
		rawURL = openaiPlatformAPIURL
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", errors.New("invalid openai responses endpoint")
	}
	if parsed.Scheme == "" || parsed.Hostname() == "" {
		return "", errors.New("openai responses endpoint has no scheme or host")
	}

	endpointPath := strings.TrimRight(parsed.Path, "/")
	if strings.HasSuffix(endpointPath, "/responses/compact") {
		endpointPath = strings.TrimSuffix(endpointPath, "/compact")
	}
	parsed.Path = endpointPath
	parsed.RawPath = ""
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.ForceQuery = false
	parsed.Fragment = ""
	return buildOpenAIResponsesURL(parsed.String()), nil
}

// NewOpenAIUpstreamFingerprint produces an opaque compatibility key. Raw
// upstream hosts, credentials, query parameters, and fragments never leave
// this boundary. Transport is intentionally excluded, while the canonical
// endpoint path prevents capability reuse across tenants on the same host.
func NewOpenAIUpstreamFingerprint(rawURL, endpointFamily string) (OpenAIUpstreamFingerprint, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return "", err
	}
	if parsed.Hostname() == "" {
		return "", errors.New("upstream url has no host")
	}

	host := strings.ToLower(strings.TrimSpace(parsed.Hostname()))
	port := strings.TrimSpace(parsed.Port())
	if (strings.EqualFold(parsed.Scheme, "https") || strings.EqualFold(parsed.Scheme, "wss")) && port == "443" {
		port = ""
	}
	if (strings.EqualFold(parsed.Scheme, "http") || strings.EqualFold(parsed.Scheme, "ws")) && port == "80" {
		port = ""
	}
	if port != "" {
		host = net.JoinHostPort(host, port)
	}

	family := strings.ToLower(strings.Trim(strings.TrimSpace(endpointFamily), "/"))
	if family == "" {
		return "", errors.New("endpoint family is empty")
	}
	endpointPath := (&url.URL{Path: parsed.Path}).EscapedPath()
	if endpointPath == "" {
		endpointPath = "/"
	}

	sum := sha256.Sum256([]byte(host + "\x00" + endpointPath + "\x00" + family))
	return OpenAIUpstreamFingerprint("upstream_v1_" + hex.EncodeToString(sum[:16])), nil
}
