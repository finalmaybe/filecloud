package serverapp

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/mingming-cn/filecloud/internal/opslog"
	"github.com/mingming-cn/filecloud/internal/storage"
)

const gcRetryDelay = 5 * time.Minute

type gcSchedule struct {
	hour, minute int
	disabled     bool
}

func parseGCSchedule(value string) (gcSchedule, error) {
	if value == "off" {
		return gcSchedule{disabled: true}, nil
	}
	at, err := time.Parse("15:04", value)
	if err != nil || at.Format("15:04") != value {
		return gcSchedule{}, fmt.Errorf("gc-at must be HH:MM (server local time) or off")
	}
	return gcSchedule{hour: at.Hour(), minute: at.Minute()}, nil
}
func (s gcSchedule) next(now time.Time) time.Time {
	for day := now; ; day = day.AddDate(0, 0, 1) {
		at := time.Date(day.Year(), day.Month(), day.Day(), s.hour, s.minute, 0, 0, now.Location())
		if at.After(now) && at.Hour() == s.hour && at.Minute() == s.minute {
			return at
		}
	}
}

// A daily tick starts a fresh inventory; in between, revisit only deferred libraries.
func (s gcSchedule) run(ctx context.Context, collect func(retry bool) bool) {
	if s.disabled {
		return
	}
	daily := s.next(time.Now())
	due := daily
	for {
		timer := time.NewTimer(time.Until(due))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		if ctx.Err() != nil {
			return
		}
		now := time.Now()
		retry := due.Before(daily) && now.Before(daily)
		if !retry {
			daily = s.next(now)
		}
		pending := collect(retry)
		due = daily
		if pending {
			if next := time.Now().Add(gcRetryDelay); next.Before(daily) {
				due = next
			}
		}
	}
}

func startOnlineGC(ctx context.Context, s gcSchedule, store *storage.Store, logger *log.Logger) func() {
	if s.disabled {
		return func() {}
	}
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		var round *storage.OnlineGCRound
		s.run(ctx, func(retry bool) bool {
			if !retry {
				var err error
				round, err = store.NewOnlineGCRound(ctx)
				if err != nil {
					if ctx.Err() == nil {
						opslog.Error(logger, "online_gc", "", "list_libraries", err)
					}
					return false
				}
			}
			report, err := round.Run(ctx)
			if ctx.Err() != nil {
				return false
			}
			opslog.GCSummary(logger, report.Completed, report.Deferred, report.Failed, report.Objects, report.Bytes)
			if err != nil {
				opslog.Error(logger, "online_gc", "", "collect", err)
			}
			return report.Deferred != 0
		})
	}()
	return func() { cancel(); <-done }
}
