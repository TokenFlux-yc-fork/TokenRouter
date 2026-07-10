package repository

import (
	"math"
	"testing"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	"github.com/stretchr/testify/require"
)

func TestGroupEntityToService_NormalizesNonFiniteBackupPoolThreshold(t *testing.T) {
	group := groupEntityToService(&dbent.Group{
		BackupPoolRefillThresholdPoints: math.Inf(1),
	})

	require.Zero(t, group.BackupPoolRefillThresholdPoints)
}
