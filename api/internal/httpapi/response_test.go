package httpapi

import (
	"testing"
	"time"
)

func TestRelativeTime(t *testing.T) {
	now := time.Date(2026, time.November, 6, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name  string
		value *time.Time
		want  string
	}{
		{name: "missing timestamp", want: "Never"},
		{name: "seconds", value: timePointer(now.Add(-10 * time.Second)), want: "just now"},
		{name: "minutes", value: timePointer(now.Add(-3 * time.Minute)), want: "3 min ago"},
		{name: "hours", value: timePointer(now.Add(-2 * time.Hour)), want: "2 hours ago"},
		{name: "days", value: timePointer(now.Add(-48 * time.Hour)), want: "2 days ago"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := relativeTime(now, test.value); got != test.want {
				t.Errorf("relativeTime() = %q, want %q", got, test.want)
			}
		})
	}
}

func timePointer(value time.Time) *time.Time {
	return &value
}
