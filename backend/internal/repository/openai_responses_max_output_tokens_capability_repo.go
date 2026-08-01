package repository

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/service"
)

type openAIResponsesMaxOutputTokensCapabilityRepository struct{ db *sql.DB }

var _ service.OpenAIResponsesMaxOutputTokensCapabilityRepository = (*openAIResponsesMaxOutputTokensCapabilityRepository)(nil)

func NewOpenAIResponsesMaxOutputTokensCapabilityRepository(db *sql.DB) service.OpenAIResponsesMaxOutputTokensCapabilityRepository {
	return &openAIResponsesMaxOutputTokensCapabilityRepository{db: db}
}

func (r *openAIResponsesMaxOutputTokensCapabilityRepository) GetExact(ctx context.Context, key service.OpenAIResponsesMaxOutputTokensCapabilityKey) (*service.OpenAIResponsesMaxOutputTokensCapabilityRecord, error) {
	if !key.Valid() {
		return nil, errors.New("invalid openai max_output_tokens capability key")
	}
	if r == nil || r.db == nil {
		return nil, errors.New("max_output_tokens capability repository database is nil")
	}
	row := r.db.QueryRowContext(ctx, `
		SELECT id, state, checked_at, last_status, last_outcome, created_at, updated_at
		FROM openai_responses_max_output_tokens_capabilities
		WHERE account_id = $1 AND upstream_fingerprint = $2 AND effective_model = $3 AND contract_version = $4 AND config_generation = $5
	`, key.AccountID, key.UpstreamFingerprint, key.EffectiveModel, key.Version, key.ConfigGeneration)
	var rec service.OpenAIResponsesMaxOutputTokensCapabilityRecord
	rec.Key = key
	var state string
	var checkedAt sql.NullTime
	var status sql.NullInt64
	if err := row.Scan(&rec.ID, &state, &checkedAt, &status, &rec.LastOutcome, &rec.CreatedAt, &rec.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	rec.State = service.OpenAIResponsesMaxOutputTokensCapabilityState(state)
	if checkedAt.Valid {
		rec.CheckedAt = &checkedAt.Time
	}
	if status.Valid {
		n := int(status.Int64)
		rec.LastStatus = &n
	}
	return &rec, nil
}

func (r *openAIResponsesMaxOutputTokensCapabilityRepository) EnsureUnknown(ctx context.Context, account *service.Account, key service.OpenAIResponsesMaxOutputTokensCapabilityKey) (bool, error) {
	if !key.Valid() || account == nil || account.ID != key.AccountID || !account.IsOpenAICompatible() {
		return false, errors.New("max_output_tokens capability requires exact OpenAI-compatible account")
	}
	if r == nil || r.db == nil {
		return false, errors.New("max_output_tokens capability repository database is nil")
	}
	result, err := r.db.ExecContext(ctx, `
		INSERT INTO openai_responses_max_output_tokens_capabilities
		(account_id, upstream_fingerprint, effective_model, contract_version, config_generation, state)
		SELECT id, $2, $3, $4, $5, 'unknown' FROM accounts
		WHERE id = $1 AND deleted_at IS NULL
		ON CONFLICT (account_id, upstream_fingerprint, effective_model, contract_version, config_generation) DO NOTHING
	`, key.AccountID, key.UpstreamFingerprint, key.EffectiveModel, key.Version, key.ConfigGeneration)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n == 1, err
}

func (r *openAIResponsesMaxOutputTokensCapabilityRepository) UpsertObservation(ctx context.Context, account *service.Account, observation service.OpenAIResponsesMaxOutputTokensCapabilityObservation) (bool, error) {
	if !observation.Key.Valid() || account == nil || account.ID != observation.Key.AccountID || !account.IsOpenAICompatible() {
		return false, errors.New("max_output_tokens capability requires exact OpenAI-compatible account")
	}
	if observation.CheckedAt.IsZero() {
		return false, errors.New("max_output_tokens capability checked_at is zero")
	}
	switch observation.State {
	case service.OpenAIResponsesMaxOutputTokensCapabilitySupported, service.OpenAIResponsesMaxOutputTokensCapabilityUnsupported:
	default:
		return false, service.ErrOpenAIResponsesMaxOutputTokensCapabilityInvalidOutcome
	}
	if r == nil || r.db == nil {
		return false, errors.New("max_output_tokens capability repository database is nil")
	}
	var status any
	if observation.StatusCode != nil {
		status = *observation.StatusCode
	}
	result, err := r.db.ExecContext(ctx, `
		INSERT INTO openai_responses_max_output_tokens_capabilities
		(account_id, upstream_fingerprint, effective_model, contract_version, config_generation, state, checked_at, last_status, last_outcome)
		SELECT a.id, $2, $3, $4, $5, $6, $7, $8, $9 FROM accounts a
		WHERE a.id = $1 AND a.deleted_at IS NULL AND a.platform IN ('openai', 'grok')
		ON CONFLICT (account_id, upstream_fingerprint, effective_model, contract_version, config_generation)
		DO UPDATE SET state = EXCLUDED.state, checked_at = EXCLUDED.checked_at,
			last_status = EXCLUDED.last_status, last_outcome = EXCLUDED.last_outcome, updated_at = NOW()
		WHERE openai_responses_max_output_tokens_capabilities.state = 'unknown'
		   OR EXCLUDED.state = 'supported'
	`, observation.Key.AccountID, observation.Key.UpstreamFingerprint, observation.Key.EffectiveModel,
		observation.Key.Version, observation.Key.ConfigGeneration, observation.State, observation.CheckedAt, status,
		strings.TrimSpace(observation.LastOutcome))
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n == 1, err
}
