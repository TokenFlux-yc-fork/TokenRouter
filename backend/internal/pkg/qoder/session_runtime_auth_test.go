package qoder

import (
	"encoding/base64"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNewSessionRuntimeAuthPayloadMatchesQoderCLICNProjection(t *testing.T) {
	dataPolicyAgreed := false
	identity := &AuthIdentity{
		Name:               "must-not-leak",
		AID:                "aid-must-not-leak",
		UID:                "uid-1",
		YxUID:              "yx-must-not-leak",
		OrganizationID:     "org-1",
		OrganizationName:   "org-name-must-not-leak",
		OrganizationTags:   []string{" Enterprise ", "CN", "Enterprise"},
		UserType:           "personal_standard",
		SecurityOauthToken: "security-token-must-not-leak",
		RefreshToken:       "refresh-token-must-not-leak",
		DataPolicyAgreed:   &dataPolicyAgreed,
	}

	session, err := NewSessionWithKey(identity, &MachineIdentity{MachineID: "machine-1"}, []byte("abcdefghijklmnop"))
	require.NoError(t, err)

	encrypted, err := base64.StdEncoding.DecodeString(session.Info)
	require.NoError(t, err)
	plaintext, err := AESDecrypt(encrypted, session.TempKey)
	require.NoError(t, err)

	var payload map[string]any
	require.NoError(t, json.Unmarshal(plaintext, &payload))
	require.Equal(t, map[string]any{
		"uid":                "uid-1",
		"organization_id":    "org-1",
		"organization_tags":  []any{"Enterprise", "CN"},
		"data_policy_agreed": false,
	}, payload)
}

func TestBuildAuthPayloadJSONOmitsUnknownDataPolicy(t *testing.T) {
	payload, err := BuildAuthPayloadJSON(&AuthIdentity{UID: "uid-1"})
	require.NoError(t, err)
	require.JSONEq(t, `{"uid":"uid-1"}`, string(payload))
}

func TestNewSessionDefaultsGatewayDataPolicyToDisagree(t *testing.T) {
	session, err := NewSessionWithKey(
		&AuthIdentity{UID: "uid-1"},
		&MachineIdentity{MachineID: "machine-1"},
		[]byte("abcdefghijklmnop"),
	)
	require.NoError(t, err)
	require.Equal(t, "disagree", session.DataPolicy)

	agreed := true
	session, err = NewSessionWithKey(
		&AuthIdentity{UID: "uid-1", DataPolicyAgreed: &agreed},
		&MachineIdentity{MachineID: "machine-1"},
		[]byte("abcdefghijklmnop"),
	)
	require.NoError(t, err)
	require.Equal(t, "agree", session.DataPolicy)
}
