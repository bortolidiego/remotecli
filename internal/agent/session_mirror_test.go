package agent

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func withTestRelayHome(t *testing.T, dir string) func() {
	old := relayHomeFn
	relayHomeFn = func() string { return dir }
	t.Cleanup(func() { relayHomeFn = old })
	return func() {}
}

func TestCleanTerminalText(t *testing.T) {
	cases := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "remove ANSI",
			input:    "\x1b[32mhello\x1b[0m \x1b[1mworld\x1b[0m",
			expected: "hello world",
		},
		{
			name:     "collapse spaces",
			input:    "a    b\tc   d",
			expected: "a b c d",
		},
		{
			name:     "strip box drawing",
			input:    "┌─linha─┐\n│valor│\n└─────┘",
			expected: "linha\nvalor",
		},
		{
			name:     "strip decorative line",
			input:    "antes\n────────\ndepois",
			expected: "antes\ndepois",
		},
		{
			name:     "trim and normalize newlines",
			input:    "\n\n\nfoo\n\n\n\nbar\n\n",
			expected: "foo\nbar",
		},
		{
			name:     "No connection fica inalterado semanticamente",
			input:    "No connection",
			expected: "No connection",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := cleanTerminalText(tc.input)
			require.Equal(t, tc.expected, got)
		})
	}

	t.Run("limita a 8k preferindo o final", func(t *testing.T) {
		big := make([]byte, 10000)
		for i := range big {
			big[i] = 'a'
		}
		big[len(big)-10] = '\n'
		for i := len(big) - 9; i < len(big); i++ {
			big[i] = 'z'
		}
		got := cleanTerminalText(string(big))
		require.LessOrEqual(t, len(got), maxSnapshotLen)
		require.Contains(t, got, "zzzzzzzzz")
	})
}

func TestWriteReadSnapshot(t *testing.T) {
	dir := t.TempDir()
	withTestRelayHome(t, dir)

	name := "agent-test"
	require.NoError(t, writeSnapshot(name, "linha 1\nlinha 2"))
	got := readSnapshot(name)
	require.Equal(t, "linha 1\nlinha 2", got)

	// Sobrescreve.
	require.NoError(t, writeSnapshot(name, "novo"))
	require.Equal(t, "novo", readSnapshot(name))

	// Leitura inexistente retorna vazio.
	require.Equal(t, "", readSnapshot("nope"))
}

func TestWatchTargetsMerge(t *testing.T) {
	dir := t.TempDir()
	withTestRelayHome(t, dir)

	require.NoError(t, RegisterWatchTarget("foo", "maestri", "/tmp/s1"))
	require.NoError(t, RegisterWatchTarget("bar", "maestri", ""))
	require.NoError(t, RegisterWatchTarget("foo", "", "/tmp/s2"))

	targets, err := loadWatchTargets()
	require.NoError(t, err)
	require.Len(t, targets, 2)
	require.Equal(t, "foo", targets[0].Name)
	require.Equal(t, "maestri", targets[0].CLI)
	require.Equal(t, "/tmp/s2", targets[0].Socket)
}

func TestSnapshotTimestamp(t *testing.T) {
	dir := t.TempDir()
	withTestRelayHome(t, dir)

	name := "ts-test"
	require.Equal(t, "", snapshotTimestamp(name))
	require.NoError(t, writeSnapshot(name, "x"))
	ts := snapshotTimestamp(name)
	require.NotEmpty(t, ts)
	parsed, err := time.Parse(time.RFC3339, ts)
	require.NoError(t, err)
	require.WithinDuration(t, time.Now().UTC(), parsed, 5*time.Second)
}

func TestTerminalDelta(t *testing.T) {
	require.Equal(t, "mundo", terminalDelta("", "mundo"))
	require.Equal(t, "", terminalDelta("igual", "igual"))
	require.Equal(t, "depois", terminalDelta("antes\nmeio", "antes\nmeio\ndepois"))
	require.Equal(t, "novo", terminalDelta("velho", "velho\nnovo"))
}

func TestExtractAssistantReply(t *testing.T) {
	cases := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "frase humana curta",
			input:    "Perfeito.\nVou fazer isso agora.",
			expected: "Perfeito.\nVou fazer isso agora.",
		},
		{
			name:     "remove ruído de hooks",
			input:    "hooks: user_prompt_submit\nThought for 200ms\nPerfeito.",
			expected: "Perfeito.",
		},
		{
			name:     "só ruído vira fallback",
			input:    "hooks: user_prompt_submit\nThought for 200ms\n149K / 500K",
			expected: "Resposta no Mac — toque Ver terminal se quiser o raw",
		},
		{
			name:     "remove paths",
			input:    "~/.maestri/config.json\nClaro, aqui está.",
			expected: "Claro, aqui está.",
		},
		{
			name:     "vazio vira fallback",
			input:    "",
			expected: "Resposta no Mac — toque Ver terminal se quiser o raw",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := extractAssistantReply(tc.input)
			require.Equal(t, tc.expected, got)
		})
	}
}

func TestSanitizeUploadName(t *testing.T) {
	require.Equal(t, "foto.png", sanitizeUploadName("foto.png"))
	require.Equal(t, "relat_rio_final_.pdf", sanitizeUploadName("relatório final!.pdf"))
	require.Equal(t, "anexo", sanitizeUploadName(""))
}
