package export

import "errors"

var (
	ErrNotFound         = errors.New("export: not found")
	ErrWindowTooLarge   = errors.New("export: date range exceeds 365-day maximum")
	ErrInvalidDateRange = errors.New("export: date_to must be after date_from")
	ErrInvalidFormat    = errors.New("export: format must be 'csv' or 'json_lines'")
	ErrDownloadExpired  = errors.New("export: download link has expired")
	ErrDownloadNotReady = errors.New("export: export is not yet complete")
	ErrInvalidToken     = errors.New("export: download token is invalid or expired")
)
