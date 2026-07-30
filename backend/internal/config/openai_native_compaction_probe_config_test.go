package config

import (
	"strings"
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"
)

func TestOpenAINativeCompactionProbeDefaultsDisabled(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	setDefaults()

	var cfg Config
	require.NoError(t, viper.Unmarshal(&cfg))
	probe := cfg.Gateway.OpenAINativeCompactionProbe
	require.False(t, probe.Enabled)
	require.Equal(t, 30, probe.TickIntervalSeconds)
	require.Equal(t, 2, probe.MaxWorkers)
	require.Equal(t, 2, probe.ClaimLimit)
	require.Equal(t, 180, probe.ClaimTTLSeconds)
	require.Equal(t, 90, probe.RequestTimeoutSeconds)
	require.Equal(t, int64(20*1024*1024), probe.MaxResponseBytes)
	require.Equal(t, 256, probe.MaxEvents)
	require.Empty(t, probe.ModelAllowlist)
	require.Zero(t, probe.MaxCostPerRunMicroUSD)
	require.Zero(t, probe.MaxCostPerDayMicroUSD)
}

func TestOpenAINativeCompactionProbeEnabledValidation(t *testing.T) {
	valid := GatewayOpenAINativeCompactionProbeConfig{
		Enabled:                   true,
		TickIntervalSeconds:       30,
		MaxWorkers:                2,
		ClaimLimit:                2,
		ClaimTTLSeconds:           180,
		RequestTimeoutSeconds:     90,
		MaxResponseBytes:          20 * 1024 * 1024,
		MaxEvents:                 256,
		SuccessReprobeMinutes:     1440,
		UnsupportedReprobeMinutes: 360,
		RetryInitialSeconds:       60,
		RetryMaxSeconds:           3600,
		RetryJitterRatio:          0.2,
		IsolatedGroupID:           7,
		IsolatedAPIKeyID:          9,
		ModelAllowlist:            []string{"gpt-test"},
		MaxOutputTokens:           256,
		MaxCostPerRunMicroUSD:     1000,
		MaxCostPerDayMicroUSD:     10000,
	}

	tests := []struct {
		name string
		edit func(*GatewayOpenAINativeCompactionProbeConfig)
		want string
	}{
		{name: "isolated identity", edit: func(c *GatewayOpenAINativeCompactionProbeConfig) { c.IsolatedGroupID = 0 }, want: "isolated_group_id"},
		{name: "allowlist", edit: func(c *GatewayOpenAINativeCompactionProbeConfig) { c.ModelAllowlist = []string{" "} }, want: "model_allowlist"},
		{name: "claim concurrency", edit: func(c *GatewayOpenAINativeCompactionProbeConfig) { c.ClaimLimit = 3 }, want: "claim_limit"},
		{name: "lease margin", edit: func(c *GatewayOpenAINativeCompactionProbeConfig) { c.ClaimTTLSeconds = 119 }, want: "claim_ttl_seconds"},
		{name: "response bytes", edit: func(c *GatewayOpenAINativeCompactionProbeConfig) { c.MaxResponseBytes = 16*1024*1024 - 1 }, want: "max_response_bytes"},
		{name: "retry order", edit: func(c *GatewayOpenAINativeCompactionProbeConfig) { c.RetryMaxSeconds = 59 }, want: "retry_max_seconds"},
		{name: "jitter", edit: func(c *GatewayOpenAINativeCompactionProbeConfig) { c.RetryJitterRatio = 1.1 }, want: "retry_jitter_ratio"},
		{name: "cost gate", edit: func(c *GatewayOpenAINativeCompactionProbeConfig) { c.MaxCostPerRunMicroUSD = 0 }, want: "cost limits"},
		{name: "daily cost", edit: func(c *GatewayOpenAINativeCompactionProbeConfig) { c.MaxCostPerDayMicroUSD = 999 }, want: "max_cost_per_day"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			probe := valid
			test.edit(&probe)
			cfg := validConfigForNativeCompactionProbe(t)
			cfg.Gateway.OpenAINativeCompactionProbe = probe
			err := cfg.Validate()
			require.Error(t, err)
			require.True(t, strings.Contains(err.Error(), test.want), err.Error())
		})
	}

	cfg := validConfigForNativeCompactionProbe(t)
	cfg.Gateway.OpenAINativeCompactionProbe = valid
	require.NoError(t, cfg.Validate())
}

func validConfigForNativeCompactionProbe(t *testing.T) *Config {
	t.Helper()
	viper.Reset()
	t.Cleanup(viper.Reset)
	setDefaults()
	viper.Set("jwt.secret", strings.Repeat("x", 32))
	viper.Set("log.output.to_file", false)
	var cfg Config
	require.NoError(t, viper.Unmarshal(&cfg))
	cfg.Gateway.OpenAIScheduler.StickyEscapeTTFTMs = 15000
	cfg.Gateway.OpenAIScheduler.StickyEscapeErrorRate = 0.5
	return &cfg
}
