package dto

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/stretchr/testify/require"
)

func TestGroupFromServiceAdmin_NormalizesNonFiniteBackupPoolThreshold(t *testing.T) {
	group := GroupFromServiceAdmin(&service.Group{
		BackupPoolRefillThresholdPoints: math.NaN(),
	})

	require.Zero(t, group.BackupPoolRefillThresholdPoints)
	_, err := json.Marshal(group)
	require.NoError(t, err)
}

func TestGroupFromServiceAdmin_PreservesExplicitEmptyPassthroughStripFields(t *testing.T) {
	group := GroupFromServiceAdmin(&service.Group{OpenAIPassthroughStripFields: []string{}})

	require.NotNil(t, group.OpenAIPassthroughStripFields)
	payload, err := json.Marshal(group)
	require.NoError(t, err)
	require.Contains(t, string(payload), `"openai_passthrough_strip_fields":[]`)
}
