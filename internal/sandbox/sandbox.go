package sandbox

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const MaxFileSize = 25 * 1024 * 1024 // 25 MB

var blockedSegments = []string{
	".git",
	".env",
	".env.",
	".ssh",
	".gnupg",
	".aws",
	".kube",
	"id_rsa",
	"id_ed25519",
	"id_ecdsa",
	".pem",
	".p12",
	".pfx",
	".key",
	"credentials",
	"secrets",
	"token",
}

// CanonicalPath retorna caminho absoluto, verifica se está dentro de base e aplica bloqueios.
// Rejeita travessia, não a normaliza.
func CanonicalPath(base, rel string) (string, error) {
	if base == "" {
		return "", errors.New("base não definida")
	}
	absBase, err := filepath.Abs(base)
	if err != nil {
		return "", fmt.Errorf("resolver base: %w", err)
	}
	realBase, err := filepath.EvalSymlinks(absBase)
	if err != nil {
		return "", fmt.Errorf("resolver base real: %w", err)
	}
	if rel == "" || rel == "." {
		return realBase, nil
	}
	if !isCleanRelative(rel) {
		return "", fmt.Errorf("caminho inválido: %s", rel)
	}
	if isBlockedRel(rel) {
		return "", fmt.Errorf("caminho bloqueado: %s", rel)
	}
	resolved, err := resolveInside(realBase, filepath.Clean(rel))
	if err != nil {
		return "", err
	}
	if !insideBase(realBase, resolved) {
		return "", fmt.Errorf("caminho fora do sandbox: %s", rel)
	}
	if isBlockedPath(resolved) {
		return "", fmt.Errorf("caminho bloqueado: %s", rel)
	}
	return resolved, nil
}

func isCleanRelative(rel string) bool {
	if rel == "" || rel == "." {
		return true
	}
	if filepath.IsAbs(rel) || strings.Contains(rel, "\x00") || strings.Contains(rel, "\\") {
		return false
	}
	for _, seg := range strings.Split(rel, string(filepath.Separator)) {
		if seg == ".." {
			return false
		}
	}
	return true
}

func resolveInside(realBase, rel string) (string, error) {
	current := realBase
	parts := strings.Split(rel, string(filepath.Separator))
	for i, part := range parts {
		if part == "" || part == "." {
			continue
		}
		next := filepath.Join(current, part)
		resolved, err := filepath.EvalSymlinks(next)
		if err == nil {
			if !insideBase(realBase, resolved) {
				return "", fmt.Errorf("caminho fora do sandbox: %s", rel)
			}
			current = resolved
			continue
		}
		if os.IsNotExist(err) {
			remaining := append([]string{current}, parts[i:]...)
			return filepath.Join(remaining...), nil
		}
		return "", fmt.Errorf("symlink: %w", err)
	}
	return current, nil
}

func insideBase(base, path string) bool {
	rel, err := filepath.Rel(base, path)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

func isBlockedPath(path string) bool {
	for _, seg := range strings.Split(path, string(filepath.Separator)) {
		if isBlockedSegment(seg) {
			return true
		}
	}
	return false
}

func isBlockedRel(rel string) bool {
	for _, seg := range strings.Split(rel, string(filepath.Separator)) {
		if isBlockedSegment(seg) {
			return true
		}
	}
	return false
}

func isBlockedSegment(seg string) bool {
	lower := strings.ToLower(seg)
	if lower == "" {
		return false
	}
	if lower == ".git" || strings.HasPrefix(lower, ".env") {
		return true
	}
	for _, blocked := range blockedSegments {
		if blocked == ".git" || blocked == ".env" || blocked == ".env." {
			continue
		}
		if strings.HasPrefix(blocked, ".") {
			if lower == blocked || strings.HasSuffix(lower, blocked) {
				return true
			}
			continue
		}
		if lower == blocked {
			return true
		}
	}
	return false
}

// ReadFile lê arquivo dentro do sandbox com limite de tamanho.
func ReadFile(base, rel string) ([]byte, error) {
	p, err := CanonicalPath(base, rel)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(p)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return nil, fmt.Errorf("%s é diretório", rel)
	}
	if info.Size() > MaxFileSize {
		return nil, fmt.Errorf("arquivo excede %d bytes", MaxFileSize)
	}
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, MaxFileSize))
}

// ParentDir retorna o diretório real dentro do sandbox para upload futuro.
func ParentDir(base, rel string) (string, error) {
	if rel == "" || rel == "." {
		p, err := CanonicalPath(base, "")
		if err != nil {
			return "", err
		}
		info, err := os.Stat(p)
		if err != nil {
			return "", err
		}
		if !info.IsDir() {
			return "", fmt.Errorf("base não é diretório")
		}
		return p, nil
	}
	p, err := CanonicalPath(base, rel)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(p)
	if err == nil {
		if !info.IsDir() {
			return "", fmt.Errorf("%s é arquivo, esperado diretório", rel)
		}
		return p, nil
	}
	if os.IsNotExist(err) {
		parent := filepath.Dir(p)
		info, err = os.Stat(parent)
		if err != nil {
			return "", err
		}
		if !info.IsDir() {
			return "", fmt.Errorf("pai de %s não é diretório", rel)
		}
		return parent, nil
	}
	return "", err
}

// NormalizeCoord valida e retorna coordenadas canônicas; rejeita travessia.
func NormalizeCoord(parts ...string) (string, error) {
	var out []string
	for _, part := range parts {
		if part == "" || part == "." {
			continue
		}
		if filepath.IsAbs(part) || strings.Contains(part, "\x00") || strings.Contains(part, "\\") {
			return "", fmt.Errorf("caminho inválido: %s", part)
		}
		for _, seg := range strings.Split(part, string(filepath.Separator)) {
			if seg == "" || seg == "." {
				continue
			}
			if seg == ".." {
				return "", fmt.Errorf("travessia rejeitada: %s", part)
			}
			out = append(out, seg)
		}
	}
	res := filepath.Join(out...)
	if res == "." {
		res = ""
	}
	return res, nil
}
