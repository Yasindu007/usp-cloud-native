package export

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"time"

	domainexport "github.com/urlshortener/platform/internal/domain/export"
	"github.com/urlshortener/platform/internal/infrastructure/storage"
	"github.com/urlshortener/platform/pkg/signedurl"
)

type Worker struct {
	repo        domainexport.Repository
	reader      domainexport.EventReader
	storage     storage.Storage
	signer      *signedurl.Signer
	log         *slog.Logger
	downloadTTL time.Duration
	pollEvery   time.Duration
}

func NewWorker(
	repo domainexport.Repository,
	reader domainexport.EventReader,
	store storage.Storage,
	signer *signedurl.Signer,
	log *slog.Logger,
	downloadTTL time.Duration,
	pollEvery time.Duration,
) *Worker {
	return &Worker{
		repo:        repo,
		reader:      reader,
		storage:     store,
		signer:      signer,
		log:         log,
		downloadTTL: downloadTTL,
		pollEvery:   pollEvery,
	}
}

func (w *Worker) Run(ctx context.Context) {
	ticker := time.NewTicker(w.pollEvery)
	defer ticker.Stop()

	for {
		if err := w.processOne(ctx); err != nil && ctx.Err() == nil {
			w.log.Error("export worker iteration failed", slog.String("error", err.Error()))
		}

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (w *Worker) processOne(ctx context.Context) error {
	job, err := w.repo.ClaimPending(ctx)
	if err != nil || job == nil {
		return err
	}

	ext := job.FileExtension()
	writer, err := w.storage.Writer(job.ID, ext)
	if err != nil {
		_ = w.repo.MarkFailed(ctx, job.ID, err.Error())
		return err
	}

	var rowCount int64
	writeErr := w.writeRows(ctx, writer, job, &rowCount)
	closeErr := writer.Close()
	if writeErr == nil {
		writeErr = closeErr
	}
	if writeErr != nil {
		_ = w.storage.Delete(job.ID, ext)
		_ = w.repo.MarkFailed(ctx, job.ID, writeErr.Error())
		return writeErr
	}

	size, err := w.storage.Size(job.ID, ext)
	if err != nil {
		_ = w.repo.MarkFailed(ctx, job.ID, err.Error())
		return err
	}
	expiresAt := time.Now().UTC().Add(w.downloadTTL)
	token := w.signer.Sign(job.ID, expiresAt)

	if err := w.repo.MarkCompleted(ctx, job.ID, w.storage.FilePath(job.ID, ext), rowCount, size, token, expiresAt); err != nil {
		return err
	}
	w.log.Info("export completed", slog.String("id", job.ID), slog.Int64("rows", rowCount), slog.Int64("bytes", size))
	return nil
}

func (w *Worker) writeRows(ctx context.Context, out io.Writer, job *domainexport.Export, rowCount *int64) error {
	rowsCh, errCh := w.reader.ReadEvents(ctx, domainexport.EventQuery{
		WorkspaceID: job.WorkspaceID,
		DateFrom:    job.DateFrom,
		DateTo:      job.DateTo,
		IncludeBots: job.IncludeBots,
		BatchSize:   1000,
	})

	switch job.Format {
	case domainexport.FormatCSV:
		cw := csv.NewWriter(out)
		if err := cw.Write([]string{"short_code", "occurred_at", "country_code", "device_type", "browser_family", "os_family", "referrer_domain", "is_bot"}); err != nil {
			return err
		}
		for row := range rowsCh {
			if err := cw.Write([]string{
				row.ShortCode,
				row.OccurredAt.Format(time.RFC3339),
				row.CountryCode,
				row.DeviceType,
				row.BrowserFamily,
				row.OSFamily,
				row.ReferrerDomain,
				fmt.Sprintf("%t", row.IsBot),
			}); err != nil {
				return err
			}
			*rowCount++
		}
		cw.Flush()
		if err := cw.Error(); err != nil {
			return err
		}
	case domainexport.FormatJSONLines:
		enc := json.NewEncoder(out)
		for row := range rowsCh {
			if err := enc.Encode(row); err != nil {
				return err
			}
			*rowCount++
		}
	default:
		return domainexport.ErrInvalidFormat
	}

	if err := <-errCh; err != nil {
		if err == context.Canceled || err == context.DeadlineExceeded {
			return nil
		}
		return err
	}
	return nil
}
