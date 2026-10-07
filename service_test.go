package gomedicationcourse

import (
	"errors"
	"sync"
	"testing"
	"time"
)

type fakeClock struct{ t time.Time }

func newFixture(t *testing.T) (*Service, *fakeClock, time.Time) {
	t.Helper()
	start := time.Date(2026, 1, 1, 8, 0, 0, 0, time.UTC)
	clk := &fakeClock{t: start}
	svc := NewService(clk.now)
	_, err := svc.CreateCourse(CreateCourseCommand{
		ID:        "C1",
		Dosage:    Dosage{Amount: 10, Unit: "mg"},
		Frequency: Frequency{Interval: 6 * time.Hour},
		StartAt:   start,
		CreatedBy: "doctor-a",
		Reason:    "initial",
	})
	if err != nil {
		t.Fatalf("create course: %v", err)
	}
	return svc, clk, start
}

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

// 跨版本计划：调整后，生效时间之后的时间点使用新版本与新间隔；
// 已登记的服药事实保留原版本与原剂量。
func TestAdjustRewritesOnlyFuturePoints(t *testing.T) {
	svc, clk, start := newFixture(t)

	rec, err := svc.RegisterIntake(RegisterCommand{
		CourseID: "C1", PlannedAt: start, ExpectedVersion: 1, Registrar: "nurse-1",
	})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if rec.Version != 1 || rec.Dosage.Amount != 10 || rec.Backfilled {
		t.Fatalf("unexpected intake snapshot: %+v", rec)
	}

	clk.advance(time.Hour)
	effective := start.Add(4 * time.Hour)
	v2, err := svc.Adjust(AdjustCommand{
		CourseID:    "C1",
		Dosage:      Dosage{Amount: 20, Unit: "mg"},
		Frequency:   Frequency{Interval: 24 * time.Hour},
		EffectiveAt: effective,
		Adjuster:    "doctor-b",
		Reason:      "increase dose",
		RequestID:   "adj-1",
	})
	if err != nil {
		t.Fatalf("adjust: %v", err)
	}
	if v2.Version != 2 {
		t.Fatalf("expected version 2, got %d", v2.Version)
	}

	entries, err := svc.Schedule("C1", start, start.Add(48*time.Hour))
	if err != nil {
		t.Fatalf("schedule: %v", err)
	}
	byTime := map[time.Time]ScheduleEntry{}
	for _, e := range entries {
		byTime[e.ScheduledAt] = e
	}

	first := byTime[start]
	if first.Version != 1 || first.Dosage.Amount != 10 || first.Status != PointRegistered {
		t.Fatalf("historical point rewritten: %+v", first)
	}
	boundary := byTime[effective]
	if boundary.Version != 2 || boundary.Dosage.Amount != 20 || boundary.Status != PointPending {
		t.Fatalf("boundary point not on v2: %+v", boundary)
	}
	if _, ok := byTime[effective.Add(24*time.Hour)]; !ok {
		t.Fatalf("expected a point 24h after effective time")
	}
	for _, old := range []time.Time{start.Add(6 * time.Hour), start.Add(12 * time.Hour)} {
		if _, ok := byTime[old]; ok {
			t.Fatalf("stale v1 future point at %s should have been removed", old)
		}
	}

	intakes, _ := svc.Intakes("C1")
	if len(intakes) != 1 || intakes[0].Version != 1 || intakes[0].Dosage.Amount != 10 {
		t.Fatalf("intake facts were rewritten: %+v", intakes)
	}
}

// 历史记录：保存调整人、原因、提交时间和生效时间，版本号递增。
func TestAdjustHistory(t *testing.T) {
	svc, clk, start := newFixture(t)
	clk.advance(time.Hour)
	submitted := clk.now()
	v2, err := svc.Adjust(AdjustCommand{
		CourseID:    "C1",
		Dosage:      Dosage{Amount: 20, Unit: "mg"},
		Frequency:   Frequency{Interval: 12 * time.Hour},
		EffectiveAt: start.Add(2 * time.Hour),
		Adjuster:    "doctor-b",
		Reason:      "tolerance",
		RequestID:   "adj-h1",
	})
	if err != nil {
		t.Fatalf("adjust: %v", err)
	}
	if !v2.SubmittedAt.Equal(submitted) || v2.Adjuster != "doctor-b" || v2.Reason != "tolerance" {
		t.Fatalf("audit fields missing: %+v", v2)
	}
	hist, err := svc.History("C1")
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(hist) != 2 || hist[0].Version != 1 || hist[1].Version != 2 {
		t.Fatalf("history not strictly increasing: %+v", hist)
	}
}

// 生效时间不能早于已经确认的服药事实；也不能早于提交时刻。
func TestEffectiveTimeGuards(t *testing.T) {
	svc, clk, start := newFixture(t)

	// 在 08:00 提前登记 20:00 的未来服药事实（版本 1）。
	clk.advance(6 * time.Hour)
	if _, err := svc.RegisterIntake(RegisterCommand{
		CourseID: "C1", PlannedAt: start.Add(12 * time.Hour), ExpectedVersion: 1,
	}); err != nil {
		t.Fatalf("register: %v", err)
	}

	// 14:00 提交调整，16:00 生效：生效时刻在未来，但早于已确认的 20:00 服药。
	_, err := svc.Adjust(AdjustCommand{
		CourseID:    "C1",
		Dosage:      Dosage{Amount: 20, Unit: "mg"},
		Frequency:   Frequency{Interval: 24 * time.Hour},
		EffectiveAt: start.Add(8 * time.Hour),
		RequestID:   "adj-bad",
	})
	if !errors.Is(err, ErrEffectiveBeforeIntake) {
		t.Fatalf("want ErrEffectiveBeforeIntake, got %v", err)
	}

	_, err = svc.Adjust(AdjustCommand{
		CourseID:    "C1",
		Dosage:      Dosage{Amount: 20, Unit: "mg"},
		Frequency:   Frequency{Interval: 24 * time.Hour},
		EffectiveAt: clk.now().Add(-time.Minute),
		RequestID:   "adj-past",
	})
	if !errors.Is(err, ErrEffectiveInPast) {
		t.Fatalf("want ErrEffectiveInPast, got %v", err)
	}
}

// 携带旧版本登记调整后的未来点 -> 版本冲突；
// 补记历史点必须用历史版本，且记录为 Backfilled，不能伪装成新版本服药。
func TestVersionConflictAndBackfill(t *testing.T) {
	svc, clk, start := newFixture(t)
	clk.advance(time.Hour)
	effective := start.Add(2 * time.Hour)
	if _, err := svc.Adjust(AdjustCommand{
		CourseID:    "C1",
		Dosage:      Dosage{Amount: 30, Unit: "mg"},
		Frequency:   Frequency{Interval: 24 * time.Hour},
		EffectiveAt: effective,
		Adjuster:    "doctor-b",
		Reason:      "change",
		RequestID:   "adj-x",
	}); err != nil {
		t.Fatalf("adjust: %v", err)
	}

	// 边界点 10:00 已经是 v2；携带版本 1 登记必须报版本冲突。
	clk.advance(2 * time.Hour) // now = 11:00
	_, err := svc.RegisterIntake(RegisterCommand{
		CourseID: "C1", PlannedAt: effective, ExpectedVersion: 1,
	})
	if !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("want ErrVersionConflict on stale future register, got %v", err)
	}

	// 调整删除的旧节奏点（如 12:00 的 v1 点）不能再登记。
	_, err = svc.RegisterIntake(RegisterCommand{
		CourseID: "C1", PlannedAt: start.Add(4 * time.Hour), ExpectedVersion: 1,
	})
	if !errors.Is(err, ErrNoSuchPlan) {
		t.Fatalf("want ErrNoSuchPlan for removed point, got %v", err)
	}

	// 补记：调整前 08:00 的历史点必须携带版本 1；用版本 2 补记是冲突。
	_, err = svc.RegisterIntake(RegisterCommand{
		CourseID: "C1", PlannedAt: start, ExpectedVersion: 2,
	})
	if !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("backfill must not masquerade as new version, got %v", err)
	}
	back, err := svc.RegisterIntake(RegisterCommand{
		CourseID: "C1", PlannedAt: start, ExpectedVersion: 1,
	})
	if err != nil {
		t.Fatalf("backfill with historical version: %v", err)
	}
	if !back.Backfilled || back.Version != 1 || back.Dosage.Amount != 10 {
		t.Fatalf("backfill must keep historical snapshot: %+v", back)
	}
}

// 幂等：同号同内容返回原调整；剂量/频次/生效时间不同返回冲突。
// 调整失败不得产生任何部分改变。
func TestAdjustIdempotencyAndAtomicity(t *testing.T) {
	svc, clk, start := newFixture(t)
	clk.advance(30 * time.Minute)
	cmd := AdjustCommand{
		CourseID:    "C1",
		Dosage:      Dosage{Amount: 20, Unit: "mg"},
		Frequency:   Frequency{Interval: 12 * time.Hour},
		EffectiveAt: start.Add(2 * time.Hour),
		Adjuster:    "doctor-b",
		Reason:      "x",
		RequestID:   "idem-1",
	}
	first, err := svc.Adjust(cmd)
	if err != nil {
		t.Fatalf("first adjust: %v", err)
	}
	clk.advance(time.Minute)
	again, err := svc.Adjust(cmd)
	if err != nil {
		t.Fatalf("idempotent retry: %v", err)
	}
	if again != first {
		t.Fatalf("retry must return the original adjustment: %+v vs %+v", first, again)
	}
	if hist, _ := svc.History("C1"); len(hist) != 2 {
		t.Fatalf("retry created another version: %+v", hist)
	}

	for _, mutate := range []func(*AdjustCommand){
		func(c *AdjustCommand) { c.Dosage.Amount = 99 },
		func(c *AdjustCommand) { c.Frequency.Interval = time.Hour },
		func(c *AdjustCommand) { c.EffectiveAt = c.EffectiveAt.Add(time.Hour) },
	} {
		diff := cmd
		mutate(&diff)
		if _, err := svc.Adjust(diff); !errors.Is(err, ErrRequestConflict) {
			t.Fatalf("want ErrRequestConflict, got %v", err)
		}
	}

	before, _ := svc.History("C1")
	if err := svc.Complete("C1", "doctor-b", clk.now()); err != nil {
		t.Fatalf("complete: %v", err)
	}
	_, err = svc.Adjust(AdjustCommand{
		CourseID:    "C1",
		Dosage:      Dosage{Amount: 40, Unit: "mg"},
		Frequency:   Frequency{Interval: 24 * time.Hour},
		EffectiveAt: clk.now().Add(time.Hour),
		RequestID:   "idem-after-complete",
	})
	if !errors.Is(err, ErrCourseClosed) {
		t.Fatalf("completed course must reject adjustment, got %v", err)
	}
	after, _ := svc.History("C1")
	if len(after) != len(before) {
		t.Fatalf("failed adjustment partially mutated versions")
	}
}

// 取消的疗程不能调整、不能登记。
func TestCancelledCourseIsClosed(t *testing.T) {
	svc, _, start := newFixture(t)
	if err := svc.Cancel("C1", "doctor", start.Add(time.Minute)); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	_, err := svc.Adjust(AdjustCommand{
		CourseID:    "C1",
		Dosage:      Dosage{Amount: 1, Unit: "mg"},
		Frequency:   Frequency{Interval: time.Hour},
		EffectiveAt: start.Add(2 * time.Hour),
		RequestID:   "r",
	})
	if !errors.Is(err, ErrCourseClosed) {
		t.Fatalf("cancel course adjust: %v", err)
	}
	_, err = svc.RegisterIntake(RegisterCommand{
		CourseID: "C1", PlannedAt: start, ExpectedVersion: 1,
	})
	if !errors.Is(err, ErrCourseClosed) {
		t.Fatalf("cancel course register: %v", err)
	}
}

// 并发：多个登记同时裁决同一计划点，只有一个成功，其余得到已登记错误。
func TestConcurrentRegisterSerializes(t *testing.T) {
	svc, clk, start := newFixture(t)
	clk.advance(time.Hour)

	const n = 16
	var wg sync.WaitGroup
	var mu sync.Mutex
	var success, already int
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			_, err := svc.RegisterIntake(RegisterCommand{
				CourseID: "C1", PlannedAt: start, ExpectedVersion: 1,
			})
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				success++
			case errors.Is(err, ErrAlreadyRegistered):
				already++
			}
		}()
	}
	wg.Wait()
	if success != 1 || already != n-1 {
		t.Fatalf("want exactly one success, got success=%d already=%d", success, already)
	}

	// 登记发生后，调整的生效时间不得早于该服药事实。
	_, err := svc.Adjust(AdjustCommand{
		CourseID:    "C1",
		Dosage:      Dosage{Amount: 5, Unit: "mg"},
		Frequency:   Frequency{Interval: time.Hour},
		EffectiveAt: start.Add(-time.Hour),
		RequestID:   "adj-race",
	})
	if !errors.Is(err, ErrEffectiveInPast) && !errors.Is(err, ErrEffectiveBeforeIntake) {
		t.Fatalf("concurrent adjust must be guarded, got %v", err)
	}
}

// 暂停/恢复：暂停区间不产生计划点；恢复后按恢复时刻以当时版本重排，
// 已登记事实不受影响。
func TestPauseResumeKeepsHistory(t *testing.T) {
	svc, clk, start := newFixture(t)

	// 08:00 服药，09:00 暂停，12:00 恢复。
	if _, err := svc.RegisterIntake(RegisterCommand{
		CourseID: "C1", PlannedAt: start, ExpectedVersion: 1,
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	clk.advance(time.Hour)
	if _, err := svc.Pause("C1", "doctor", clk.now(), "sick"); err != nil {
		t.Fatalf("pause: %v", err)
	}
	resumeAt := start.Add(4 * time.Hour)
	if err := svc.Resume("C1", "doctor", resumeAt); err != nil {
		t.Fatalf("resume: %v", err)
	}

	entries, err := svc.Schedule("C1", start, start.Add(30*time.Hour))
	if err != nil {
		t.Fatalf("schedule: %v", err)
	}
	for _, e := range entries {
		if e.ScheduledAt.After(clk.now()) && e.ScheduledAt.Before(resumeAt) {
			t.Fatalf("point generated inside paused gap: %+v", e)
		}
	}
	first := entries[0]
	if first.ScheduledAt != start || first.Version != 1 || first.Status != PointRegistered {
		t.Fatalf("historical intake must survive pause/resume: %+v", first)
	}
	// 恢复后的第一个点从恢复时刻开始，仍为版本 1。
	if entries[len(entries)-1].ScheduledAt.Before(resumeAt) {
		t.Fatalf("no regenerated points after resume")
	}
	if e := findEntry(entries, resumeAt); e == nil || e.Version != 1 {
		t.Fatalf("resume anchor point missing or wrong version: %+v", entries)
	}
}

// 完成后：未来点作废，历史点仍可补记；完成的疗程不能再调整。
func TestCompleteThenBackfill(t *testing.T) {
	svc, clk, start := newFixture(t)
	// 在 09:00 完成疗程。
	clk.advance(time.Hour)
	completeAt := clk.now()
	// 先把未来计划点展开出来，完成时它们应被置为 voided 而非消失。
	if _, err := svc.Schedule("C1", start, start.Add(24*time.Hour)); err != nil {
		t.Fatalf("pre-complete schedule: %v", err)
	}
	if err := svc.Complete("C1", "doctor", completeAt); err != nil {
		t.Fatalf("complete: %v", err)
	}
	// 14:00 的点已经 voided，不能登记。
	if _, err := svc.RegisterIntake(RegisterCommand{
		CourseID: "C1", PlannedAt: start.Add(6 * time.Hour), ExpectedVersion: 1,
	}); !errors.Is(err, ErrCourseClosed) {
		t.Fatalf("future point after completion must be closed, got %v", err)
	}
	// 完成时刻之前的 08:00 历史点允许携带版本 1 补记。
	rec, err := svc.RegisterIntake(RegisterCommand{
		CourseID: "C1", PlannedAt: start, ExpectedVersion: 1,
	})
	if err != nil {
		t.Fatalf("backfill before completion should succeed: %v", err)
	}
	if !rec.Backfilled || rec.Version != 1 {
		t.Fatalf("unexpected backfill record: %+v", rec)
	}
	// 再调整被拒绝。
	_, err = svc.Adjust(AdjustCommand{
		CourseID:    "C1",
		Dosage:      Dosage{Amount: 1, Unit: "mg"},
		Frequency:   Frequency{Interval: time.Hour},
		EffectiveAt: completeAt.Add(time.Hour),
		RequestID:   "a",
	})
	if !errors.Is(err, ErrCourseClosed) {
		t.Fatalf("completed course cannot be adjusted, got %v", err)
	}
}

// 登记幂等：同号同内容重复登记返回同一服药事实，不产生第二条事实。
func TestRegisterIdempotency(t *testing.T) {
	svc, _, start := newFixture(t)
	cmd := RegisterCommand{
		CourseID: "C1", PlannedAt: start, ExpectedVersion: 1, RequestID: "reg-1",
	}
	first, err := svc.RegisterIntake(cmd)
	if err != nil {
		t.Fatalf("first register: %v", err)
	}
	again, err := svc.RegisterIntake(cmd)
	if err != nil {
		t.Fatalf("idempotent register: %v", err)
	}
	if again != first {
		t.Fatalf("retry must return the original intake: %+v vs %+v", first, again)
	}
	if intakes, _ := svc.Intakes("C1"); len(intakes) != 1 {
		t.Fatalf("retry duplicated intake: %+v", intakes)
	}
	diff := cmd
	diff.ExpectedVersion = 2
	if _, err := svc.RegisterIntake(diff); !errors.Is(err, ErrRequestConflict) {
		t.Fatalf("same request id with different content must conflict, got %v", err)
	}
}

func findEntry(es []ScheduleEntry, at time.Time) *ScheduleEntry {
	for i := range es {
		if es[i].ScheduledAt.Equal(at) {
			return &es[i]
		}
	}
	return nil
}
