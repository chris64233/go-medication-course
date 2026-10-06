package gomedicationcourse

import (
	"errors"
	"sync"
	"testing"
	"time"
)

var t0 = time.Date(2026, 1, 1, 8, 0, 0, 0, time.UTC)

func newServiceAt(now time.Time) *Service {
	s := NewService()
	s.SetClock(func() time.Time { return now })
	return s
}

func mustCourse(t *testing.T, s *Service, start time.Time, dose float64, interval int) *Course {
	t.Helper()
	c, err := s.CreateCourse("patient-1", start, dose, interval, "dr.who", "initial")
	if err != nil {
		t.Fatalf("create course: %v", err)
	}
	return c
}

// 跨版本计划：调整前后计划点分别采用各自版本与剂量，历史点不被删除或重算。
func TestCrossVersionPlan(t *testing.T) {
	s := newServiceAt(t0)
	c := mustCourse(t, s, t0, 100, 8)

	if _, err := s.RegisterDose(c.ID, t0, 1); err != nil {
		t.Fatalf("register: %v", err)
	}
	eff := t0.Add(24 * time.Hour)
	if _, err := s.SubmitAdjustment(c.ID, AdjustmentRequest{
		ExternalRequestID: "req-1", DoseMg: 200, IntervalHours: 12,
		EffectiveAt: eff, Author: "dr.who", Reason: "increase",
	}); err != nil {
		t.Fatalf("adjust: %v", err)
	}

	points, err := s.Plan(c.ID, t0, t0.Add(72*time.Hour))
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	// 版本 1：t0, +8h, +16h；版本 2：+24h, +36h, +48h, +60h
	want := []struct {
		at      time.Time
		version int
		dose    float64
	}{
		{t0, 1, 100}, {t0.Add(8 * time.Hour), 1, 100}, {t0.Add(16 * time.Hour), 1, 100},
		{eff, 2, 200}, {eff.Add(12 * time.Hour), 2, 200},
		{eff.Add(24 * time.Hour), 2, 200}, {eff.Add(36 * time.Hour), 2, 200},
	}
	if len(points) != len(want) {
		t.Fatalf("got %d points, want %d: %+v", len(points), len(want), points)
	}
	for i, w := range want {
		p := points[i]
		if !p.ScheduledAt.Equal(w.at) || p.Version != w.version || p.DoseMg != w.dose {
			t.Errorf("point %d = %+v, want time=%v version=%d dose=%v", i, p, w.at, w.version, w.dose)
		}
	}
	if points[0].Status != PointStatusTaken || points[0].Record.Version != 1 || points[0].Record.DoseMg != 100 {
		t.Errorf("taken point lost original version/dose: %+v", points[0])
	}
}

// 边界生效时间：生效时间等于最晚已确认服药事实允许，早于则拒绝；
// 生效时刻起的计划点采用新版本。
func TestBoundaryEffectiveAt(t *testing.T) {
	s := newServiceAt(t0.Add(8 * time.Hour))
	c := mustCourse(t, s, t0, 100, 8)
	if _, err := s.RegisterDose(c.ID, t0.Add(8*time.Hour), 1); err != nil {
		t.Fatalf("register: %v", err)
	}

	if _, err := s.SubmitAdjustment(c.ID, AdjustmentRequest{
		ExternalRequestID: "early", DoseMg: 150, IntervalHours: 8,
		EffectiveAt: t0.Add(4 * time.Hour), Author: "a", Reason: "r",
	}); !errors.Is(err, ErrEffectiveBeforeConfirmed) {
		t.Fatalf("want ErrEffectiveBeforeConfirmed, got %v", err)
	}

	v, err := s.SubmitAdjustment(c.ID, AdjustmentRequest{
		ExternalRequestID: "boundary", DoseMg: 150, IntervalHours: 8,
		EffectiveAt: t0.Add(8 * time.Hour), Author: "a", Reason: "r",
	})
	if err != nil {
		t.Fatalf("boundary effectiveAt should be accepted: %v", err)
	}
	if v.Version != 2 {
		t.Fatalf("version = %d, want 2", v.Version)
	}
	// 已确认事实仍是版本 1；生效时刻起的新计划点采用版本 2。
	points, _ := s.Plan(c.ID, t0, t0.Add(32*time.Hour))
	if points[1].Record == nil || points[1].Record.Version != 1 {
		t.Errorf("confirmed fact must keep version 1: %+v", points[0])
	}
	if points[2].Version != 2 || points[2].DoseMg != 150 {
		t.Errorf("future point must use version 2: %+v", points[1])
	}
}

// 补记限制：补记保留原版本原剂量；不存在的计划点、重复登记、
// 旧版本登记未来服药均被拒绝。
func TestBackfillRestrictions(t *testing.T) {
	now := t0.Add(30 * time.Hour)
	s := newServiceAt(now)
	c := mustCourse(t, s, t0, 100, 8)
	if _, err := s.SubmitAdjustment(c.ID, AdjustmentRequest{
		ExternalRequestID: "adj", DoseMg: 200, IntervalHours: 8,
		EffectiveAt: t0.Add(24 * time.Hour), Author: "a", Reason: "r",
	}); err != nil {
		t.Fatalf("adjust: %v", err)
	}

	// 补记调整前的历史点：保留版本 1 与原剂量，不被新版本伪装。
	rec, err := s.RegisterDose(c.ID, t0.Add(8*time.Hour), 1)
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if !rec.Backfill || rec.Version != 1 || rec.DoseMg != 100 {
		t.Errorf("backfill must keep original version/dose: %+v", rec)
	}
	// 重复登记同一计划点。
	if _, err := s.RegisterDose(c.ID, t0.Add(8*time.Hour), 1); !errors.Is(err, ErrDoseAlreadyRecorded) {
		t.Errorf("want ErrDoseAlreadyRecorded, got %v", err)
	}
	// 非计划时间点。
	if _, err := s.RegisterDose(c.ID, t0.Add(3*time.Hour), 1); !errors.Is(err, ErrNoPlannedDose) {
		t.Errorf("want ErrNoPlannedDose, got %v", err)
	}
	// 携带旧版本登记调整后的未来服药。
	if _, err := s.RegisterDose(c.ID, t0.Add(32*time.Hour), 1); !errors.Is(err, ErrVersionConflict) {
		t.Errorf("want ErrVersionConflict, got %v", err)
	}
	if _, err := s.RegisterDose(c.ID, t0.Add(32*time.Hour), 2); err != nil {
		t.Errorf("current version should register: %v", err)
	}
}

// 并发登记与并发调整：同一计划点只能登记成功一次；
// 调整生效后旧版本登记必须冲突；终止后不能再调整。
func TestConcurrentRegistrationAndAdjustment(t *testing.T) {
	s := newServiceAt(t0)
	c := mustCourse(t, s, t0, 100, 8)

	var wg sync.WaitGroup
	var mu sync.Mutex
	errs := map[error]int{}
	record := func(err error) {
		mu.Lock()
		errs[err]++
		mu.Unlock()
	}

	// 并发登记同一计划点：恰好一次成功。
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.RegisterDose(c.ID, t0, 1)
			record(err)
		}()
	}
	wg.Wait()
	if errs[nil] != 1 || errs[ErrDoseAlreadyRecorded] != 15 {
		t.Fatalf("concurrent register: %v", errs)
	}

	// 并发提交不同内容的调整：版本号必须连续递增，无丢号。
	errs = map[error]int{}
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := s.SubmitAdjustment(c.ID, AdjustmentRequest{
				ExternalRequestID: string(rune('a' + i)),
				DoseMg:            float64(100 + i), IntervalHours: 8,
				EffectiveAt: t0, Author: "a", Reason: "r",
			})
			record(err)
		}(i)
	}
	wg.Wait()
	if errs[nil] != 8 {
		t.Fatalf("concurrent adjust: %v", errs)
	}
	hist, _ := s.AdjustmentHistory(c.ID)
	for i, v := range hist {
		if v.Version != i+1 {
			t.Fatalf("versions not strictly increasing: %+v", hist)
		}
	}

	// 并发完成与调整：裁决后要么调整先成功，要么返回疗程已终止。
	errs = map[error]int{}
	wg.Add(2)
	go func() {
		defer wg.Done()
		record(s.CompleteCourse(c.ID))
	}()
	go func() {
		defer wg.Done()
		_, err := s.SubmitAdjustment(c.ID, AdjustmentRequest{
			ExternalRequestID: "last", DoseMg: 500, IntervalHours: 8,
			EffectiveAt: t0, Author: "a", Reason: "r",
		})
		record(err)
	}()
	wg.Wait()
	if errs[nil]+errs[ErrCourseTerminated] != 2 || errs[nil] == 0 && errs[ErrCourseTerminated] == 0 {
		t.Fatalf("complete/adjust race: %v", errs)
	}

	// 已完成疗程：不能再调整或登记。
	if _, err := s.SubmitAdjustment(c.ID, AdjustmentRequest{
		ExternalRequestID: "after", DoseMg: 1, IntervalHours: 1, EffectiveAt: t0,
	}); !errors.Is(err, ErrCourseTerminated) {
		t.Errorf("want ErrCourseTerminated, got %v", err)
	}
	if _, err := s.RegisterDose(c.ID, t0.Add(8*time.Hour), 1); !errors.Is(err, ErrCourseTerminated) {
		t.Errorf("want ErrCourseTerminated, got %v", err)
	}
}

// 重复请求：同号同内容返回原调整；同号不同剂量/频次/生效时间返回冲突；
// 失败请求不产生任何部分变更。
func TestDuplicateRequestIdempotency(t *testing.T) {
	s := newServiceAt(t0)
	c := mustCourse(t, s, t0, 100, 8)
	req := AdjustmentRequest{
		ExternalRequestID: "req-x", DoseMg: 200, IntervalHours: 12,
		EffectiveAt: t0.Add(24 * time.Hour), Author: "a", Reason: "r",
	}
	v1, err := s.SubmitAdjustment(c.ID, req)
	if err != nil {
		t.Fatalf("adjust: %v", err)
	}
	v2, err := s.SubmitAdjustment(c.ID, req)
	if err != nil || v2 != v1 {
		t.Fatalf("idempotent replay must return original adjustment: %v %v", v2, err)
	}
	hist, _ := s.AdjustmentHistory(c.ID)
	if len(hist) != 2 {
		t.Fatalf("replay must not create a new version: %d", len(hist))
	}

	for _, mutate := range []func(*AdjustmentRequest){
		func(r *AdjustmentRequest) { r.DoseMg = 300 },
		func(r *AdjustmentRequest) { r.IntervalHours = 6 },
		func(r *AdjustmentRequest) { r.EffectiveAt = t0.Add(48 * time.Hour) },
	} {
		conflicting := req
		mutate(&conflicting)
		if _, err := s.SubmitAdjustment(c.ID, conflicting); !errors.Is(err, ErrRequestConflict) {
			t.Errorf("want ErrRequestConflict, got %v", err)
		}
	}

	// 失败调整（生效时间早于已确认事实）不得改变计划与服药记录。
	if _, err := s.RegisterDose(c.ID, t0.Add(24*time.Hour), 2); err != nil {
		t.Fatalf("register: %v", err)
	}
	before, _ := s.Plan(c.ID, t0, t0.Add(72*time.Hour))
	if _, err := s.SubmitAdjustment(c.ID, AdjustmentRequest{
		ExternalRequestID: "bad", DoseMg: 999, IntervalHours: 1, EffectiveAt: t0,
	}); !errors.Is(err, ErrEffectiveBeforeConfirmed) {
		t.Fatalf("want ErrEffectiveBeforeConfirmed, got %v", err)
	}
	after, _ := s.Plan(c.ID, t0, t0.Add(72*time.Hour))
	if len(before) != len(after) {
		t.Fatalf("failed adjustment changed the plan")
	}
	for i := range before {
		if before[i] != after[i] && (before[i].ScheduledAt != after[i].ScheduledAt ||
			before[i].Version != after[i].Version || before[i].DoseMg != after[i].DoseMg) {
			t.Fatalf("failed adjustment mutated plan point %d", i)
		}
	}
	hist, _ = s.AdjustmentHistory(c.ID)
	if len(hist) != 2 {
		t.Fatalf("failed adjustment created a version: %d", len(hist))
	}
}

// 暂停/恢复：暂停期间登记与调整被拒绝，恢复后可用同一版本继续。
func TestPauseResume(t *testing.T) {
	s := newServiceAt(t0)
	c := mustCourse(t, s, t0, 100, 8)
	if err := s.PauseCourse(c.ID); err != nil {
		t.Fatalf("pause: %v", err)
	}
	if _, err := s.RegisterDose(c.ID, t0, 1); !errors.Is(err, ErrCoursePaused) {
		t.Errorf("want ErrCoursePaused, got %v", err)
	}
	if _, err := s.SubmitAdjustment(c.ID, AdjustmentRequest{
		ExternalRequestID: "p", DoseMg: 1, IntervalHours: 1, EffectiveAt: t0,
	}); !errors.Is(err, ErrCoursePaused) {
		t.Errorf("want ErrCoursePaused, got %v", err)
	}
	if err := s.ResumeCourse(c.ID); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if _, err := s.RegisterDose(c.ID, t0, 1); err != nil {
		t.Errorf("register after resume: %v", err)
	}
	if err := s.CancelCourse(c.ID); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if _, err := s.SubmitAdjustment(c.ID, AdjustmentRequest{
		ExternalRequestID: "c", DoseMg: 1, IntervalHours: 1, EffectiveAt: t0,
	}); !errors.Is(err, ErrCourseTerminated) {
		t.Errorf("want ErrCourseTerminated, got %v", err)
	}
}
