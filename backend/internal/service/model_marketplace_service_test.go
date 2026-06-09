package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

type marketplaceGroupRepoStub struct {
	GroupRepository
	groups []Group
}

func (r *marketplaceGroupRepoStub) ListActive(ctx context.Context) ([]Group, error) {
	return r.groups, nil
}

type marketplaceSettingsStub struct {
	SettingRepository
}

func (r *marketplaceSettingsStub) GetMultiple(ctx context.Context, keys []string) (map[string]string, error) {
	return map[string]string{}, nil
}

func TestModelMarketplaceServiceListPublicDoesNotReadRecentRequestHealth(t *testing.T) {
	svc := NewModelMarketplaceService(
		&marketplaceGroupRepoStub{groups: []Group{
			{
				ID:                 1,
				Name:               "Public Plus",
				Platform:           PlatformOpenAI,
				DisplayBrand:       "OpenAI",
				RateMultiplier:     1,
				Status:             StatusActive,
				ActiveAccountCount: 1,
			},
		}},
		&marketplaceSettingsStub{},
		nil,
		nil,
		nil,
		struct{}{},
	)

	groups, err := svc.ListPublic(context.Background())

	require.NoError(t, err)
	require.Len(t, groups, 1)
	require.NotEmpty(t, groups[0].Models)
}
