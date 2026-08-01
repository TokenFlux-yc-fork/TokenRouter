package repository

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/stretchr/testify/require"
)

func TestOpsRepositoryListAttemptTimelineUsesDurableAttributions(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	startedAt := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	completedAt := startedAt.Add(time.Second)
	observedAt := completedAt.Add(time.Second)
	httpStatus := 429
	semanticOutcome := "upstream_error"
	terminalEvent := "response.completed"
	successfulTerminal := false
	deliveryCommitted := true
	safeToFailover := false
	mock.ExpectQuery(`(?s)FROM upstream_attempt_attributions.*WHERE gateway_request_id = \$1 OR client_request_id = \$1.*ORDER BY started_at ASC, attempt_id ASC.*LIMIT 200`).
		WithArgs("request-1").
		WillReturnRows(sqlmock.NewRows([]string{
			"attempt_id", "client_request_id", "gateway_request_id", "started_at", "completed_at", "observed_at", "account_id", "transport", "upstream_provider", "effective_model", "state", "state_version", "transport_observed", "http_observed", "http_status", "upstream_request_id", "upstream_response_id", "ws_connection_id", "ws_turn_id", "semantic_observed", "semantic_outcome", "terminal_event", "successful_terminal", "usage_observed", "delivery_observed", "delivery_committed", "safe_to_failover",
		}).
			AddRow("attempt-1", "client-1", "gateway-1", startedAt, completedAt, observedAt, int64(7), "http", "openai", "gpt-test", "terminal", int64(3), true, true, httpStatus, "upstream-request", nil, nil, nil, true, semanticOutcome, terminalEvent, successfulTerminal, true, true, deliveryCommitted, safeToFailover).
			AddRow("attempt-2", "client-2", "gateway-2", startedAt.Add(time.Second), nil, observedAt, int64(8), "websocket", "openai", "gpt-test", "started", int64(1), false, false, nil, nil, nil, nil, nil, false, nil, nil, nil, false, false, nil, nil))

	repo := &opsRepository{db: db}
	items, err := repo.ListAttemptTimeline(context.Background(), " request-1 ")
	require.NoError(t, err)
	require.Len(t, items, 2)
	require.Equal(t, "attempt-1", items[0].AttemptID)
	require.Equal(t, 0, items[0].Index)
	require.Equal(t, "client-1", items[0].ClientRequestID)
	require.Equal(t, "gateway-1", items[0].GatewayRequestID)
	require.Equal(t, startedAt, items[0].StartedAt)
	require.Equal(t, &completedAt, items[0].CompletedAt)
	require.Equal(t, observedAt, items[0].ObservedAt)
	require.Equal(t, &httpStatus, items[0].HTTPStatus)
	require.Equal(t, &semanticOutcome, items[0].SemanticOutcome)
	require.Equal(t, &terminalEvent, items[0].TerminalEvent)
	require.Equal(t, &successfulTerminal, items[0].SuccessfulTerminal)
	require.Equal(t, &deliveryCommitted, items[0].DeliveryCommitted)
	require.Equal(t, &safeToFailover, items[0].SafeToFailover)
	require.Nil(t, items[1].CompletedAt)
	require.Nil(t, items[1].UpstreamRequestID)
	require.Nil(t, items[1].HTTPStatus)
	require.Nil(t, items[1].SemanticOutcome)
	require.Nil(t, items[1].SuccessfulTerminal)
	require.Nil(t, items[1].DeliveryCommitted)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOpsRepositoryListAttemptTimelineEmptyRequestIDSkipsQuery(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	repo := &opsRepository{db: db}
	items, err := repo.ListAttemptTimeline(context.Background(), "  ")
	require.NoError(t, err)
	require.NotNil(t, items)
	require.Empty(t, items)
	require.NoError(t, mock.ExpectationsWereMet())
}

var _ service.OpsRepository = (*opsRepository)(nil)
