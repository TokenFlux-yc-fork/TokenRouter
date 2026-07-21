package service

import (
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/pkg/qoder"
	"github.com/stretchr/testify/require"
)

func TestApplyQoderAccountIdentityMetadataMapsRuntimeDataPolicy(t *testing.T) {
	identity := &qoder.AuthIdentity{}
	applyQoderAccountIdentityMetadata(identity, &Account{
		Credentials: map[string]any{"data_policy": "disagree"},
	})

	require.NotNil(t, identity.DataPolicyAgreed)
	require.False(t, *identity.DataPolicyAgreed)
}
