package serverapp

import (
	"context"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/mingming-cn/filecloud/internal/object"
	"github.com/mingming-cn/filecloud/internal/storage"
)

func TestGCScheduleDailyAndDeferredRetry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		s := gcSchedule{hour: 3}
		next := s.next(time.Now())
		type tick struct {
			at    time.Time
			retry bool
		}
		calls := make(chan tick)
		stopped := make(chan struct{})
		go func() {
			defer close(stopped)
			first := true
			s.run(ctx, func(retry bool) bool {
				select {
				case calls <- tick{time.Now(), retry}:
				case <-ctx.Done():
					return false
				}
				pending := first
				first = false
				return pending
			})
		}()
		for _, want := range []tick{{next, false}, {next.Add(gcRetryDelay), true}, {s.next(next), false}} {
			got := <-calls
			if !got.at.Equal(want.at) || got.retry != want.retry {
				t.Fatalf("got %+v, want %+v", got, want)
			}
		}
		cancel()
		<-stopped
	})
}

func TestScheduledGCUsesRunningStore(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx := t.Context()
		dir := t.TempDir()
		if err := storage.Init(ctx, dir); err != nil {
			t.Fatal(err)
		}
		s, err := storage.OpenForServe(ctx, dir)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		now := time.Now()
		const owner = "test-owner"
		const lib = "test-library"
		if err := s.CreateUser(ctx, storage.User{ID: owner, Username: "scheduled", PasswordHash: "unused"}, now); err != nil {
			t.Fatal(err)
		}
		if _, _, err := s.CreateLibrary(ctx, storage.Library{ID: lib, OwnerUserID: owner, Name: "scheduled"}, now); err != nil {
			t.Fatal(err)
		}
		id := object.ID([]byte("orphan"))
		if _, err := s.PutObject(ctx, owner, lib, "blocks", id, strings.NewReader("orphan")); err != nil {
			t.Fatal(err)
		}
		aged := now.Add(-48 * time.Hour)
		if err := os.Chtimes(filepath.Join(s.ObjectsDir(), owner, lib, "blocks", id[:2], id[2:]), aged, aged); err != nil {
			t.Fatal(err)
		}
		at := now.Add(10 * time.Minute)
		schedule := gcSchedule{hour: at.Hour(), minute: at.Minute()}
		stop := startOnlineGC(ctx, schedule, s, log.New(io.Discard, "", 0))
		synctest.Wait()
		time.Sleep(time.Until(schedule.next(now)))
		synctest.Wait()
		stop()
		if exists, err := s.HasObject(ctx, owner, lib, "blocks", id); err != nil || exists {
			t.Fatal("scheduled orphan collection", exists, err)
		}
		if err := s.CheckReady(ctx); err != nil {
			t.Fatal(err)
		}
	})
}

func TestGCScheduleConfiguration(t *testing.T) {
	for _, bad := range []string{"", "3:00", "24:00", "03:60", "@daily"} {
		if _, err := parseGCSchedule(bad); err == nil {
			t.Fatal("accepted", bad)
		}
	}
	s, err := parseGCSchedule("off")
	if err != nil {
		t.Fatal(err)
	}
	s.run(t.Context(), func(bool) bool { t.Fatal("disabled task fired"); return false })
	s, err = parseGCSchedule("23:05")
	if err != nil {
		t.Fatal(err)
	}
	loc := time.FixedZone("local", 8*3600)
	today := time.Date(2026, 11, 21, 23, 5, 0, 0, loc)
	if !s.next(today.Add(-time.Second)).Equal(today) || !s.next(today).Equal(today.AddDate(0, 0, 1)) {
		t.Fatal("local calendar schedule")
	}
}
