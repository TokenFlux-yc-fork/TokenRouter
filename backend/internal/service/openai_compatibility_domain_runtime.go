package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

var (
	ErrOpenAICompatibilityDomainUnknown  = errors.New("openai compatibility domain is unknown")
	ErrOpenAICompatibilityDomainMismatch = errors.New("openai compatibility domain mismatch")
)

func nativeOpenAICompatibilityDomainRequired(ctx context.Context) bool {
	native, ok := OpenAINativeRemoteCompactionV2FromContext(ctx)
	return ok && native
}

// OpenAICompatibilityDomainAttempt holds request-local native-v2 compatibility
// state. Candidate checks never mutate shared continuation state; CommitDelivery
// is the only operation that publishes bindings.
type OpenAICompatibilityDomainAttempt struct {
	service            *OpenAIGatewayService
	groupID            *int64
	sessionHash        string
	expected           OpenAICompatibilityDomain
	hasExpected        bool
	candidate          OpenAICompatibilityDomain
	candidateAccountID int64
	hasCandidate       bool
}

// NewOpenAINativeCompatibilityDomainAttempt loads the domain of an existing
// continuation. A response continuation without a domain, or a legacy sticky
// session without a domain, is unknown and therefore fails closed. A completely
// new session remains unbound until successful delivery.
func (s *OpenAIGatewayService) NewOpenAINativeCompatibilityDomainAttempt(
	ctx context.Context,
	groupID *int64,
	previousResponseID string,
	sessionHash string,
) (*OpenAICompatibilityDomainAttempt, error) {
	attempt := &OpenAICompatibilityDomainAttempt{
		service:     s,
		groupID:     groupID,
		sessionHash: strings.TrimSpace(sessionHash),
	}
	if s == nil {
		return nil, fmt.Errorf("%w: state service is nil", ErrOpenAICompatibilityDomainUnknown)
	}
	store := s.getOpenAIWSStateStore()
	if store == nil {
		return nil, fmt.Errorf("%w: state store is unavailable", ErrOpenAICompatibilityDomainUnknown)
	}

	responseID := strings.TrimSpace(previousResponseID)
	if responseID != "" {
		domain, ok := store.GetResponseDomain(ctx, derefGroupID(groupID), responseID)
		if !ok || !domain.Valid() {
			return nil, fmt.Errorf("%w: response continuation has no domain", ErrOpenAICompatibilityDomainUnknown)
		}
		attempt.expected = domain
		attempt.hasExpected = true
	}

	if attempt.sessionHash == "" {
		return attempt, nil
	}
	if domain, ok := store.GetSessionDomain(ctx, derefGroupID(groupID), attempt.sessionHash); ok && domain.Valid() {
		if attempt.hasExpected && !attempt.expected.CompatibleWith(domain) {
			return nil, fmt.Errorf("%w: response and session domains differ", ErrOpenAICompatibilityDomainMismatch)
		}
		attempt.expected = domain
		attempt.hasExpected = true
		return attempt, nil
	}

	// A pre-existing account binding proves this is a continuation rather than a
	// new session. Missing domain metadata on that continuation is not reusable.
	if accountID, err := s.getStickySessionAccountID(ctx, groupID, attempt.sessionHash); err == nil && accountID > 0 {
		return nil, fmt.Errorf("%w: sticky session has no domain", ErrOpenAICompatibilityDomainUnknown)
	}
	return attempt, nil
}

// CheckCandidate resolves the domain after account model mapping and restricts
// every later failover candidate to the first/continuation domain.
func (a *OpenAICompatibilityDomainAttempt) CheckCandidate(
	ctx context.Context,
	account *Account,
	routingModel string,
) (OpenAICompatibilityDomain, error) {
	if a == nil {
		return OpenAICompatibilityDomain{}, fmt.Errorf("%w: attempt is nil", ErrOpenAICompatibilityDomainUnknown)
	}
	domain, err := ResolveOpenAINativeCompatibilityDomainForRequest(ctx, account, routingModel)
	if err != nil || !domain.Valid() {
		if err == nil {
			err = ErrOpenAICompatibilityDomainUnknown
		}
		return OpenAICompatibilityDomain{}, fmt.Errorf("%w: %v", ErrOpenAICompatibilityDomainUnknown, err)
	}
	if a.hasExpected && !a.expected.CompatibleWith(domain) {
		return OpenAICompatibilityDomain{}, ErrOpenAICompatibilityDomainMismatch
	}
	if a.hasCandidate && !a.candidate.CompatibleWith(domain) {
		return OpenAICompatibilityDomain{}, ErrOpenAICompatibilityDomainMismatch
	}
	if !a.hasCandidate {
		a.candidate = domain
		a.hasCandidate = true
	}
	a.candidateAccountID = account.ID
	return domain, nil
}

// CommitDelivery publishes session/ResponseID compatibility state only after
// the caller confirms that a complete native-v2 response reached the client.
// Failed attempts explicitly remove any eager HTTP ResponseID account binding.
func (a *OpenAICompatibilityDomainAttempt) CommitDelivery(
	ctx context.Context,
	accountID int64,
	responseID string,
	deliveryCommitted bool,
) error {
	if a == nil || a.service == nil {
		return fmt.Errorf("%w: attempt is unavailable", ErrOpenAICompatibilityDomainUnknown)
	}
	store := a.service.getOpenAIWSStateStore()
	if store == nil {
		return fmt.Errorf("%w: state store is unavailable", ErrOpenAICompatibilityDomainUnknown)
	}
	groupID := derefGroupID(a.groupID)
	responseID = strings.TrimSpace(responseID)
	if !deliveryCommitted || !a.hasCandidate || !a.candidate.Valid() || accountID <= 0 || accountID != a.candidateAccountID {
		if responseID != "" {
			return errors.Join(
				store.DeleteResponseDomain(ctx, groupID, responseID),
				store.DeleteResponseAccount(ctx, groupID, responseID),
			)
		}
		return nil
	}

	ttl := a.service.openAIWSResponseStickyTTL()
	if responseID != "" {
		if !store.BindResponseDomain(ctx, groupID, responseID, a.candidate, ttl) {
			return fmt.Errorf("%w: failed to bind response domain", ErrOpenAICompatibilityDomainUnknown)
		}
		if err := store.BindResponseAccount(ctx, groupID, responseID, accountID, ttl); err != nil {
			_ = store.DeleteResponseDomain(ctx, groupID, responseID)
			return err
		}
	}
	if a.sessionHash != "" {
		if !store.BindSessionDomain(ctx, groupID, a.sessionHash, a.candidate, a.service.openAIWSSessionStickyTTL()) {
			return fmt.Errorf("%w: failed to bind session domain", ErrOpenAICompatibilityDomainUnknown)
		}
		if err := a.service.BindStickySession(ctx, a.groupID, a.sessionHash, accountID); err != nil {
			_ = store.DeleteSessionDomain(ctx, groupID, a.sessionHash)
			return err
		}
	}
	return nil
}
