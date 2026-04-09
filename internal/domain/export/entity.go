package export

import "time"

type Format string

const (
	FormatCSV       Format = "csv"
	FormatJSONLines Format = "json_lines"
)

type Status string

const (
	StatusPending    Status = "pending"
	StatusProcessing Status = "processing"
	StatusCompleted  Status = "completed"
	StatusFailed     Status = "failed"
)

const MaxWindowDays = 365

type Export struct {
	ID          string
	WorkspaceID string
	RequestedBy string
	Format      Format
	Status      Status
	DateFrom    time.Time
	DateTo      time.Time
	IncludeBots bool

	FilePath          string
	RowCount          int64
	FileSizeBytes     int64
	DownloadToken     string
	DownloadExpiresAt *time.Time

	ErrorMessage string

	CreatedAt       time.Time
	WorkerStartedAt *time.Time
	CompletedAt     *time.Time
}

func (e *Export) IsDownloadable() bool {
	return e.Status == StatusCompleted && e.DownloadExpiresAt != nil && time.Now().UTC().Before(*e.DownloadExpiresAt)
}

func (e *Export) FileExtension() string {
	switch e.Format {
	case FormatCSV:
		return ".csv"
	case FormatJSONLines:
		return ".jsonl"
	default:
		return ".dat"
	}
}

func (e *Export) ContentType() string {
	switch e.Format {
	case FormatCSV:
		return "text/csv; charset=utf-8"
	case FormatJSONLines:
		return "application/x-ndjson"
	default:
		return "application/octet-stream"
	}
}

func (e *Export) FileName() string {
	return "export_" + e.WorkspaceID[:min(8, len(e.WorkspaceID))] +
		"_" + e.DateFrom.Format("2006-01-02") +
		"_to_" + e.DateTo.Format("2006-01-02") +
		e.FileExtension()
}

type RedirectEventRow struct {
	ShortCode      string    `json:"short_code"`
	OccurredAt     time.Time `json:"occurred_at"`
	CountryCode    string    `json:"country_code"`
	DeviceType     string    `json:"device_type"`
	BrowserFamily  string    `json:"browser_family"`
	OSFamily       string    `json:"os_family"`
	ReferrerDomain string    `json:"referrer_domain"`
	IsBot          bool      `json:"is_bot"`
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
