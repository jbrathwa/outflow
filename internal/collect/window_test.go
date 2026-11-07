package collect

import (
	"testing"
	"time"
)

func TestParseSince(t *testing.T) {
	cases := []struct {
		in      string
		want    time.Duration
		wantErr bool
	}{
		{"", 24 * time.Hour, false},
		{"90m", 90 * time.Minute, false},
		{"7d", 168 * time.Hour, false},
		{"5m", 5 * time.Minute, false},
		{"30d", 720 * time.Hour, false},
		{"1m", 0, true},
		{"31d", 0, true},
		{"abc", 0, true},
		{"xd", 0, true},
	}
	for _, c := range cases {
		got, err := ParseSince(c.in)
		if (err != nil) != c.wantErr || (!c.wantErr && got != c.want) {
			t.Errorf("ParseSince(%q) = %v, %v", c.in, got, err)
		}
	}
}

func TestWindowTruncatesToMinute(t *testing.T) {
	now := time.Date(2026, 11, 7, 10, 30, 45, 123, time.UTC)
	w := Window(now, 2*time.Hour)
	if !w.End.Equal(time.Date(2026, 11, 7, 10, 30, 0, 0, time.UTC)) {
		t.Errorf("end %v", w.End)
	}
	if !w.Start.Equal(time.Date(2026, 11, 7, 8, 30, 0, 0, time.UTC)) {
		t.Errorf("start %v", w.Start)
	}
}
