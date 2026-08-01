package repository

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/stretchr/testify/require"
)

func maxOutputTokensTestIdentity(t *testing.T) (*service.Account, service.OpenAIResponsesMaxOutputTokensCapabilityKey) {
	t.Helper()
	account := &service.Account{ID: 17, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Credentials: map[string]any{"api_key": "test"}}
	key, err := service.ResolveOpenAIResponsesMaxOutputTokensCapabilityKey(account, "gpt-5.5")
	require.NoError(t, err)
	return account, key
}

func TestOpenAIResponsesMaxOutputTokensCapabilityGetExactUsesFullKey(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	_, key := maxOutputTokensTestIdentity(t)
	now := time.Now().UTC()
	mock.ExpectQuery(`(?s)FROM openai_responses_max_output_tokens_capabilities.*account_id = \$1.*upstream_fingerprint = \$2.*effective_model = \$3.*contract_version = \$4.*config_generation = \$5`).
		WithArgs(key.AccountID, key.UpstreamFingerprint, key.EffectiveModel, key.Version, key.ConfigGeneration).
		WillReturnRows(sqlmock.NewRows([]string{"id", "state", "checked_at", "last_status", "last_outcome", "created_at", "updated_at"}).
			AddRow(9, "unsupported", now, 400, "explicit_unsupported_parameter", now, now))

	record, err := NewOpenAIResponsesMaxOutputTokensCapabilityRepository(db).GetExact(context.Background(), key)
	require.NoError(t, err)
	require.NotNil(t, record)
	require.Equal(t, key, record.Key)
	require.Equal(t, service.OpenAIResponsesMaxOutputTokensCapabilityUnsupported, record.State)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOpenAIResponsesMaxOutputTokensCapabilityUpsertUsesDefinitiveFence(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	account, key := maxOutputTokensTestIdentity(t)
	checkedAt := time.Now().UTC()
	status := 400
	mock.ExpectExec(`(?s)INSERT INTO openai_responses_max_output_tokens_capabilities.*ON CONFLICT.*state = 'unknown'.*EXCLUDED.state = 'supported'`).
		WithArgs(key.AccountID, key.UpstreamFingerprint, key.EffectiveModel, key.Version, key.ConfigGeneration,
			service.OpenAIResponsesMaxOutputTokensCapabilityUnsupported, checkedAt, status, "explicit_unsupported_parameter").
		WillReturnResult(sqlmock.NewResult(0, 1))

	updated, err := NewOpenAIResponsesMaxOutputTokensCapabilityRepository(db).UpsertObservation(context.Background(), account, service.OpenAIResponsesMaxOutputTokensCapabilityObservation{
		Key: key, State: service.OpenAIResponsesMaxOutputTokensCapabilityUnsupported, StatusCode: &status,
		LastOutcome: "explicit_unsupported_parameter", CheckedAt: checkedAt,
	})
	require.NoError(t, err)
	require.True(t, updated)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOpenAIResponsesMaxOutputTokensCapabilityRejectsUnknownObservation(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	account, key := maxOutputTokensTestIdentity(t)
	updated, err := NewOpenAIResponsesMaxOutputTokensCapabilityRepository(db).UpsertObservation(context.Background(), account, service.OpenAIResponsesMaxOutputTokensCapabilityObservation{
		Key: key, State: service.OpenAIResponsesMaxOutputTokensCapabilityUnknown, CheckedAt: time.Now().UTC(),
	})
	require.ErrorIs(t, err, service.ErrOpenAIResponsesMaxOutputTokensCapabilityInvalidOutcome)
	require.False(t, updated)
	require.NoError(t, mock.ExpectationsWereMet())
}
