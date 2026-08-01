package repository

import (
	"context"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/stretchr/testify/require"
)

func TestOpsRepositoryListAttemptTimeline(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	mock.ExpectQuery(`(?s)` + regexp.QuoteMeta("SELECT jsonb_build_object(") + `.*ORDER BY\s+.*attempts\.ordinality ASC,\s+e\.id ASC\s+LIMIT 200`).
		WithArgs("request-1").
		WillReturnRows(sqlmock.NewRows([]string{"attempt"}).
			AddRow(`{"at_unix_ms":200,"index":1,"platform":"openai","upstream_status_code":429,"upstream_request_id":"up-2","kind":"http_error"}`).
			AddRow(`{"at_unix_ms":100,"index":0,"platform":"openai","account_id":7,"stage":"upstream"}`))

	repo := &opsRepository{db: db}
	items, err := repo.ListAttemptTimeline(context.Background(), "request-1")
	require.NoError(t, err)
	require.Equal(t, []*service.OpsAttemptTimelineItem{
		{AtUnixMs: 200, Index: 1, Platform: "openai", UpstreamStatusCode: 429, UpstreamRequestID: "up-2", Kind: "http_error"},
		{AtUnixMs: 100, Index: 0, Platform: "openai", AccountID: 7, Stage: "upstream"},
	}, items)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOpsRepositoryListAttemptTimelineEmptyRequestIDSkipsQuery(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	repo := &opsRepository{db: db}
	items, err := repo.ListAttemptTimeline(context.Background(), "  ")
	require.NoError(t, err)
	require.Empty(t, items)
	require.NoError(t, mock.ExpectationsWereMet())
}
