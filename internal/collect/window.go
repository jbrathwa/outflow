package collect

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jbrathwa/outflow/internal/model"
)

const (
	minSince     = 5 * time.Minute
	maxSince     = 30 * 24 * time.Hour
	defaultSince = 24 * time.Hour
)

// ParseSince parses a --since value: "" is 24h; Go durations and "<n>d" are accepted; 5m <= d <= 30d.
func ParseSince(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return defaultSince, nil
	}
	var d time.Duration
	if strings.HasSuffix(s, "d") {
		n, err := strconv.Atoi(strings.TrimSuffix(s, "d"))
		if err != nil {
			return 0, fmt.Errorf("invalid --since %q: use a duration like 90m, 24h or 7d", s)
		}
		d = time.Duration(n) * 24 * time.Hour
	} else {
		var err error
		d, err = time.ParseDuration(s)
		if err != nil {
			return 0, fmt.Errorf("invalid --since %q: use a duration like 90m, 24h or 7d", s)
		}
	}
	if d < minSince || d > maxSince {
		return 0, fmt.Errorf("--since %q out of range: must be between 5m and 30d", s)
	}
	return d, nil
}

// Window returns the range ending at now (truncated to the minute) and starting since earlier.
func Window(now time.Time, since time.Duration) model.Window {
	end := now.Truncate(time.Minute)
	return model.Window{Start: end.Add(-since), End: end}
}
