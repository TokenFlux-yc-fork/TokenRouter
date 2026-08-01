package repository

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/service"
)

type openAIResponsesInputTokensCapabilityRepository struct{ db *sql.DB }

var _ service.OpenAIResponsesInputTokensCapabilityRepository = (*openAIResponsesInputTokensCapabilityRepository)(nil)

func NewOpenAIResponsesInputTokensCapabilityRepository(db *sql.DB) service.OpenAIResponsesInputTokensCapabilityRepository {
	return &openAIResponsesInputTokensCapabilityRepository{db: db}
}

func (r *openAIResponsesInputTokensCapabilityRepository) GetExact(ctx context.Context, key service.OpenAIResponsesInputTokensCapabilityKey) (*service.OpenAIResponsesInputTokensCapabilityRecord, error) {
	if err := validateOpenAIResponsesInputTokensCapabilityKey(key); err != nil {
		return nil, err
	}
	if r == nil || r.db == nil {
		return nil, errors.New("input tokens capability repository database is nil")
	}
	row := r.db.QueryRowContext(ctx, `
		SELECT id, state, checked_at, last_status, last_outcome, created_at, updated_at
		FROM openai_responses_input_tokens_capabilities
		WHERE account_id = $1 AND upstream_fingerprint = $2 AND effective_model = $3 AND contract_version = $4 AND config_generation = $5
	`, key.AccountID, key.UpstreamFingerprint, key.EffectiveModel, key.ContractVersion, key.ConfigGeneration)
	var rec service.OpenAIResponsesInputTokensCapabilityRecord
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
	rec.State = service.OpenAIResponsesInputTokensCapabilityState(state)
	if checkedAt.Valid {
		rec.CheckedAt = &checkedAt.Time
	}
	if status.Valid {
		n := int(status.Int64)
		rec.LastStatus = &n
	}
	return &rec, nil
}

func (r *openAIResponsesInputTokensCapabilityRepository) EnsureUnknown(ctx context.Context, account *service.Account, key service.OpenAIResponsesInputTokensCapabilityKey) (bool, error) {
	if err := validateOpenAIResponsesInputTokensCapabilityKey(key); err != nil {
		return false, err
	}
	if account == nil || account.ID != key.AccountID || !account.IsOpenAICompatible() {
		return false, errors.New("input tokens capability requires exact OpenAI-compatible account")
	}
	if r == nil || r.db == nil {
		return false, errors.New("input tokens capability repository database is nil")
	}
	result, err := r.db.ExecContext(ctx, `
		INSERT INTO openai_responses_input_tokens_capabilities
		(account_id, upstream_fingerprint, effective_model, contract_version, config_generation, state)
		SELECT id, $2, $3, $4, $5, 'unknown' FROM accounts
		WHERE id = $1 AND deleted_at IS NULL
		ON CONFLICT (account_id, upstream_fingerprint, effective_model, contract_version, config_generation) DO NOTHING
	`, key.AccountID, key.UpstreamFingerprint, key.EffectiveModel, key.ContractVersion, key.ConfigGeneration)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n == 1, err
}

func (r *openAIResponsesInputTokensCapabilityRepository) UpsertObservation(ctx context.Context, account *service.Account, observation service.OpenAIResponsesInputTokensCapabilityObservation) (bool, error) {
	if err := validateOpenAIResponsesInputTokensCapabilityKey(observation.Key); err != nil {
		return false, err
	}
	if account == nil || account.ID != observation.Key.AccountID || !account.IsOpenAICompatible() {
		return false, errors.New("input tokens capability requires exact OpenAI-compatible account")
	}
	if observation.CheckedAt.IsZero() {
		return false, errors.New("input tokens capability checked_at is zero")
	}
	switch observation.State {
	case service.OpenAIResponsesInputTokensCapabilitySupported, service.OpenAIResponsesInputTokensCapabilityUnsupported, service.OpenAIResponsesInputTokensCapabilityScopeDenied:
	default:
		return false, service.ErrOpenAIResponsesInputTokensCapabilityInvalidOutcome
	}
	var status any
	if observation.StatusCode != nil {
		status = *observation.StatusCode
	}
	if r == nil || r.db == nil {
		return false, errors.New("input tokens capability repository database is nil")
	}
	result, err := r.db.ExecContext(ctx, `
		INSERT INTO openai_responses_input_tokens_capabilities
		(account_id, upstream_fingerprint, effective_model, contract_version, config_generation, state, checked_at, last_status, last_outcome)
		SELECT a.id, $2, $3, $4, $5, $6, $7, $8, $9 FROM accounts a
		WHERE a.id = $1 AND a.deleted_at IS NULL AND a.platform IN ('openai', 'grok')
		ON CONFLICT (account_id, upstream_fingerprint, effective_model, contract_version, config_generation)
		DO UPDATE SET state = EXCLUDED.state, checked_at = EXCLUDED.checked_at,
			last_status = EXCLUDED.last_status, last_outcome = EXCLUDED.last_outcome, updated_at = NOW()
		WHERE openai_responses_input_tokens_capabilities.state = 'unknown'
		   OR EXCLUDED.state = 'supported'
	`, observation.Key.AccountID, observation.Key.UpstreamFingerprint, observation.Key.EffectiveModel,
		observation.Key.ContractVersion, observation.Key.ConfigGeneration, observation.State, observation.CheckedAt, status,
		strings.TrimSpace(observation.LastOutcome))
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n == 1, err
}

func validateOpenAIResponsesInputTokensCapabilityKey(key service.OpenAIResponsesInputTokensCapabilityKey) error {
	if !key.Valid() {
		return errors.New("invalid openai input tokens capability key")
	}
	return nil
}
