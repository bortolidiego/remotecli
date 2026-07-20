package sandbox

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCanonicalPathInsideBase(t *testing.T) {
	base := t.TempDir()
	realBase, err := filepath.EvalSymlinks(base)
	require.NoError(t, err)
	p, err := CanonicalPath(base, "docs/readme.md")
	require.NoError(t, err)
	require.Equal(t, filepath.Join(realBase, "docs", "readme.md"), p)
}

func TestCanonicalPathBlocksTraversal(t *testing.T) {
	base := t.TempDir()
	_, err := CanonicalPath(base, "../etc/passwd")
	require.Error(t, err)
	_, err = CanonicalPath(base, "a/../../etc")
	require.Error(t, err)
}

func TestCanonicalPathAllowsNormalDotNames(t *testing.T) {
	base := t.TempDir()
	for _, name := range []string{"docs/v1..draft.txt", "notes/.../file.txt", ".gitignore"} {
		p, err := CanonicalPath(base, name)
		require.NoError(t, err)
		require.True(t, strings.HasSuffix(filepath.ToSlash(p), name))
	}
}

func TestCanonicalPathBlocksGit(t *testing.T) {
	base := t.TempDir()
	gitDir := filepath.Join(base, ".git", "config")
	require.NoError(t, os.MkdirAll(filepath.Dir(gitDir), 0755))
	require.NoError(t, os.WriteFile(gitDir, []byte("x"), 0644))
	_, err := CanonicalPath(base, ".git/config")
	require.Error(t, err)
	require.True(t, strings.Contains(err.Error(), "bloqueado") || strings.Contains(err.Error(), "fora"))
}

func TestCanonicalPathBlocksEnvAndKeys(t *testing.T) {
	base := t.TempDir()
	for _, name := range []string{".env", ".env.local", "id_rsa", "deploy.pem", "private.key"} {
		p := filepath.Join(base, name)
		require.NoError(t, os.WriteFile(p, []byte("secret"), 0644))
		_, err := CanonicalPath(base, name)
		require.Error(t, err)
		require.Contains(t, err.Error(), "bloqueado")
	}
}

func TestCanonicalPathBlocksSymlinkOutside(t *testing.T) {
	base := t.TempDir()
	outside := t.TempDir()
	outsideFile := filepath.Join(outside, "secret.txt")
	require.NoError(t, os.WriteFile(outsideFile, []byte("secret"), 0644))
	link := filepath.Join(base, "link")
	require.NoError(t, os.Symlink(outsideFile, link))
	_, err := CanonicalPath(base, "link")
	require.Error(t, err)
	require.Contains(t, err.Error(), "fora")
}

func TestCanonicalPathBlocksSymlinkParentOutside(t *testing.T) {
	base := t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(base, "out")
	require.NoError(t, os.Symlink(outside, link))
	_, err := CanonicalPath(base, "out/new.txt")
	require.Error(t, err)
	require.Contains(t, err.Error(), "fora")
}

func TestReadFileLimit(t *testing.T) {
	base := t.TempDir()
	small := filepath.Join(base, "small.txt")
	require.NoError(t, os.WriteFile(small, []byte("ok"), 0644))
	data, err := ReadFile(base, "small.txt")
	require.NoError(t, err)
	require.Equal(t, "ok", string(data))
}

func TestReadFileTooBig(t *testing.T) {
	base := t.TempDir()
	huge := filepath.Join(base, "huge.bin")
	require.NoError(t, os.WriteFile(huge, make([]byte, MaxFileSize+1), 0644))
	_, err := ReadFile(base, "huge.bin")
	require.Error(t, err)
}

func TestNormalizeCoordRejectsTraversal(t *testing.T) {
	_, err := NormalizeCoord("../a")
	require.Error(t, err)
	_, err = NormalizeCoord("a", "..", "b")
	require.Error(t, err)
	c, err := NormalizeCoord("a", "b", "c.txt")
	require.NoError(t, err)
	require.Equal(t, "a/b/c.txt", c)
	_, err = NormalizeCoord("a", "..", "b")
	require.Error(t, err)
}

func TestParentDir(t *testing.T) {
	base := t.TempDir()
	realBase, err := filepath.EvalSymlinks(base)
	require.NoError(t, err)
	dir := filepath.Join(base, "uploads")
	require.NoError(t, os.MkdirAll(dir, 0755))
	p, err := ParentDir(base, "uploads/new")
	require.NoError(t, err)
	require.Equal(t, filepath.Join(realBase, "uploads"), p)
}
