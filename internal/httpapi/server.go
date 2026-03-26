package httpapi

import (
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"os"
	"strconv"
	"strings"

	"github.com/gorilla/mux"

	"pii-masker/internal/config"
	"pii-masker/internal/core"
	"pii-masker/internal/document"
	"pii-masker/internal/service"
	"pii-masker/internal/upstage"
)

type Server struct {
	config  config.Config
	service *service.Service
	router  *mux.Router
}

func New(cfg config.Config, svc *service.Service) *Server {
	server := &Server{
		config:  cfg,
		service: svc,
		router:  mux.NewRouter(),
	}
	server.routes()
	return server
}

func (s *Server) Handler() http.Handler {
	return s.router
}

func (s *Server) Mount(pathPrefix string, handler http.Handler) {
	s.router.PathPrefix(pathPrefix).Handler(handler)
}

func (s *Server) routes() {
	s.router.HandleFunc("/", s.handleIndex).Methods(http.MethodGet)
	s.router.HandleFunc("/ui", s.handleIndex).Methods(http.MethodGet)
	s.router.HandleFunc("/v1/health", s.handleHealth).Methods(http.MethodGet)
	s.router.HandleFunc("/v1/config/public", s.handlePublicConfig).Methods(http.MethodGet)
	s.router.HandleFunc("/v1/test-connection", s.handleTestConnection).Methods(http.MethodPost)
	s.router.HandleFunc("/v1/mask", s.handleMask).Methods(http.MethodPost)
	s.router.HandleFunc("/v1/jobs", s.handleCreateJob).Methods(http.MethodPost)
	s.router.HandleFunc("/v1/jobs/{job_id}", s.handleGetJob).Methods(http.MethodGet)
	s.router.HandleFunc("/v1/jobs/{job_id}/result", s.handleGetJobResult).Methods(http.MethodGet)
	s.router.HandleFunc("/v1/history", s.handleHistory).Methods(http.MethodGet)
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

func (s *Server) handlePublicConfig(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"limits": map[string]any{
			"max_file_size_bytes": s.config.Limits.MaxFileSizeBytes,
			"max_pages":           s.config.Limits.MaxPages,
		},
		"supported_types": s.config.Limits.SupportedMIMEs,
		"async_enabled":   true,
		"response_mode":   "multipart/mixed",
	})
}

func (s *Server) handleTestConnection(w http.ResponseWriter, r *http.Request) {
	status, err := s.service.TestConnection(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, &core.APIError{
			Code:    "connection_test_failed",
			Message: err.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (s *Server) handleMask(w http.ResponseWriter, r *http.Request) {
	input, err := s.readProcessInput(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, &core.APIError{
			Code:    "invalid_request",
			Message: err.Error(),
		})
		return
	}

	metadata, maskedContent, processErr := s.service.ProcessSync(r.Context(), input)
	if processErr != nil {
		statusCode := http.StatusBadGateway
		if metadata != nil && metadata.Error != nil && metadata.Error.Code == "processing_failed" {
			statusCode = http.StatusBadRequest
		}
		writeJSON(w, statusCode, metadata)
		return
	}

	metadata.Output.Inline = true
	s.writeMultipartResponse(w, metadata, maskedContent)
}

func (s *Server) handleCreateJob(w http.ResponseWriter, r *http.Request) {
	input, err := s.readProcessInput(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, &core.APIError{
			Code:    "invalid_request",
			Message: err.Error(),
		})
		return
	}

	job, err := s.service.CreateJob(r.Context(), input)
	if err != nil {
		writeError(w, http.StatusInternalServerError, &core.APIError{
			Code:    "job_create_failed",
			Message: err.Error(),
		})
		return
	}
	job.Metadata.Output.DownloadURL = service.JoinPublicURL(s.config.Server.PublicBaseURL, "v1", "jobs", job.ID, "result")
	writeJSON(w, http.StatusAccepted, job.Metadata)
}

func (s *Server) handleGetJob(w http.ResponseWriter, r *http.Request) {
	jobID := mux.Vars(r)["job_id"]
	job, ok, err := s.service.GetJob(jobID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, &core.APIError{
			Code:    "job_lookup_failed",
			Message: err.Error(),
		})
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, &core.APIError{
			Code:    "job_not_found",
			Message: "작업을 찾을 수 없습니다.",
		})
		return
	}
	if job.OutputPath != "" {
		job.Metadata.Output.DownloadURL = service.JoinPublicURL(s.config.Server.PublicBaseURL, "v1", "jobs", job.ID, "result")
	}
	writeJSON(w, http.StatusOK, job.Metadata)
}

func (s *Server) handleGetJobResult(w http.ResponseWriter, r *http.Request) {
	jobID := mux.Vars(r)["job_id"]
	job, ok, err := s.service.GetJob(jobID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, &core.APIError{
			Code:    "job_lookup_failed",
			Message: err.Error(),
		})
		return
	}
	if !ok || job.OutputPath == "" {
		writeError(w, http.StatusNotFound, &core.APIError{
			Code:    "job_result_not_found",
			Message: "결과 파일을 찾을 수 없습니다.",
		})
		return
	}

	content, err := os.ReadFile(job.OutputPath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, &core.APIError{
			Code:    "result_read_failed",
			Message: err.Error(),
		})
		return
	}

	w.Header().Set("Content-Type", job.Metadata.Output.MIMEType)
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, job.Metadata.Output.FileName))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(content)
}

func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	limit := 20
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			limit = parsed
		}
	}
	items, err := s.service.ListJobs(limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, &core.APIError{
			Code:    "history_lookup_failed",
			Message: err.Error(),
		})
		return
	}

	response := make([]core.ProcessMetadata, 0, len(items))
	for _, item := range items {
		if item.OutputPath != "" {
			item.Metadata.Output.DownloadURL = service.JoinPublicURL(s.config.Server.PublicBaseURL, "v1", "jobs", item.ID, "result")
		}
		response = append(response, item.Metadata)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": response})
}

func (s *Server) readProcessInput(r *http.Request) (service.ProcessInput, error) {
	if err := r.ParseMultipartForm(s.config.Limits.MaxFileSizeBytes + 1024*1024); err != nil {
		return service.ProcessInput{}, fmt.Errorf("invalid multipart form: %w", err)
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		return service.ProcessInput{}, fmt.Errorf("file field is required")
	}
	defer file.Close()

	content, err := io.ReadAll(io.LimitReader(file, s.config.Limits.MaxFileSizeBytes+1))
	if err != nil {
		return service.ProcessInput{}, fmt.Errorf("failed to read uploaded file: %w", err)
	}

	attachment := document.NewAttachment(header.Filename, header.Header.Get("Content-Type"), content)
	options := upstage.ParseOptions{
		Model:   strings.TrimSpace(r.FormValue("model")),
		Lang:    strings.TrimSpace(r.FormValue("lang")),
		Schema:  strings.TrimSpace(r.FormValue("schema")),
		Verbose: strings.EqualFold(strings.TrimSpace(r.FormValue("verbose")), "true"),
	}

	return service.ProcessInput{
		Attachment: attachment,
		Options:    options,
	}, nil
}

func (s *Server) writeMultipartResponse(w http.ResponseWriter, metadata *core.ProcessMetadata, fileContent []byte) {
	writer := multipart.NewWriter(w)
	w.Header().Set("Content-Type", "multipart/mixed; boundary="+writer.Boundary())
	w.WriteHeader(http.StatusOK)

	jsonHeader := textPartHeader("metadata.json", "application/json; charset=utf-8")
	jsonPart, _ := writer.CreatePart(jsonHeader)
	_ = json.NewEncoder(jsonPart).Encode(metadata)

	fileHeader := textPartHeader(metadata.Output.FileName, metadata.Output.MIMEType)
	filePart, _ := writer.CreatePart(fileHeader)
	_, _ = filePart.Write(fileContent)

	_ = writer.Close()
}

func textPartHeader(filename, contentType string) textproto.MIMEHeader {
	header := make(textproto.MIMEHeader)
	header.Set("Content-Type", contentType)
	header.Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	return header
}

func writeJSON(w http.ResponseWriter, statusCode int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeError(w http.ResponseWriter, statusCode int, apiErr *core.APIError) {
	writeJSON(w, statusCode, map[string]any{"error": apiErr})
}
