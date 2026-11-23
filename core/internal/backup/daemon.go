package backup

import (
	"context"
	"fmt"
	"time"
)

// ParseAt reads a time of day, "HH:MM" (CLOUDOS_BACKUP_AT).
func ParseAt(s string) (hour, minute int, err error) {
	t, err := time.Parse("15:04", s)
	if err != nil {
		return 0, 0, fmt.Errorf("CLOUDOS_BACKUP_AT %q: want HH:MM, such as 03:00", s)
	}
	return t.Hour(), t.Minute(), nil
}

// next is the first hour:minute strictly after now, in now's time zone.
func next(now time.Time, hour, minute int) time.Time {
	t := time.Date(now.Year(), now.Month(), now.Day(), hour, minute, 0, 0, now.Location())
	if !t.After(now) {
		t = time.Date(now.Year(), now.Month(), now.Day()+1, hour, minute, 0, 0, now.Location())
	}
	return t
}

// Daemon backs the box up every day at hour:minute, until ctx ends, and on
// Sundays reads back part of the repository (Check) and tests a restore
// (RestoreTest). A run that fails is in backup_runs; the next one comes the
// next night all the same.
func (s *Service) Daemon(ctx context.Context, hour, minute int) error {
	for {
		at := next(time.Now(), hour, minute)
		s.log().Info("backup: next run", "at", at.Format(time.RFC3339))
		timer := time.NewTimer(time.Until(at))
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
		if _, err := s.Backup(ctx); err != nil {
			s.log().Error("backup failed", "err", err)
		}
		if at.Weekday() != time.Sunday || ctx.Err() != nil {
			continue
		}
		if err := s.Check(ctx, at); err != nil {
			s.log().Error("backup check failed", "err", err)
		}
		if err := s.RestoreTest(ctx); err != nil {
			s.log().Error("backup restore test failed", "err", err)
		}
	}
}
