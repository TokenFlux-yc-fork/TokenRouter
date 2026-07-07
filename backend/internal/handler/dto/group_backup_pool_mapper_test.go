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
