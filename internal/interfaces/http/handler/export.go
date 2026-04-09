package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"

	"github.com/urlshortener/platform/internal/application/apperrors"
	appexport "github.com/urlshortener/platform/internal/application/export"
	domainauth "github.com/urlshortener/platform/internal/domain/auth"
	domainexport "github.com/urlshortener/platform/internal/domain/export"
	"github.com/urlshortener/platform/internal/infrastructure/storage"
	"github.com/urlshortener/platform/internal/interfaces/http/response"
	"github.com/urlshortener/platform/pkg/logger"
	"github.com/urlshortener/platform/pkg/signedurl"
)

type ExportCreator interface {
	Handle(ctx context.Context, cmd appexport.CreateCommand) (*appexport.CreateResult, error)
}

type ExportQuerier interface {
	GetByID(ctx context.Context, id, workspaceID string) (*domainexport.Export, error)
	ListByWorkspace(ctx context.Context, workspaceID string, limit int) ([]*domainexport.Export, error)
}

type ExportDownloadLookup interface {
	GetAnyByID(ctx context.Context, id string) (*domainexport.Export, error)
}

type ExportStorage interface {
	Reader(exportID, extension string) (io.ReadCloser, error)
}

type CreateExportRequest struct {
	Format      string    `json:"format"`
	DateFrom    time.Time `json:"date_from"`
	DateTo      time.Time `json:"date_to"`
	IncludeBots bool      `json:"include_bots"`
}

type ExportResponse struct {
	ID                string     `json:"id"`
	WorkspaceID       string     `json:"workspace_id"`
	Format            string     `json:"format"`
	Status            string     `json:"status"`
	DateFrom          time.Time  `json:"date_from"`
	DateTo            time.Time  `json:"date_to"`
	IncludeBots       bool       `json:"include_bots"`
	RowCount          *int64     `json:"row_count,omitempty"`
	FileSizeBytes     *int64     `json:"file_size_bytes,omitempty"`
	ErrorMessage      string     `json:"error_message,omitempty"`
	DownloadURL       *string    `json:"download_url,omitempty"`
	DownloadExpiresAt *time.Time `json:"download_expires_at,omitempty"`
	CreatedAt         time.Time  `json:"created_at"`
	CompletedAt       *time.Time `json:"completed_at,omitempty"`
}

type ExportHandler struct {
	creator ExportCreator
	querier ExportQuerier
	lookup  ExportDownloadLookup
	storage ExportStorage
	signer  *signedurl.Signer
	baseURL string
	log     *slog.Logger
}

func NewExportHandler(
	creator ExportCreator,
	querier ExportQuerier,
	lookup ExportDownloadLookup,
	store ExportStorage,
	signer *signedurl.Signer,
	baseURL string,
	log *slog.Logger,
) *ExportHandler {
	return &ExportHandler{
		creator: creator,
		querier: querier,
		lookup:  lookup,
		storage: store,
		signer:  signer,
		baseURL: baseURL,
		log:     log,
	}
}

func (h *ExportHandler) Create(w http.ResponseWriter, r *http.Request) {
	log := logger.FromContext(r.Context()).With(
		slog.String("handler", "ExportHandler.Create"),
		slog.String("request_id", chimiddleware.GetReqID(r.Context())),
	)

	claims, ok := domainauth.FromContext(r.Context())
	if !ok {
		response.WriteProblem(w, response.Problem{
			Type: response.ProblemTypeUnauthenticated, Title: "Unauthorized", Status: http.StatusUnauthorized,
		})
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var req CreateExportRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.BadRequest(w, "request body could not be decoded", r.URL.Path)
		return
	}

	result, err := h.creator.Handle(r.Context(), appexport.CreateCommand{
		WorkspaceID: claims.WorkspaceID,
		RequestedBy: claims.UserID,
		Format:      req.Format,
		DateFrom:    req.DateFrom,
		DateTo:      req.DateTo,
		IncludeBots: req.IncludeBots,
	})
	if err != nil {
		h.writeError(w, r, err, log)
		return
	}

	response.JSON(w, http.StatusAccepted, response.Envelope{
		Data: ExportResponse{
			ID:          result.ID,
			WorkspaceID: result.WorkspaceID,
			Format:      result.Format,
			Status:      result.Status,
			DateFrom:    result.DateFrom,
			DateTo:      result.DateTo,
			IncludeBots: result.IncludeBots,
			CreatedAt:   result.CreatedAt,
		},
	})
}

func (h *ExportHandler) Get(w http.ResponseWriter, r *http.Request) {
	claims, ok := domainauth.FromContext(r.Context())
	if !ok {
		response.WriteProblem(w, response.Problem{
			Type: response.ProblemTypeUnauthenticated, Title: "Unauthorized", Status: http.StatusUnauthorized,
		})
		return
	}
	e, err := h.querier.GetByID(r.Context(), chi.URLParam(r, "exportID"), claims.WorkspaceID)
	if err != nil {
		h.writeError(w, r, err, logger.FromContext(r.Context()))
		return
	}
	response.JSON(w, http.StatusOK, response.Envelope{Data: toExportResponse(e, h.baseURL)})
}

func (h *ExportHandler) List(w http.ResponseWriter, r *http.Request) {
	claims, ok := domainauth.FromContext(r.Context())
	if !ok {
		response.WriteProblem(w, response.Problem{
			Type: response.ProblemTypeUnauthenticated, Title: "Unauthorized", Status: http.StatusUnauthorized,
		})
		return
	}
	limit := 20
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 && n <= 100 {
			limit = n
		}
	}
	exports, err := h.querier.ListByWorkspace(r.Context(), claims.WorkspaceID, limit)
	if err != nil {
		h.writeError(w, r, err, logger.FromContext(r.Context()))
		return
	}
	results := make([]ExportResponse, 0, len(exports))
	for _, e := range exports {
		results = append(results, toExportResponse(e, h.baseURL))
	}
	response.JSON(w, http.StatusOK, response.Envelope{Data: results})
}

func (h *ExportHandler) Download(w http.ResponseWriter, r *http.Request) {
	exportID := chi.URLParam(r, "exportID")
	token := r.URL.Query().Get("token")
	if token == "" {
		response.WriteProblem(w, response.Problem{
			Type: response.ProblemTypeUnauthenticated, Title: "Unauthorized", Status: http.StatusUnauthorized, Detail: "A valid download token is required.",
		})
		return
	}

	e, err := h.lookup.GetAnyByID(r.Context(), exportID)
	if err != nil {
		if errors.Is(err, domainexport.ErrNotFound) {
			response.NotFound(w, r.URL.Path)
			return
		}
		response.InternalError(w, r.URL.Path)
		return
	}
	if e.Status != domainexport.StatusCompleted || e.DownloadExpiresAt == nil {
		response.Conflict(w, domainexport.ErrDownloadNotReady.Error(), r.URL.Path)
		return
	}
	if err := h.signer.Verify(exportID, token, *e.DownloadExpiresAt); err != nil {
		response.WriteProblem(w, response.Problem{
			Type: response.ProblemTypeUnauthenticated, Title: "Unauthorized", Status: http.StatusUnauthorized, Detail: domainexport.ErrInvalidToken.Error(),
		})
		return
	}

	reader, err := h.storage.Reader(exportID, e.FileExtension())
	if err != nil {
		if errors.Is(err, storage.ErrFileNotFound) {
			response.WriteProblem(w, response.Problem{
				Type:     response.ProblemTypeGone,
				Title:    "Gone",
				Status:   http.StatusGone,
				Detail:   "The export file is no longer available.",
				Instance: r.URL.Path,
			})
			return
		}
		response.InternalError(w, r.URL.Path)
		return
	}
	defer reader.Close()

	w.Header().Set("Content-Type", e.ContentType())
	w.Header().Set("Content-Disposition", `attachment; filename="`+e.FileName()+`"`)
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, reader)
}

func toExportResponse(e *domainexport.Export, baseURL string) ExportResponse {
	resp := ExportResponse{
		ID:                e.ID,
		WorkspaceID:       e.WorkspaceID,
		Format:            string(e.Format),
		Status:            string(e.Status),
		DateFrom:          e.DateFrom,
		DateTo:            e.DateTo,
		IncludeBots:       e.IncludeBots,
		ErrorMessage:      e.ErrorMessage,
		DownloadExpiresAt: e.DownloadExpiresAt,
		CreatedAt:         e.CreatedAt,
		CompletedAt:       e.CompletedAt,
	}
	if e.Status == domainexport.StatusCompleted {
		rowCount := e.RowCount
		size := e.FileSizeBytes
		resp.RowCount = &rowCount
		resp.FileSizeBytes = &size
		if e.IsDownloadable() && e.DownloadToken != "" {
			url := signedurl.BuildDownloadURL(baseURL, e.ID, e.DownloadToken)
			resp.DownloadURL = &url
		}
	}
	return resp
}

func (h *ExportHandler) writeError(w http.ResponseWriter, r *http.Request, err error, log *slog.Logger) {
	var ve *apperrors.ValidationError
	if errors.As(err, &ve) {
		response.UnprocessableEntity(w, ve.Message, r.URL.Path)
		return
	}
	if errors.Is(err, domainexport.ErrNotFound) || errors.Is(err, apperrors.ErrNotFound) {
		response.NotFound(w, r.URL.Path)
		return
	}
	log.Error("unexpected error in export handler", slog.String("error", err.Error()), slog.String("path", r.URL.Path))
	response.InternalError(w, r.URL.Path)
}
