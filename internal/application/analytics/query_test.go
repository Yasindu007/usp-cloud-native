package analytics_test

import (
	"testing"
	"time"

	"github.com/urlshortener/platform/internal/domain/analytics"
)

func TestWindow_IsValid(t *testing.T) {
	valid := []analytics.Window{
		analytics.Window1Hour, analytics.Window24Hour,
		analytics.Window7Day, analytics.Window30Day, analytics.WindowAllTime,
	}
	for _, w := range valid {
		if !w.IsValid() {
			t.Errorf("expected IsValid=true for %q", w)
		}
	}
	if analytics.Window("2w").IsValid() {
		t.Error("expected IsValid=false for unknown window")
	}
}

func TestWindow_Duration(t *testing.T) {
	cases := map[analytics.Window]time.Duration{
		analytics.Window1Hour:  time.Hour,
		analytics.Window24Hour: 24 * time.Hour,
		analytics.Window7Day:   7 * 24 * time.Hour,
		analytics.Window30Day:  30 * 24 * time.Hour,
	}
	for w, want := range cases {
		got := w.Duration()
		if got != want {
			t.Errorf("Window(%q).Duration(): want %v, got %v", w, want, got)
		}
	}
}

func TestParseWindow_ValidValues(t *testing.T) {
	for _, s := range []string{"1h", "24h", "7d", "30d", "all"} {
		w, err := analytics.ParseWindow(s)
		if err != nil {
			t.Errorf("ParseWindow(%q): unexpected error: %v", s, err)
		}
		if string(w) != s {
			t.Errorf("ParseWindow(%q): got %q", s, w)
		}
	}
}

func TestParseWindow_InvalidValue(t *testing.T) {
	_, err := analytics.ParseWindow("2weeks")
	if err == nil {
		t.Error("expected error for invalid window")
	}
}

func TestGranularity_IsValid(t *testing.T) {
	for _, g := range []analytics.Granularity{
		analytics.Granularity1Minute, analytics.Granularity1Hour, analytics.Granularity1Day,
	} {
		if !g.IsValid() {
			t.Errorf("expected IsValid=true for %q", g)
		}
	}
}

func TestValidateWindowGranularity(t *testing.T) {
	now := time.Now()

	// 1m granularity — window must be <= 24h
	err := analytics.ValidateWindowGranularity(now.Add(-23*time.Hour), now, analytics.Granularity1Minute)
	if err != nil {
		t.Errorf("23h window with 1m granularity: unexpected error: %v", err)
	}

	err = analytics.ValidateWindowGranularity(now.Add(-25*time.Hour), now, analytics.Granularity1Minute)
	if err == nil {
		t.Error("25h window with 1m granularity: expected error")
	}

	// 1h granularity — window must be <= 30d
	err = analytics.ValidateWindowGranularity(now.Add(-29*24*time.Hour), now, analytics.Granularity1Hour)
	if err != nil {
		t.Errorf("29d window with 1h granularity: unexpected error: %v", err)
	}

	err = analytics.ValidateWindowGranularity(now.Add(-31*24*time.Hour), now, analytics.Granularity1Hour)
	if err == nil {
		t.Error("31d window with 1h granularity: expected error")
	}
}

func TestDimension_ColumnName(t *testing.T) {
	cases := map[analytics.Dimension]string{
		analytics.DimensionCountry:    "country_code",
		analytics.DimensionDeviceType: "device_type",
		analytics.DimensionBrowser:    "browser_family",
		analytics.DimensionOS:         "os_family",
		analytics.DimensionReferrer:   "referrer_domain",
	}
	for d, want := range cases {
		got := d.ColumnName()
		if got != want {
			t.Errorf("Dimension(%q).ColumnName(): want %q, got %q", d, want, got)
		}
	}
}

func TestDimension_InvalidColumnName_Empty(t *testing.T) {
	d := analytics.Dimension("unknown_dim")
	if d.ColumnName() != "" {
		t.Error("unknown dimension must return empty column name")
	}
}
