package qoder

import (
	"net"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMachineIPFromAddressesUsesFirstNonLoopbackIPv4(t *testing.T) {
	addresses := []net.Addr{
		&net.IPNet{IP: net.ParseIP("127.0.0.1"), Mask: net.CIDRMask(8, 32)},
		&net.IPNet{IP: net.ParseIP("2001:db8::1"), Mask: net.CIDRMask(64, 128)},
		&net.IPNet{IP: net.ParseIP("172.18.0.1"), Mask: net.CIDRMask(24, 32)},
		&net.IPNet{IP: net.ParseIP("192.168.5.36"), Mask: net.CIDRMask(24, 32)},
	}

	require.Equal(t, "172.18.0.1", machineIPFromAddresses(addresses))
}

func TestParseSiteAndRefreshModeCompatibility(t *testing.T) {
	site, err := ParseSite("")
	require.NoError(t, err)
	require.Equal(t, SiteGlobal, site)

	site, err = ParseSite("cn")
	require.NoError(t, err)
	require.Equal(t, SiteCN, site)
	_, err = ParseSite("unknown")
	require.ErrorContains(t, err, "unsupported site")

	mode, err := ParseRefreshMode("")
	require.NoError(t, err)
	require.Equal(t, RefreshModeCosy, mode)
	mode, err = ParseRefreshMode(RefreshModeQoderCN20)
	require.NoError(t, err)
	require.Equal(t, RefreshModeQoderCN20, mode)
	_, err = ParseRefreshMode("unknown")
	require.ErrorContains(t, err, "unsupported refresh_mode")
}

func TestProfileForSiteUsesFrozenProductionValues(t *testing.T) {
	global, err := ProfileForSite(SiteGlobal)
	require.NoError(t, err)
	require.Equal(t, GlobalDeviceAuthorizationURL, global.DeviceAuthorizationURL)
	require.Equal(t, GlobalOpenAPIBaseURL, global.OpenAPIBaseURL)
	require.Equal(t, GlobalCenterBaseURL, global.CenterBaseURL)
	require.Equal(t, GlobalGatewayBaseURL, global.GatewayBaseURL)
	require.Equal(t, GlobalClientVersion, global.ClientVersion)
	require.Equal(t, GlobalOAuthClientID, global.OAuthClientID)
	require.Equal(t, "Qoder/"+GlobalClientVersion, global.OpenAPIUserAgent())

	cn, err := ProfileForSite(SiteCN)
	require.NoError(t, err)
	require.Equal(t, CNDeviceAuthorizationURL, cn.DeviceAuthorizationURL)
	require.Equal(t, CNOpenAPIBaseURL, cn.OpenAPIBaseURL)
	require.Empty(t, cn.CenterBaseURL)
	require.Equal(t, CNGatewayBaseURL, cn.GatewayBaseURL)
	require.Equal(t, CNClientVersion, cn.ClientVersion)
	require.Equal(t, CNOAuthClientID, cn.OAuthClientID)
	require.Equal(t, "Qoder CN/"+CNClientVersion, cn.OpenAPIUserAgent())
}

func TestNormalizeProfileAllowsTestEndpointInjection(t *testing.T) {
	profile, err := NormalizeProfile(Profile{
		Site:           SiteCN,
		OpenAPIBaseURL: "http://127.0.0.1:18080/",
		GatewayBaseURL: "http://127.0.0.1:18081/",
	})
	require.NoError(t, err)
	require.Equal(t, "http://127.0.0.1:18080", profile.OpenAPIBaseURL)
	require.Equal(t, "http://127.0.0.1:18081", profile.GatewayBaseURL)
	require.Equal(t, CNClientVersion, profile.ClientVersion)
}

func TestMachineOSForNormalizesArchitectureAliases(t *testing.T) {
	require.Equal(t, "aarch64_darwin", MachineOSFor("arm64", "darwin"))
	require.Equal(t, "aarch64_linux", MachineOSFor("aarch64", "linux"))
	require.Equal(t, "x86_64_linux", MachineOSFor("amd64", "linux"))
	require.Equal(t, "x86_64_darwin", MachineOSFor("x86_64", "darwin"))
}

func TestModelsAndAliasesAreSiteAware(t *testing.T) {
	require.Equal(t, []string{
		"claude-opus-4-6",
		"auto",
		"performance",
		"efficient",
		"lite",
		"qwen3.8-max-preview",
		"qwen3.7-max",
		"qwen3.7-plus",
		"kimi-k3",
		"kimi-k2.7-code",
		"glm-5.2",
		"deepseek-v4-pro",
		"deepseek-v4-flash",
		"minimax-m3",
	}, DefaultRequestModelIDsForSite(SiteGlobal))
	require.Equal(t, []string{
		"auto",
		"qwen3.8-max-preview",
		"qwen3.7-max",
		"qwen3.7-plus",
		"qwen3.6-flash",
		"deepseek-v4-pro",
		"deepseek-v4-flash",
		"glm-5.2",
		"kimi-k2.7-code",
		"minimax-m2.7",
	}, DefaultRequestModelIDsForSite(SiteCN))

	route, ok := AliasForSite(SiteGlobal, "qwen3.8-max-preview")
	require.True(t, ok)
	require.Equal(t, "qmodel_preview", route)
	route, ok = AliasForSite(SiteCN, "minimax-m2.7")
	require.True(t, ok)
	require.Equal(t, "mmodel", route)
	require.False(t, ModelCompatibleWithSite(SiteGlobal, "minimax-m2.7"))
	require.True(t, ModelCompatibleWithSite(SiteCN, "minimax-m2.7"))
	require.True(t, ModelCompatibleWithSite(SiteGlobal, "unknown-raw-key"))
}

func TestThinkingCapabilityForSiteUsesCNSnapshot(t *testing.T) {
	tests := []struct {
		model string
		want  ThinkingCapability
	}{
		{model: "auto", want: ThinkingUnsupported},
		{model: "qwen3.8-max-preview", want: ThinkingToggleOnly},
		{model: "qwen3.7-max", want: ThinkingToggleOnly},
		{model: "qwen3.7-plus", want: ThinkingToggleOnly},
		{model: "qwen3.6-flash", want: ThinkingUnsupported},
		{model: "deepseek-v4-pro", want: ThinkingHighMax},
		{model: "deepseek-v4-flash", want: ThinkingHighMax},
		{model: "glm-5.2", want: ThinkingHighMax},
		{model: "kimi-k2.7-code", want: ThinkingUnsupported},
		{model: "minimax-m2.7", want: ThinkingUnsupported},
	}

	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			require.Equal(t, tt.want, ThinkingCapabilityForSite(SiteCN, tt.model))
		})
	}

	// route key、空白和大小写必须与公开 alias 得到相同能力。
	require.Equal(t, ThinkingToggleOnly, ThinkingCapabilityForSite(SiteCN, " QMODEL_LATEST "))
	require.Equal(t, ThinkingHighMax, ThinkingCapabilityForSite(SiteCN, "dmodel"))
	// Global 与未知/自定义路由必须保持隔离。
	require.Equal(t, ThinkingUnsupported, ThinkingCapabilityForSite(SiteGlobal, "qmodel_latest"))
	require.Equal(t, ThinkingUnsupported, ThinkingCapabilityForSite(SiteCN, "custom-model"))
}

func TestCNThinkingCapabilitySnapshotCoversEveryModel(t *testing.T) {
	// 能力表必须显式覆盖所有 CN route key，避免新增模型静默沿用零值能力。
	wantRoutes := make(map[string]struct{}, len(cnAliases))
	for _, model := range cnModels {
		route, ok := cnAliases[model.ID]
		require.True(t, ok, "CN 模型 %q 缺少 route key", model.ID)
		wantRoutes[route] = struct{}{}
	}
	require.Len(t, cnAliases, len(cnModels), "CN 模型和 alias 数量必须一致")

	gotRoutes := make(map[string]struct{}, len(cnThinkingCapabilities))
	for route := range cnThinkingCapabilities {
		gotRoutes[route] = struct{}{}
	}
	require.Equal(t, wantRoutes, gotRoutes, "CN Thinking 能力表必须与模型 route key 完全一致")
}
