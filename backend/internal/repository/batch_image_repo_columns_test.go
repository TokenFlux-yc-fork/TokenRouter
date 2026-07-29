package repository

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBatchImageJobColumnsCoalesceLegacyBillingUser(t *testing.T) {
	require.Contains(t, batchImageJobColumns, "COALESCE(billing_user_id, user_id) AS billing_user_id")
}

func TestBatchImageJobColumnsCoalesceLegacyRequestedModel(t *testing.T) {
	require.Contains(t, batchImageJobColumns, "COALESCE(NULLIF(requested_model, ''), model) AS requested_model")
}
