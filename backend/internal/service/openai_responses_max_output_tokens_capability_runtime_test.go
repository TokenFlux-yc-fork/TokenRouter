package service

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type maxOutputTokensCapabilityRepositoryStub struct {
	ensured      []OpenAIResponsesMaxOutputTokensCapabilityKey
	observations []OpenAIResponsesMaxOutputTokensCapabilityObservation
	record       *OpenAIResponsesMaxOutputTokensCapabilityRecord
	getErr       error
	ensureErr    error
	upsertErr    error
}

func (r *maxOutputTokensCapabilityRepositoryStub) GetExact(context.Context, OpenAIResponsesMaxOutputTokensCapabilityKey) (*OpenAIResponsesMaxOutputTokensCapabilityRecord, error) {
	return r.record, r.getErr
}

func (r *maxOutputTokensCapabilityRepositoryStub) EnsureUnknown(_ context.Context, _ *Account, key OpenAIResponsesMaxOutputTokensCapabilityKey) (bool, error) {
	r.ensured = append(r.ensured, key)
	return r.ensureErr == nil, r.ensureErr
}

func (r *maxOutputTokensCapabilityRepositoryStub) UpsertObservation(_ context.Context, _ *Account, observation OpenAIResponsesMaxOutputTokensCapabilityObservation) (bool, error) {
	r.observations = append(r.observations, observation)
	return r.upsertErr == nil, r.upsertErr
}

func TestPrepareOpenAIResponsesMaxOutputTokensCapability(t *testing.T) {
	account := newOpenAIRejectedFieldTestAccount()
	repo := &maxOutputTokensCapabilityRepositoryStub{}
	svc := &OpenAIGatewayService{openAIResponsesMaxOutputTokensCapabilityRepo: repo}

	key, ok := svc.prepareOpenAIResponsesMaxOutputTokensCapability(context.Background(), account, []byte(`{"model":"gpt-5.5","max_output_tokens":100}`), "gpt-5.5")
	require.True(t, ok)
	require.True(t, key.Valid())
	require.Equal(t, []OpenAIResponsesMaxOutputTokensCapabilityKey{key}, repo.ensured)

	_, ok = svc.prepareOpenAIResponsesMaxOutputTokensCapability(context.Background(), account, []byte(`{"model":"gpt-5.5"}`), "gpt-5.5")
	require.False(t, ok)
	require.Len(t, repo.ensured, 1)
}

func TestObserveOpenAIResponsesMaxOutputTokensCapability(t *testing.T) {
	account := newOpenAIRejectedFieldTestAccount()
	key, err := ResolveOpenAIResponsesMaxOutputTokensCapabilityKey(account, "gpt-5.5")
	require.NoError(t, err)
	repo := &maxOutputTokensCapabilityRepositoryStub{}
	svc := &OpenAIGatewayService{openAIResponsesMaxOutputTokensCapabilityRepo: repo}

	svc.observeOpenAIResponsesMaxOutputTokensCapability(context.Background(), account, key, OpenAIResponsesMaxOutputTokensCapabilityUnsupported, http.StatusBadRequest, "explicit_unsupported_parameter")
	require.Len(t, repo.observations, 1)
	observation := repo.observations[0]
	require.Equal(t, key, observation.Key)
	require.Equal(t, OpenAIResponsesMaxOutputTokensCapabilityUnsupported, observation.State)
	require.Equal(t, http.StatusBadRequest, *observation.StatusCode)
	require.Equal(t, "explicit_unsupported_parameter", observation.LastOutcome)
	require.WithinDuration(t, time.Now().UTC(), observation.CheckedAt, time.Second)
}

func TestOpenAIResponsesMaxOutputTokensCapabilityKnownUnsupported(t *testing.T) {
	account := newOpenAIRejectedFieldTestAccount()
	key, err := ResolveOpenAIResponsesMaxOutputTokensCapabilityKey(account, "gpt-5.5")
	require.NoError(t, err)
	repo := &maxOutputTokensCapabilityRepositoryStub{record: &OpenAIResponsesMaxOutputTokensCapabilityRecord{
		Key: key, State: OpenAIResponsesMaxOutputTokensCapabilityUnsupported,
	}}
	svc := &OpenAIGatewayService{openAIResponsesMaxOutputTokensCapabilityRepo: repo}

	require.True(t, svc.OpenAIResponsesMaxOutputTokensCapabilityKnownUnsupported(
		context.Background(), account, []byte(`{"max_output_tokens":100}`), "gpt-5.5", false,
	))
	require.False(t, svc.OpenAIResponsesMaxOutputTokensCapabilityKnownUnsupported(
		context.Background(), account, []byte(`{"input":"hello"}`), "gpt-5.5", false,
	))

	repo.getErr = errors.New("lookup failed")
	require.False(t, svc.OpenAIResponsesMaxOutputTokensCapabilityKnownUnsupported(
		context.Background(), account, []byte(`{"max_output_tokens":100}`), "gpt-5.5", false,
	))
}

func TestNewOpenAIResponsesMaxOutputTokensUnsupportedFailoverError(t *testing.T) {
	err := NewOpenAIResponsesMaxOutputTokensUnsupportedFailoverError(http.StatusBadRequest)
	require.True(t, err.IsOpenAIResponsesMaxOutputTokensUnsupported())
	require.True(t, err.ShouldRetryNextAccount())
	require.False(t, err.ShouldReportAccountScheduleFailure())
	require.Equal(t, OpenAIResponsesMaxOutputTokensUnsupportedClientMessage, err.ClientMessage)
}

func TestMaxOutputTokensCapabilityRepositoryFailureIsFailOpen(t *testing.T) {
	account := newOpenAIRejectedFieldTestAccount()
	repo := &maxOutputTokensCapabilityRepositoryStub{ensureErr: errors.New("ensure failed"), upsertErr: errors.New("upsert failed")}
	svc := &OpenAIGatewayService{openAIResponsesMaxOutputTokensCapabilityRepo: repo}

	key, ok := svc.prepareOpenAIResponsesMaxOutputTokensCapability(context.Background(), account, []byte(`{"max_output_tokens":100}`), "gpt-5.5")
	require.True(t, ok)
	svc.observeOpenAIResponsesMaxOutputTokensCapability(context.Background(), account, key, OpenAIResponsesMaxOutputTokensCapabilityUnsupported, http.StatusBadRequest, "explicit_unsupported_parameter")
	require.Len(t, repo.ensured, 1)
	require.Len(t, repo.observations, 1)
}
