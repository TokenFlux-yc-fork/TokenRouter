package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOpsServiceListAttemptTimelineSortsAndReturnsEmptySlice(t *testing.T) {
	repo := &opsRepoMock{
		ListAttemptTimelineFn: func(_ context.Context, requestID string) ([]*OpsAttemptTimelineItem, error) {
			require.Equal(t, "request-1", requestID)
			return []*OpsAttemptTimelineItem{
				{AtUnixMs: 200, Index: 0},
				{AtUnixMs: 100, Index: 2},
				{AtUnixMs: 100, Index: 1},
			}, nil
		},
	}
	svc := NewOpsService(repo, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)

	items, err := svc.ListAttemptTimeline(context.Background(), " request-1 ")
	require.NoError(t, err)
	require.Equal(t, []int64{100, 100, 200}, []int64{items[0].AtUnixMs, items[1].AtUnixMs, items[2].AtUnixMs})
	require.Equal(t, []int{1, 2, 0}, []int{items[0].Index, items[1].Index, items[2].Index})

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
