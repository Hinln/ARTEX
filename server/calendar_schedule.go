package server

import (
	"fmt"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
)

type scheduleWindow struct {
	active    bool
	complete  bool
	key       string
	start     time.Time
	nextStart *time.Time
	nextEnd   *time.Time
}

func scheduleLocation(mode string) *time.Location {
	if mode == "system" {
		return time.Local
	}
	loc, _ := time.LoadLocation("Asia/Shanghai")
	return loc
}

func scheduleClock(day time.Time, clock string, loc *time.Location) time.Time {
	parts := strings.Split(clock, ":")
	h, _ := strconv.Atoi(parts[0])
	m, _ := strconv.Atoi(parts[1])
	return time.Date(day.Year(), day.Month(), day.Day(), h, m, 0, 0, loc)
}

// end before start means next-day end. Equal midnight means a full day;
// 24:00 is accepted for the end and normalised by PostgreSQL.
func windowBounds(day time.Time, s *db.TaskSchedule, loc *time.Location) (time.Time, *time.Time) {
	start := scheduleClock(day, s.StartTime, loc)
	if s.EndTime == "" {
		return start, nil
	}
	end := scheduleClock(day, s.EndTime, loc)
	if !end.After(start) {
		end = scheduleClock(day.AddDate(0, 0, 1), s.EndTime, loc)
	}
	return start, &end
}

func evaluateScheduleWindow(s *db.TaskSchedule, now time.Time) scheduleWindow {
	loc := scheduleLocation(s.TimezoneMode)
	now = now.In(loc)
	if s.ScheduleType == "once" {
		day, err := time.ParseInLocation("2006-01-02", s.RunDate, loc)
		if err != nil {
			return scheduleWindow{}
		}
		var start time.Time
		var end *time.Time
		if s.EndDate != "" {
			endDay, parseErr := time.ParseInLocation("2006-01-02", s.EndDate, loc)
			if parseErr != nil {
				return scheduleWindow{}
			}
			start = scheduleClock(day, s.StartTime, loc)
			endClock := s.EndTime
			if endClock == "" {
				endClock = "24:00"
			}
			endValue := scheduleClock(endDay, endClock, loc)
			end = &endValue
		} else {
			start, end = windowBounds(day, s, loc)
		}
		active := !now.Before(start) && (end == nil || now.Before(*end))
		complete := end != nil && !now.Before(*end)
		// An open-ended one-shot remains active after its start until manually
		// paused; there is no end boundary at which it could be considered missed.
		key := "once:" + s.RunDate
		if s.EndDate != "" {
			key += ":" + s.EndDate
		}
		w := scheduleWindow{active: active, complete: complete, key: key, start: start, nextEnd: end}
		if now.Before(start) {
			w.key = ""
			w.nextStart = &start
		}
		return w
	}
	// Once an unbounded weekly plan has started, it remains active.
	if s.EndTime == "" && strings.HasPrefix(s.LastWindowKey, "active:") {
		return scheduleWindow{active: true, key: s.LastWindowKey}
	}
	days := map[int]bool{}
	for _, d := range s.Weekdays {
		days[d] = true
	}
	w := scheduleWindow{}
	for offset := -1; offset <= 7; offset++ {
		day := now.AddDate(0, 0, offset)
		weekday := int(day.Weekday())
		if weekday == 0 {
			weekday = 7
		}
		if !days[weekday] {
			continue
		}
		start, end := windowBounds(day, s, loc)
		if !now.Before(start) && (end == nil || now.Before(*end)) {
			w.active = true
			w.start = start
			w.key = "active:" + start.Format("2006-01-02T15:04")
			w.nextEnd = end
			// Do not pause between adjacent windows (e.g. Saturday + Sunday full days).
			continue
		}
		if start.After(now) && (w.nextStart == nil || start.Before(*w.nextStart)) {
			w.nextStart = &start
			if !w.active {
				w.nextEnd = end
			}
		}
	}
	return w
}

// evaluateRuntimeScheduleWindow applies startup/manual state to a calendar
// window. A plan's creation date says nothing about when its next window opens.
func evaluateRuntimeScheduleWindow(item *db.TaskSchedule, now, startedAt time.Time) scheduleWindow {
	w := evaluateScheduleWindow(item, now)
	if !item.Enabled || item.Status == "completed" {
		w.active = false
		return w
	}
	// Only suppress an unrecorded window that was already open at startup.
	// An explicit configuration edit clears LastWindowKey and advances UpdatedAt;
	// allow such edits (as well as newly-created plans) to enter the current window.
	if w.active && item.LastWindowKey == "" && item.CreatedAt.Before(startedAt) &&
		!item.UpdatedAt.After(startedAt) && !w.start.IsZero() && w.start.Before(startedAt) {
		w.active = false
		w.key = "missed:" + w.key
	}
	if strings.HasPrefix(item.LastWindowKey, "missed:") && item.LastWindowKey == "missed:"+w.key {
		w.active = false
		w.key = item.LastWindowKey
	}
	if item.LastWindowKey == "manual" && !w.active {
		w.active = true
		w.key = "manual"
		w.complete = false
	}
	return w
}

// taskManualScheduleEnd allows only the selected task to run until the next
// enabled calendar's end. The normal union of windows still governs overlapping
// plans after that boundary. Open-ended or disabled plans impose no deadline.
func (s *Server) taskManualScheduleEnd(taskID string, now time.Time) (*time.Time, error) {
	id, err := strconv.ParseInt(taskID, 10, 64)
	if err != nil {
		return nil, err
	}
	items, err := s.m.pg.ListTaskSchedules()
	if err != nil {
		return nil, err
	}
	var until *time.Time
	for _, item := range items {
		for _, boundID := range item.TaskIDs {
			if boundID != id {
				continue
			}
			if until == nil {
				unbounded := time.Date(9999, 12, 31, 0, 0, 0, 0, time.UTC)
				until = &unbounded
			}
			if item.Enabled && item.Status != "completed" {
				w := evaluateScheduleWindow(item, now)
				if w.nextEnd != nil && w.nextEnd.After(now) && w.nextEnd.Before(*until) {
					until = w.nextEnd
				}
			}
			break
		}
	}
	return until, nil
}

// proxyResumeInsideSchedule reports whether a task attached to any enabled
// calendar is currently allowed to run. Tasks without a calendar remain
// eligible. Manual schedule releases are honored through schedule_manual_until.
func (s *Server) proxyResumeInsideSchedule(taskID string, now time.Time) (bool, error) {
	id, err := strconv.ParseInt(taskID, 10, 64)
	if err != nil {
		return false, err
	}
	items, err := s.m.pg.ListTaskSchedules()
	if err != nil {
		return false, err
	}
	attached := false
	open := false
	for _, item := range items {
		for _, boundID := range item.TaskIDs {
			if boundID != id {
				continue
			}
			attached = true
			if item.Enabled && item.Status != "completed" {
				w := evaluateScheduleWindow(item, now)
				open = open || w.active || item.LastWindowKey == "manual"
			}
			break
		}
	}
	if !attached {
		return true, nil
	}
	var manualUntil time.Time
	if err := s.m.pg.QueryRow(`SELECT COALESCE(schedule_manual_until, 'epoch'::timestamptz) FROM tasks WHERE id=$1`, id).Scan(&manualUntil); err == nil && manualUntil.After(now) {
		open = true
	}
	return open, nil
}

func (sc *Scheduler) fireTaskSchedules() {
	sc.fireTaskSchedulesAt(time.Now())
}

func (sc *Scheduler) fireTaskSchedulesAt(now time.Time) {
	sc.s.scheduleMu.Lock()
	defer sc.s.scheduleMu.Unlock()
	items, err := sc.pg.ListTaskSchedules()
	if err != nil {
		return
	}
	open := map[int64]bool{}
	windows := map[int64]scheduleWindow{}
	for _, item := range items {
		w := evaluateRuntimeScheduleWindow(item, now, sc.startedAt)
		windows[item.ID] = w
		if w.active {
			for _, id := range item.TaskIDs {
				open[id] = true
			}
		}
	}
	// Persisted task-level manual starts survive ticks, FIFO promotion and
	// restarts. Their deadline is the next calendar end, even if no scheduler
	// tick ran inside that window.
	rows, err := sc.pg.Query(`SELECT id FROM tasks WHERE schedule_manual_until > $1 AND deleted_at IS NULL`, now)
	if err != nil {
		return
	}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return
		}
		open[id] = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return
	}
	// Evaluate the union first, then control each task at most once per tick.
	seen := map[int64]bool{}
	for _, item := range items {
		w := windows[item.ID]
		errors := []string{}
		for _, id := range item.TaskIDs {
			if seen[id] {
				continue
			}
			seen[id] = true
			t, ok := sc.s.m.Task(strconv.FormatInt(id, 10))
			if !ok {
				continue
			}
			state := t.lifecycleSnapshot()
			if db.IsTerminal(state.Status) {
				continue
			}
			// A proxy-paused task can remain paused when a calendar window ends
			// during reconnect. Convert it to a schedule pause so a later probe
			// cannot resume work outside the configured window.
			if !open[id] && state.Paused && state.PauseReason == "paused_by_proxy" {
				if err := sc.s.m.ConvertProxyPauseToSchedule(strconv.FormatInt(id, 10)); err != nil {
					errors = append(errors, fmt.Sprintf("任务 #%d: %s", id, err))
				}
				continue
			}
			action := ""
			if open[id] && state.Paused {
				action = "resume"
			}
			if !open[id] && !state.Paused {
				action = "pause"
			}
			if action == "" {
				continue
			}
			// Explicit user pauses are never automatically lifted.
			if action == "resume" {
				var managed bool
				if err := sc.pg.QueryRow(`SELECT schedule_paused FROM tasks WHERE id=$1`, id).Scan(&managed); err != nil || !managed {
					continue
				}
			}
			_, err := sc.s.applyTaskControlWithCause(t, action, agent.AbortPausedBySchedule)
			message := "计划窗口"
			if err != nil {
				message = err.Error()
				errors = append(errors, fmt.Sprintf("任务 #%d: %s", id, message))
			}
			_ = sc.pg.RecordScheduleHistory(item.ID, id, action, err == nil, message)
		}
		status := "outside"
		if !item.Enabled {
			status = "paused"
		} else if w.active {
			status = "running"
		} else if w.complete {
			status = "completed"
		} else if item.LastWindowKey == "" {
			status = "waiting"
		}
		if len(errors) > 0 {
			status = "error"
		}
		key := item.LastWindowKey
		if w.active {
			key = w.key
		} else if strings.HasPrefix(w.key, "missed:") {
			key = w.key
		}
		if item.Status != status || key != item.LastWindowKey || len(errors) > 0 {
			if err := sc.pg.TouchTaskSchedule(item.ID, status, key, strings.Join(errors, "; ")); err != nil {
				continue
			}
			_ = sc.pg.RecordScheduleHistory(item.ID, 0, status, len(errors) == 0, strings.Join(errors, "; "))
		}
	}
}
