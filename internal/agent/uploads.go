package agent

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bortolidiego/relay/shared/contracts"
)

const maxUploadSize = 15 << 20 // 15MB

var allowedMimePrefixes = []string{
	"image/",
	"application/pdf",
	"text/",
	"application/json",
	"application/csv",
	"application/vnd.ms-excel",
	"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
}

var allowedExtensions = map[string]bool{
	".txt": true, ".md": true, ".json": true, ".csv": true,
	".png": true, ".jpg": true, ".jpeg": true, ".webp": true, ".gif": true,
	".pdf": true,
}

// uploadRecord descreve um arquivo enviado pelo celular para a sessão.
type uploadRecord struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Mime     string `json:"mime"`
	Size     int64  `json:"size"`
	Path     string `json:"path"`      // caminho absoluto no Mac
	URL      string `json:"url"`       // /api/sessions/{id}/files/{fileId}
	Created  string `json:"created"`
	Caption  string `json:"caption,omitempty"`
	Session  string `json:"session_id"`
}

func (a *Agent) uploadDirForSession(sessionID string) string {
	return filepath.Join(relayHome(), "uploads", sanitizeFile(sessionID))
}

// registerUploadRoutes deve ser chamada dentro de handleSessionRoot para rotas de upload/files.
func (a *Agent) handleUploadOrFiles(w http.ResponseWriter, r *http.Request, sessionID string, extraPath string) {
	if extraPath == "upload" {
		if r.Method == http.MethodPost {
			a.handleSessionUpload(w, r, sessionID)
			return
		}
		http.Error(w, "método não permitido", http.StatusMethodNotAllowed)
		return
	}
	if extraPath == "files" {
		http.Error(w, "método não permitido", http.StatusMethodNotAllowed)
		return
	}
	// /api/sessions/{id}/files/{fileId}
	if strings.HasPrefix(extraPath, "files/") {
		fileID := strings.TrimPrefix(extraPath, "files/")
		if r.Method == http.MethodGet {
			a.handleSessionFile(w, r, sessionID, fileID)
			return
		}
		http.Error(w, "método não permitido", http.StatusMethodNotAllowed)
		return
	}
	http.NotFound(w, r)
}

func (a *Agent) handleSessionUpload(w http.ResponseWriter, r *http.Request, sessionID string) {
	sess, ok := a.registry.SessionByID(sessionID)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "sessão não encontrada"})
		return
	}

	if err := r.ParseMultipartForm(maxUploadSize); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("form inválido: %v", err)})
		return
	}
	defer r.MultipartForm.RemoveAll()

	file, header, err := r.FormFile("file")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "campo 'file' obrigatório"})
		return
	}
	defer file.Close()

	if header.Size > maxUploadSize {
		writeJSON(w, 413, map[string]string{"error": "arquivo excede 15MB"})
		return
	}
	if header.Size == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "arquivo vazio"})
		return
	}

	name := strings.TrimSpace(header.Filename)
	if name == "" {
		name = "anexo"
	}
	name = sanitizeUploadName(name)

	mimeType := detectMimeType(name, header.Header.Get("Content-Type"))
	if !isAllowedMime(mimeType, name) {
		writeJSON(w, http.StatusUnsupportedMediaType, map[string]string{"error": "tipo de arquivo não permitido"})
		return
	}

	fileID, err := randomUploadID()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	dir := a.uploadDirForSession(sessionID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	timestamp := time.Now().UTC().Format("20060102-150405")
	localName := fmt.Sprintf("%s-%s", timestamp, name)
	localPath := filepath.Join(dir, localName)
	if _, err := os.Stat(localPath); err == nil {
		// evita colisão rara
		localName = fmt.Sprintf("%s-%s-%s", timestamp, fileID[:8], name)
		localPath = filepath.Join(dir, localName)
	}

	out, err := os.Create(localPath)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	written, err := io.Copy(out, io.LimitReader(file, maxUploadSize+1))
	out.Close()
	if err != nil {
		_ = os.Remove(localPath)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if written > maxUploadSize {
		_ = os.Remove(localPath)
		writeJSON(w, 413, map[string]string{"error": "arquivo excede 15MB"})
		return
	}

	record := uploadRecord{
		ID:      fileID,
		Name:    name,
		Mime:    mimeType,
		Size:    written,
		Path:    localPath,
		URL:     fmt.Sprintf("/api/sessions/%s/files/%s", sessionID, fileID),
		Created: time.Now().UTC().Format(time.RFC3339),
		Caption: strings.TrimSpace(r.FormValue("caption")),
		Session: sessionID,
	}
	if err := a.saveUploadRecord(record); err != nil {
		_ = os.Remove(localPath)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	// Não injeta na sessão aqui: o PWA manda UMA mensagem (texto + caminhos).
	// Evita duplicar "[anexo]" + pergunta e perde a resposta no celular.
	_ = sess

	writeJSON(w, http.StatusOK, record)
}

func (a *Agent) handleSessionFile(w http.ResponseWriter, r *http.Request, sessionID, fileID string) {
	record, err := a.loadUploadRecord(sessionID, fileID)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "arquivo não encontrado"})
		return
	}

	data, err := os.ReadFile(record.Path)
	if err != nil {
		if os.IsNotExist(err) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "arquivo não encontrado"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	inline := strings.HasPrefix(record.Mime, "image/")
	disp := "attachment"
	if inline {
		disp = "inline"
	}
	w.Header().Set("Content-Type", record.Mime)
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(data)))
	w.Header().Set("Content-Disposition", fmt.Sprintf("%s; filename=\"%s\"", disp, record.Name))
	w.Header().Set("X-Relay-File-ID", record.ID)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func (a *Agent) uploadRecordPath(fileID string) string {
	return filepath.Join(relayHome(), "uploads", "records", fileID+".json")
}

func (a *Agent) saveUploadRecord(record uploadRecord) error {
	path := a.uploadRecordPath(record.ID)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(record)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}

func (a *Agent) loadUploadRecord(sessionID, fileID string) (uploadRecord, error) {
	var rec uploadRecord
	b, err := os.ReadFile(a.uploadRecordPath(fileID))
	if err != nil {
		return rec, err
	}
	if err := json.Unmarshal(b, &rec); err != nil {
		return rec, err
	}
	if rec.Session != sessionID {
		return rec, fmt.Errorf("sessão não corresponde")
	}
	return rec, nil
}

func formatAttachmentMessage(name, absPath, caption string) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("[anexo] %s (salvo em %s)", name, absPath))
	if caption != "" {
		b.WriteString("\n")
		b.WriteString(caption)
	}
	return b.String()
}

func randomUploadID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func sanitizeUploadName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "anexo"
	}
	base := filepath.Base(name)
	base = strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '.' || r == '-' || r == '_' {
			return r
		}
		return '_'
	}, base)
	if len(base) > 80 {
		ext := filepath.Ext(base)
		base = base[:80-len(ext)] + ext
	}
	if base == "" || base == "." {
		base = "anexo"
	}
	return base
}

func detectMimeType(name, contentType string) string {
	if contentType != "" {
		mt, _, err := mime.ParseMediaType(contentType)
		if err == nil && mt != "" && mt != "application/octet-stream" {
			return mt
		}
	}
	ext := strings.ToLower(filepath.Ext(name))
	switch ext {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".webp":
		return "image/webp"
	case ".gif":
		return "image/gif"
	case ".pdf":
		return "application/pdf"
	case ".txt", ".md":
		return "text/plain"
	case ".json":
		return "application/json"
	case ".csv":
		return "text/csv"
	}
	return contentType
}

func isAllowedMime(mt, name string) bool {
	if mt == "" {
		return false
	}
	for _, prefix := range allowedMimePrefixes {
		if strings.HasPrefix(mt, prefix) {
			return true
		}
	}
	ext := strings.ToLower(filepath.Ext(name))
	return allowedExtensions[ext]
}
