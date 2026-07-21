package qoder

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDefaultAuthDirUsesQoderCLICNConfigRoot(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	require.Equal(t, filepath.Join(home, ".qoder-cn", ".auth"), DefaultAuthDir())
}

func TestLoadLocalIdentityUsesQoderCLICNMachineIdentity(t *testing.T) {
	authDir := t.TempDir()
	machineID := "12345678-1234-4234-8234-123456789abc"
	userJSON, err := json.Marshal(AuthInfo{
		UID:                "user-1",
		SecurityOauthToken: "token-1",
	})
	require.NoError(t, err)
	encrypted, err := AESEncrypt(userJSON, []byte(machineID[:16]))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(authDir, "machine_id"), []byte(machineID+"\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(authDir, "user"), []byte(base64.StdEncoding.EncodeToString(encrypted)+"\n"), 0o600))

	_, machine, err := LoadLocalIdentity(authDir)

	require.NoError(t, err)
	require.Equal(t, machineID, machine.MachineID)
	require.Equal(t, machineID, machine.MachineToken)
	require.Equal(t, "5", machine.MachineType)
}
