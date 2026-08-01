package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestOpsServiceListAttemptTimelineUsesOpsRepository(t *testing.T) {
	startedAt := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	repo := &opsRepoMock{
		ListAttemptTimelineFn: func(_ context.Context, requestID string) ([]*OpsAttemptTimelineItem, error) {
			require.Equal(t, "request-1", requestID)
			return []*OpsAttemptTimelineItem{{AttemptID: "attempt-1", StartedAt: startedAt}}, nil
		},
	}
	svc := NewOpsService(repo, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)

	items, err := svc.ListAttemptTimeline(context.Background(), " request-1 ")
	require.NoError(t, err)
	require.Equal(t, []*OpsAttemptTimelineItem{{AttemptID: "attempt-1", StartedAt: startedAt}}, items)

	items, err = svc.ListAttemptTimeline(context.Background(), " ")
	require.NoError(t, err)
	require.Nil(t, items)
}

func TestOpsServiceListAttemptTimelineWithoutRepositoryReturnsEmptySlice(t *testing.T) {
	svc := NewOpsService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	items, err := svc.ListAttemptTimeline(context.Background(), "request-1")
	require.NoError(t, err)
	require.NotNil(t, items)
	require.Empty(t, items)
}
