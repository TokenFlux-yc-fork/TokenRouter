package qoder

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGetDataPolicyContextMapsQoderCNStatuses(t *testing.T) {
	tests := []struct {
		name       string
		status     string
		wantAgreed bool
	}{
		{name: "agree", status: "AGREE", wantAgreed: true},
		{name: "no record", status: "NO_RECORD", wantAgreed: true},
		{name: "disagree", status: "DISAGREE", wantAgreed: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			profile := MustProfileForSite(SiteCN)
			profile.GatewayBaseURL = "https://gateway.example"
			var captured *http.Request
			doer := func(req *http.Request) (*http.Response, error) {
				captured = req
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader(`{"success":true,"result":{"status":"` + tt.status + `"}}`)),
					Request:    req,
				}, nil
			}

			agreed, err := GetDataPolicyContext(context.Background(), &AuthIdentity{UID: "user-1"}, &MachineIdentity{
				MachineID:    "machine-1",
				MachineToken: "machine-1",
				MachineType:  "5",
			}, profile, doer)

			require.NoError(t, err)
			require.Equal(t, tt.wantAgreed, agreed)
			require.Equal(t, http.MethodGet, captured.Method)
			require.Equal(t, "/algo"+DataPolicyPath, captured.URL.Path)
			require.NotEmpty(t, captured.URL.Query().Get("requestId"))
			require.Equal(t, "2", captured.URL.Query().Get("version"))
			require.Empty(t, captured.URL.Query().Get("Encode"))
		})
	}
}

func TestGetDataPolicyContextRejectsUnknownOrFailedStatus(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "unknown", body: `{"success":true,"result":{"status":"PENDING"}}`},
		{name: "missing", body: `{"success":true,"result":{}}`},
		{name: "rejected", body: `{"success":false,"result":{"status":"DISAGREE"}}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			profile := MustProfileForSite(SiteCN)
			profile.GatewayBaseURL = "https://gateway.example"
			doer := func(req *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader(tt.body)),
					Request:    req,
				}, nil
			}

			_, err := GetDataPolicyContext(
				context.Background(),
				&AuthIdentity{UID: "user-1"},
				&MachineIdentity{MachineID: "machine-1"},
				profile,
				doer,
			)
			require.Error(t, err)
		})
	}
}
