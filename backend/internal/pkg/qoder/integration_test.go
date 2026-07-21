package qoder

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func qoderCLICNIntegrationPayload(prompt string) map[string]any {
	requestID := GenerateRequestID()
	return map[string]any{
		"stream":           true,
		"session_id":       GenerateRequestID(),
		"request_id":       requestID,
		"chat_record_id":   requestID,
		"request_set_id":   GenerateRequestID(),
		"agent_id":         "agent_common",
		"task_id":          "common",
		"session_type":     "qoderclicn",
		"aliyun_user_type": "",
		"system":           "Reply briefly.",
		"model_config": map[string]any{
			"key":    "auto",
			"source": "system",
			"format": "openai",
		},
		"messages": []map[string]any{
			{
				"role":     "user",
				"content":  prompt,
				"contents": []map[string]any{{"type": "text", "text": prompt}},
			},
		},
		"parameters": map[string]any{"max_tokens": 10},
		"chat_context": map[string]any{
			"text": prompt,
			"extra": map[string]any{
				"modelConfig":     map[string]any{"key": "auto"},
				"originalContent": prompt,
			},
		},
		"business": map[string]any{
			"product":  "cli",
			"type":     "agent",
			"version":  CNClientVersion,
			"stage":    "start",
			"id":       GenerateRequestID(),
			"name":     prompt,
			"begin_at": time.Now().UnixMilli(),
		},
	}
}

func TestQoderCLICNIntegrationPayload(t *testing.T) {
	payload := qoderCLICNIntegrationPayload("Say hi.")

	require.Equal(t, "qoderclicn", payload["session_type"])
	_, ok := payload["system"].(string)
	require.True(t, ok)
	chatContext, ok := payload["chat_context"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "Say hi.", chatContext["text"])
	extra, ok := chatContext["extra"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "Say hi.", extra["originalContent"])
	business, ok := payload["business"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "cli", business["product"])
	require.Equal(t, "agent", business["type"])
	require.Equal(t, CNClientVersion, business["version"])
}

func requireLocalQoderAuth(t *testing.T, authDir string) {
	t.Helper()
	for _, name := range []string{"machine_id", "user"} {
		if _, err := os.Stat(filepath.Join(authDir, name)); err != nil {
			if os.IsNotExist(err) {
				t.Skip("local qoderclicn auth not found")
			}
			t.Fatalf("inspect local qoderclicn auth metadata: %v", err)
		}
	}
}

// TestRealAPI performs an end-to-end test against the real Qoder API.
func TestRealAPI(t *testing.T) {
	if os.Getenv("QODER_RUN_REAL_API_TESTS") != "1" {
		t.Skip("set QODER_RUN_REAL_API_TESTS=1 to run real Qoder API integration test")
	}
	authDir := DefaultAuthDir()
	if authDir == "" {
		t.Skip("no home directory")
	}
	requireLocalQoderAuth(t, authDir)

	identity, machine, err := LoadLocalIdentity(authDir)
	if err != nil {
		t.Fatalf("LoadLocalIdentity: %v", err)
	}
	t.Log("Loaded local Qoder auth metadata")
	if identity == nil || strings.TrimSpace(identity.SecurityOauthToken) == "" {
		t.Fatal("local qoderclicn auth does not contain a usable identity")
	}
	if machine.MachineID == "" || machine.MachineToken != machine.MachineID || machine.MachineType != "5" {
		t.Fatal("local Qoder auth does not contain a valid qoderclicn machine identity")
	}

	session, err := NewSession(identity, machine)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Logf("Session cosy_key length: %d", len(session.CosyKey))

	payload := qoderCLICNIntegrationPayload("Say hi.")
	payload["aliyun_user_type"] = identity.UserType

	bodyJSON, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal qoderclicn payload: %v", err)
	}
	client := NewClient(APIBaseURL)

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	resp, err := client.StreamRequestContext(ctx, session, "", bodyJSON, map[string]string{
		"x-model-key":    "auto",
		"x-model-source": "system",
	})
	if err != nil {
		t.Fatalf("StreamRequest: %v", err)
	}
	t.Logf("Response status: %d", resp.StatusCode)

	textParts, done := collectStream(ctx, resp, t)
	result := strings.TrimSpace(strings.Join(textParts, ""))
	t.Logf("Result: %q (done=%v)", result, done)

	if !done {
		t.Fatal("qoderclicn stream ended without [DONE]")
	}
	if result == "" {
		t.Error("Expected non-empty response")
	} else {
		fmt.Printf("✓ SUCCESS: %q\n", result)
	}
}

func collectStream(ctx context.Context, resp *http.Response, t *testing.T) ([]string, bool) {
	t.Helper()
	var textParts []string

	for event := range StreamEvents(resp) {
		select {
		case <-ctx.Done():
			return textParts, false
		default:
		}
		if event.Type == "text_delta" && event.Text != "" {
			textParts = append(textParts, event.Text)
		} else if event.Type == "error" {
			t.Logf("SSE error: %s", event.Text)
			return textParts, false
		} else if event.IsDone {
			return textParts, true
		}
	}
	return textParts, false
}

// TestReadLocalAuthFromDisk tests reading and decrypting local Qoder auth.
func TestReadLocalAuthFromDisk(t *testing.T) {
	if os.Getenv("QODER_RUN_LOCAL_AUTH_TESTS") != "1" && os.Getenv("QODER_RUN_REAL_API_TESTS") != "1" {
		t.Skip("set QODER_RUN_LOCAL_AUTH_TESTS=1 to run local Qoder auth import test")
	}
	authDir := DefaultAuthDir()
	if authDir == "" {
		t.Skip("no home directory")
	}
	requireLocalQoderAuth(t, authDir)

	info, err := ReadLocalAuth(authDir)
	if err != nil {
		t.Fatalf("ReadLocalAuth: %v", err)
	}

	if info.UID == "" {
		t.Error("expected non-empty UID")
	}
	if info.AccessToken == "" && info.SecurityOauthToken == "" {
		t.Error("expected non-empty token")
	}
	t.Log("Loaded local Qoder auth metadata")
}
