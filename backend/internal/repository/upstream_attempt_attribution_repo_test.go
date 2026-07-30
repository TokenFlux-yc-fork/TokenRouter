package repository

import (
	"context"
	"database/sql"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/stretchr/testify/require"
)

func TestUpstreamAttemptAttributionRepositoryUpsertUsesMonotonicTerminalCAS(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	repo := NewUpstreamAttemptAttributionRepository(db)
	attempt := unitUpstreamAttempt()

	mock.ExpectQuery("(?s)INSERT INTO upstream_attempt_attributions.*state <> 'terminal'.*EXCLUDED.state_version > upstream_attempt_attributions.state_version.*CASE upstream_attempt_attributions.state").
		WithArgs(
			attempt.AttemptID, attempt.ClientRequestID, attempt.GatewayRequestID, attempt.UpstreamRequestID,
			attempt.UpstreamResponseID, attempt.WSConnectionID, attempt.WSTurnID, attempt.AccountID,
			attempt.Transport, attempt.Domain.Provider, attempt.Domain.UpstreamFingerprint,
			attempt.Domain.EffectiveModel, attempt.Domain.ContractVersion, attempt.State,
			attempt.StateVersion, attempt.TransportObserved, attempt.HTTPObserved, attempt.HTTPStatus,
			attempt.SemanticObserved, attempt.Semantic.Outcome, attempt.Semantic.OutputItemDoneCount,
			attempt.Semantic.CompactionItemCount, attempt.Semantic.MalformedItemCount,
			attempt.Semantic.TerminalEvent, attempt.Semantic.TerminalCount,
			attempt.Semantic.SuccessfulTerminal, attempt.Usage.Observed, attempt.Usage.InputTokens,
			attempt.Usage.OutputTokens, attempt.Usage.CacheCreationInputTokens,
			attempt.Usage.CacheReadInputTokens, attempt.Usage.ImageInputTokens,
			attempt.Usage.ImageOutputTokens, attempt.Usage.CostUSD, attempt.DeliveryObserved, attempt.DeliveryCommitted,
			attempt.SafeToFailover, attempt.StartedAt, attempt.CompletedAt, attempt.ObservedAt,
		).
		WillReturnRows(upstreamAttemptRows(attempt))

	stored, err := repo.Upsert(context.Background(), attempt)
	require.NoError(t, err)
	require.Equal(t, attempt.AttemptID, stored.AttemptID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUpstreamAttemptAttributionRepositoryEqualVersionReplayReturnsExisting(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	repo := NewUpstreamAttemptAttributionRepository(db)
	attempt := unitUpstreamAttempt()

	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO upstream_attempt_attributions")).WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT " + upstreamAttemptAttributionColumns + " FROM upstream_attempt_attributions WHERE attempt_id = $1")).
		WithArgs(attempt.AttemptID).
		WillReturnRows(upstreamAttemptRows(attempt))

	stored, err := repo.Upsert(context.Background(), attempt)
	require.NoError(t, err)
	require.Equal(t, attempt.StateVersion, stored.StateVersion)
	require.NoError(t, mock.ExpectationsWereMet())
}

func upstreamAttemptRows(attempt service.UpstreamAttemptAttribution) *sqlmock.Rows {
	columns := []string{
		"attempt_id", "client_request_id", "gateway_request_id", "upstream_request_id", "upstream_response_id",
		"ws_connection_id", "ws_turn_id", "account_id", "transport", "upstream_provider", "upstream_fingerprint",
		"effective_model", "contract_version", "state", "state_version", "transport_observed", "http_observed", "http_status",
		"semantic_observed", "semantic_outcome", "output_item_done_count",
		"compaction_item_count", "malformed_item_count", "terminal_event", "terminal_count", "successful_terminal",
		"usage_observed", "input_tokens", "output_tokens", "cache_creation_input_tokens", "cache_read_input_tokens",
		"image_input_tokens", "image_output_tokens", "cost_usd", "delivery_observed", "delivery_committed", "safe_to_failover", "started_at",
		"completed_at", "observed_at", "created_at", "updated_at",
	}
	return sqlmock.NewRows(columns).AddRow(
		attempt.AttemptID, attempt.ClientRequestID, attempt.GatewayRequestID, attempt.UpstreamRequestID,
		attempt.UpstreamResponseID, attempt.WSConnectionID, attempt.WSTurnID, attempt.AccountID, attempt.Transport,
		attempt.Domain.Provider, attempt.Domain.UpstreamFingerprint, attempt.Domain.EffectiveModel,
		attempt.Domain.ContractVersion, attempt.State, attempt.StateVersion, attempt.TransportObserved,
		attempt.HTTPObserved, nil, attempt.SemanticObserved, attempt.Semantic.Outcome,
		attempt.Semantic.OutputItemDoneCount, attempt.Semantic.CompactionItemCount, attempt.Semantic.MalformedItemCount,
		attempt.Semantic.TerminalEvent, attempt.Semantic.TerminalCount, attempt.Semantic.SuccessfulTerminal,
		attempt.Usage.Observed, nil, nil, nil, nil, nil, nil, nil, attempt.DeliveryObserved, attempt.DeliveryCommitted,
		attempt.SafeToFailover, attempt.StartedAt, nil, attempt.ObservedAt, attempt.StartedAt, attempt.ObservedAt,
	)
}

func unitUpstreamAttempt() service.UpstreamAttemptAttribution {
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	return service.UpstreamAttemptAttribution{
		AttemptID:        "attempt_unit",
		ClientRequestID:  "client_unit",
		GatewayRequestID: "gateway_unit",
		AccountID:        7,
		Transport:        service.UpstreamAttemptTransportHTTP,
		Domain: service.OpenAICompatibilityDomain{
			Provider: "openai", UpstreamFingerprint: "upstream_v1_unit", EffectiveModel: "gpt-unit", ContractVersion: "v2",
		},
		State: service.UpstreamAttemptStateStarted, StateVersion: 1, StartedAt: now, ObservedAt: now,
	}
}
