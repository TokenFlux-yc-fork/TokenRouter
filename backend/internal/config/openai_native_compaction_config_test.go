package config

import (
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"
)

func TestOpenAINativeCompactionDefaultsAreBounded(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	setDefaults()
	var cfg Config
	require.NoError(t, viper.Unmarshal(&cfg))

	limits := cfg.Gateway.OpenAINativeCompaction
	require.Positive(t, limits.MemoryThresholdBytes)
	require.GreaterOrEqual(t, limits.MaxAttemptBytes, limits.MemoryThresholdBytes)
	require.Positive(t, limits.MaxAttemptEvents)
	require.Positive(t, limits.MaxAttemptDurationSeconds)
	require.GreaterOrEqual(t, limits.MaxProcessStagedBytes, limits.MaxAttemptBytes)
	require.GreaterOrEqual(t, limits.MaxRequestCumulativeBytes, limits.MaxAttemptBytes)
	require.Positive(t, limits.SemanticQuarantineSeconds)
}

func TestOpenAINativeCompactionValidation(t *testing.T) {
	cfg := validConfigForNativeCompactionProbe(t)
	valid := cfg.Gateway.OpenAINativeCompaction

	tests := []struct {
		name string
		edit func(*GatewayOpenAINativeCompactionConfig)
		want string
	}{
		{name: "positive limits", edit: func(c *GatewayOpenAINativeCompactionConfig) { c.MaxAttemptEvents = 0 }, want: "limits must be positive"},
		{name: "memory threshold", edit: func(c *GatewayOpenAINativeCompactionConfig) { c.MemoryThresholdBytes = c.MaxAttemptBytes + 1 }, want: "memory_threshold_bytes"},
		{name: "process budget", edit: func(c *GatewayOpenAINativeCompactionConfig) { c.MaxProcessStagedBytes = c.MaxAttemptBytes - 1 }, want: "max_process_staged_bytes"},
		{name: "request budget", edit: func(c *GatewayOpenAINativeCompactionConfig) { c.MaxRequestCumulativeBytes = c.MaxAttemptBytes - 1 }, want: "max_request_cumulative_bytes"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			copy := *cfg
			copy.Gateway.OpenAINativeCompaction = valid
			test.edit(&copy.Gateway.OpenAINativeCompaction)
			require.ErrorContains(t, copy.Validate(), test.want)
		})
	}

	cfg.Gateway.OpenAINativeCompaction = valid
	require.NoError(t, cfg.Validate())
}
