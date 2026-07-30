package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func readOpenAINativeCompactionFixture(t *testing.T, name string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", "openai_native_compaction_v2", name+".sse"))
	require.NoError(t, err)
	return string(body)
}

func TestOpenAINativeCompactionFixturesAreSyntheticAndParseable(t *testing.T) {
	fixtures := []string{
		"valid_one",
		"zero_compaction",
		"two_compactions",
		"malformed_compaction",
		"added_only",
		"terminal_output_only",
		"failed_terminal",
		"incomplete_eof",
		"duplicate_terminal",
		"post_terminal_frame",
	}
	for _, name := range fixtures {
		t.Run(name, func(t *testing.T) {
			body := readOpenAINativeCompactionFixture(t, name)
			lower := strings.ToLower(body)
			require.NotContains(t, lower, "authorization")
			require.NotContains(t, lower, "api_key")
			require.NotContains(t, lower, "oauth")
			require.NotContains(t, lower, "sk-")

			payloads := 0
			forEachOpenAISSEDataPayload(body, func(payload []byte) {
				payloads++
				require.True(t, gjson.ValidBytes(payload), "fixture payload must be valid JSON: %s", payload)
			})
			require.Positive(t, payloads)
		})
	}
}

func TestLegacyCompactReconstructionStillAcceptsAddedOnlyFixture(t *testing.T) {
	outputJSON, ok := reconstructResponseOutputFromSSE(readOpenAINativeCompactionFixture(t, "added_only"))
	require.True(t, ok)
	items := gjson.ParseBytes(outputJSON).Array()
	require.Len(t, items, 1)
	require.Equal(t, "compaction", items[0].Get("type").String())
	require.Equal(t, "fixture-added-only", items[0].Get("encrypted_content").String())
}
