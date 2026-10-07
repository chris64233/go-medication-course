package gomedicationcourse

import (
	"sync"
	"testing"
	"time"
)

func mustCreate(t *testing.T, svc *Service, id string, start time.Time, every time.Duration, amount float64) *Course {
	t.Helper()
	c, err := svc.CreateCourse(CreateCourseRequest{
		ID:         id,
		Medication: "drug",
		Dose:       Dose{Amount: amount, Unit: "mg"},
		Frequency:  Frequency{Every: every},
		StartAt:    start,
		Operator:   "dr-a",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	return c
}

func TestCrossVersionPlanAndFrozenFacts(t *testing.T) {
	now := time.Date(2026, 1, 1, 8, 0, 0, 0, time.UTC)
	svc := NewService(nil).WithClock(func() time.Time { return now })
	mustCreate(t, svc, "c1", now, 8*time.Hour, 100)

	in, err := svc.RegisterIntake(RegisterIntakeRequest{
		CourseID: "c1", ScheduledAt: now, TakenAt: now, ExpectedVersion: 1, Operator: "nurse",
	})
	if err != nil {
		t.Fatalf("register v1: %v", err)
	}

	eff := now.Add(24 * time.Hour)
	v2, err := svc.AdjustPrescription(AdjustPrescriptionRequest{
		CourseID: "c1", Dose: Dose{Amount: 200, Unit: "mg"},
		Frequency: Frequency{Every: 12 * time.Hour}, EffectiveAt: eff,
		Operator: "dr-b", Reason: "escalation", SubmittedAt: now.Add(time.Hour),
		RequestID: "adj-1", ExpectedVersion: 1,
	})
	if err != nil {
		t.Fatalf("adjust: %v", err)
	}
	if v2.Version != 2 || v2.AdjustedBy != "dr-b" || v2.Reason != "escalation" {
		t.Fatalf("adjustment metadata wrong: %+v", v2)
	}

	c, _ := svc.GetCourse("c1")
	plan, err := ExpandPlan(c, now, now.Add(48*time.Hour))
	if err != nil {
		t.Fatalf("expand: %v", err)
	}
	want := []struct {
		at      time.Time
		version int
		amount  float64
	}{
		{now, 1, 100},
		{now.Add(8 * time.Hour), 1, 100},
		{now.Add(16 * time.Hour), 1, 100},
		{eff, 2, 200},
		{eff.Add(12 * time.Hour), 2, 200},
	}
	if len(plan) != len(want) {
		t.Fatalf("plan len = %d (%+v), want %d", len(plan), plan, len(want))
	}
	for i, w := range want {
		if !plan[i].PlannedAt.Equal(w.at) || plan[i].Version != w.version || plan[i].Dose.Amount != w.amount {
			t.Fatalf("point %d = %+v, want at %s v%d %vmg", i, plan[i], w.at, w.version, w.amount)
		}
	}
	if plan[0].Taken == nil || plan[0].Taken.Version != 1 || plan[0].Taken.Dose.Amount != 100 {
		t.Fatalf("historical fact not preserved on plan: %+v", plan[0].Taken)
	}
	if in.Version != 1 || in.Dose.Amount != 100 || in.Backfill {
		t.Fatalf("intake fact mutated after adjustment: %+v", in)
	}
}

func TestBoundaryEffectiveTime(t *testing.T) {
	now := time.Date(2026, 2, 1, 9, 0, 0, 0, time.UTC)
	svc := NewService(nil)
	mustCreate(t, svc, "c1", now, 24*time.Hour, 10)
	eff := now.Add(24 * time.Hour)
	if _, err := svc.AdjustPrescription(AdjustPrescriptionRequest{
		CourseID: "c1", Dose: Dose{Amount: 20, Unit: "mg"}, Frequency: Frequency{Every: 24 * time.Hour},
		EffectiveAt: eff, Operator: "dr", RequestID: "r",
	}); err != nil {
		t.Fatalf("adjust at boundary: %v", err)
	}
	// The boundary instant belongs to the new version; the old version must
	// conflict there.
	if _, err := svc.RegisterIntake(RegisterIntakeRequest{
		CourseID: "c1", ScheduledAt: eff, TakenAt: eff, ExpectedVersion: 1,
	}); !IsConflict(err, ConflictVersion) {
		t.Fatalf("old version at boundary: err = %v, want version conflict", err)
	}
	in, err := svc.RegisterIntake(RegisterIntakeRequest{
		CourseID: "c1", ScheduledAt: eff, TakenAt: eff, ExpectedVersion: 2,
	})
	if err != nil {
		t.Fatalf("new version at boundary: %v", err)
	}
	if in.Version != 2 || in.Backfill {
		t.Fatalf("boundary intake = %+v", in)
	}
}

func TestEffectiveTimeCannotPrecedeConfirmedFact(t *testing.T) {
	now := time.Date(2026, 3, 1, 8, 0, 0, 0, time.UTC)
	svc := NewService(nil)
	mustCreate(t, svc, "c1", now, 24*time.Hour, 10)
	later := now.Add(24 * time.Hour)
	if _, err := svc.RegisterIntake(RegisterIntakeRequest{
		CourseID: "c1", ScheduledAt: later, TakenAt: later, ExpectedVersion: 1,
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	if _, err := svc.AdjustPrescription(AdjustPrescriptionRequest{
		CourseID: "c1", Dose: Dose{Amount: 20, Unit: "mg"}, Frequency: Frequency{Every: 24 * time.Hour},
		EffectiveAt: later, Operator: "dr", RequestID: "r",
	}); !IsConflict(err, ConflictEffectiveTime) {
		t.Fatalf("equal-to-fact effective: err = %v, want effective time conflict", err)
	}
	if _, err := svc.AdjustPrescription(AdjustPrescriptionRequest{
		CourseID: "c1", Dose: Dose{Amount: 20, Unit: "mg"}, Frequency: Frequency{Every: 24 * time.Hour},
		EffectiveAt: later.Add(time.Hour), Operator: "dr", RequestID: "r2",
	}); err != nil {
		t.Fatalf("strictly after fact effective: %v", err)
	}
}

func TestBackfillRules(t *testing.T) {
	now := time.Date(2026, 4, 1, 8, 0, 0, 0, time.UTC)
	svc := NewService(nil).WithClock(func() time.Time { return now.Add(48 * time.Hour) })
	mustCreate(t, svc, "c1", now, 24*time.Hour, 10)

	in, err := svc.RegisterIntake(RegisterIntakeRequest{
		CourseID: "c1", ScheduledAt: now, ExpectedVersion: 1,
		Operator: "n", Reason: "late entry",
	})
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if !in.Backfill || in.Version != 1 || in.Dose.Amount != 10 {
		t.Fatalf("backfill intake = %+v", in)
	}
	// A backfill cannot be disguised under a version that did not govern the
	// scheduled time.
	if _, err := svc.RegisterIntake(RegisterIntakeRequest{
		CourseID: "c1", ScheduledAt: now.Add(24 * time.Hour), ExpectedVersion: 2,
	}); !IsConflict(err, ConflictVersion) {
		t.Fatalf("backfill under wrong version: err = %v", err)
	}
	if _, err := svc.RegisterIntake(RegisterIntakeRequest{
		CourseID: "c1", ScheduledAt: now.Add(time.Hour), ExpectedVersion: 1,
	}); !IsConflict(err, ConflictInvalid) {
		t.Fatalf("non-planned point: err = %v", err)
	}
	if _, err := svc.RegisterIntake(RegisterIntakeRequest{
		CourseID: "c1", ScheduledAt: now, ExpectedVersion: 1,
	}); !IsConflict(err, ConflictDuplicate) {
		t.Fatalf("duplicate: err = %v", err)
	}
}

func TestIdempotentAdjustmentAndConflict(t *testing.T) {
	now := time.Date(2026, 5, 1, 8, 0, 0, 0, time.UTC)
	svc := NewService(nil)
	mustCreate(t, svc, "c1", now, 24*time.Hour, 10)
	req := AdjustPrescriptionRequest{
		CourseID: "c1", Dose: Dose{Amount: 20, Unit: "mg"},
		Frequency: Frequency{Every: 12 * time.Hour}, EffectiveAt: now.Add(24 * time.Hour),
		Operator: "dr", RequestID: "req-1",
	}
	first, err := svc.AdjustPrescription(req)
	if err != nil {
		t.Fatalf("first adjust: %v", err)
	}
	second, err := svc.AdjustPrescription(req)
	if err != nil {
		t.Fatalf("idempotent adjust: %v", err)
	}
	if first.Version != second.Version {
		t.Fatalf("same request id produced versions %d and %d", first.Version, second.Version)
	}
	if hist, _ := svc.AdjustmentHistory("c1"); len(hist) != 2 {
		t.Fatalf("history len = %d, want 2", len(hist))
	}
	req.Dose.Amount = 30
	if _, err := svc.AdjustPrescription(req); !IsConflict(err, ConflictIdempotency) {
		t.Fatalf("changed payload: err = %v", err)
	}
	// A failed adjustment must leave no partial state.
	if hist, _ := svc.AdjustmentHistory("c1"); len(hist) != 2 {
		t.Fatalf("failed adjustment mutated versions: %d", len(hist))
	}
}

func TestTerminalCourseCannotAdjustOrRegister(t *testing.T) {
	now := time.Date(2026, 6, 1, 8, 0, 0, 0, time.UTC)
	svc := NewService(nil)
	mustCreate(t, svc, "c1", now, 24*time.Hour, 10)
	if err := svc.Complete(StatusEventRequest{CourseID: "c1", At: now, ExpectedVersion: 1}); err != nil {
		t.Fatalf("complete: %v", err)
	}
	if _, err := svc.AdjustPrescription(AdjustPrescriptionRequest{
		CourseID: "c1", Dose: Dose{Amount: 20, Unit: "mg"}, Frequency: Frequency{Every: time.Hour},
		EffectiveAt: now.Add(24 * time.Hour), RequestID: "x",
	}); !IsConflict(err, ConflictStatus) {
		t.Fatalf("adjust completed: err = %v", err)
	}
	if _, err := svc.RegisterIntake(RegisterIntakeRequest{
		CourseID: "c1", ScheduledAt: now, ExpectedVersion: 1,
	}); !IsConflict(err, ConflictStatus) {
		t.Fatalf("register completed: err = %v", err)
	}

	svc2 := NewService(nil)
	mustCreate(t, svc2, "c2", now, 24*time.Hour, 10)
	if err := svc2.Cancel(StatusEventRequest{CourseID: "c2", At: now, ExpectedVersion: 1}); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if _, err := svc2.AdjustPrescription(AdjustPrescriptionRequest{
		CourseID: "c2", Dose: Dose{Amount: 20, Unit: "mg"}, Frequency: Frequency{Every: time.Hour},
		EffectiveAt: now.Add(24 * time.Hour), RequestID: "y",
	}); !IsConflict(err, ConflictStatus) {
		t.Fatalf("adjust cancelled: err = %v", err)
	}
}

func TestPauseResumeWindows(t *testing.T) {
	now := time.Date(2026, 7, 1, 8, 0, 0, 0, time.UTC)
	svc := NewService(nil)
	mustCreate(t, svc, "c1", now, 8*time.Hour, 10)
	if _, err := svc.RegisterIntake(RegisterIntakeRequest{
		CourseID: "c1", ScheduledAt: now, TakenAt: now, ExpectedVersion: 1,
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := svc.Pause(StatusEventRequest{
		CourseID: "c1", At: now.Add(8 * time.Hour), ExpectedVersion: 1,
	}); err != nil {
		t.Fatalf("pause: %v", err)
	}
	if _, err := svc.RegisterIntake(RegisterIntakeRequest{
		CourseID: "c1", ScheduledAt: now.Add(16 * time.Hour), ExpectedVersion: 1,
	}); !IsConflict(err, ConflictStatus) {
		t.Fatalf("register while paused: err = %v", err)
	}
	if err := svc.Resume(StatusEventRequest{
		CourseID: "c1", At: now.Add(24 * time.Hour), ExpectedVersion: 1,
	}); err != nil {
		t.Fatalf("resume: %v", err)
	}
	c, _ := svc.GetCourse("c1")
	plan, err := ExpandPlan(c, now, now.Add(40*time.Hour))
	if err != nil {
		t.Fatalf("expand: %v", err)
	}
	for _, p := range plan {
		for _, w := range c.Pauses {
			if !p.PlannedAt.Before(w.From) && p.PlannedAt.Before(w.To) {
				t.Fatalf("point %s inside pause window %+v", p.PlannedAt, w)
			}
		}
	}
}

func TestConcurrentRegistrationAndAdjustmentArbitration(t *testing.T) {
	now := time.Date(2026, 8, 1, 8, 0, 0, 0, time.UTC)
	clock := now
	svc := NewService(nil).WithClock(func() time.Time { return clock })
	mustCreate(t, svc, "c1", now, 24*time.Hour, 10)

	// N goroutines register future doses under version 1 while others submit
	// an adjustment. Every accepted operation is arbitrated against a single
	// course state; exactly one registration per time point succeeds.
	future := []time.Time{
		now.Add(24 * time.Hour), now.Add(48 * time.Hour), now.Add(72 * time.Hour),
	}
	var wg sync.WaitGroup
	var regMu sync.Mutex
	accepted := map[string]int{}
	for round := 0; round < 5; round++ {
		for _, ft := range future {
			wg.Add(1)
			go func(at time.Time) {
				defer wg.Done()
				_, err := svc.RegisterIntake(RegisterIntakeRequest{
					CourseID: "c1", ScheduledAt: at, TakenAt: at, ExpectedVersion: 1,
				})
				regMu.Lock()
				if err == nil {
					accepted[at.Format(time.RFC3339)]++
				}
				regMu.Unlock()
			}(ft)
		}
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			_, _ = svc.AdjustPrescription(AdjustPrescriptionRequest{
				CourseID: "c1", Dose: Dose{Amount: 20, Unit: "mg"},
				Frequency:   Frequency{Every: 12 * time.Hour},
				EffectiveAt: now.Add(96 * time.Hour),
				RequestID:   "adj-concurrent",
			})
		}(round)
	}
	wg.Wait()
	for _, ft := range future {
		if accepted[ft.Format(time.RFC3339)] != 1 {
			t.Fatalf("time %s accepted %d times, want exactly 1", ft, accepted[ft.Format(time.RFC3339)])
		}
	}

	// A stale ExpectedVersion on a later status transition is rejected.
	if err := svc.Pause(StatusEventRequest{
		CourseID: "c1", At: now.Add(100 * time.Hour), ExpectedVersion: 1,
	}); !IsConflict(err, ConflictVersion) {
		t.Fatalf("pause on stale version: err = %v", err)
	}
	if err := svc.Pause(StatusEventRequest{
		CourseID: "c1", At: now.Add(100 * time.Hour), ExpectedVersion: 2,
	}); err != nil {
		t.Fatalf("pause on current version: %v", err)
	}
}

func TestStaleVersionFutureRegistrationAndHistoryKept(t *testing.T) {
	now := time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC)
	svc := NewService(nil)
	mustCreate(t, svc, "c1", now, 8*time.Hour, 100)

	// Confirm one historical point before the adjustment.
	if _, err := svc.RegisterIntake(RegisterIntakeRequest{
		CourseID: "c1", ScheduledAt: now, TakenAt: now, ExpectedVersion: 1,
	}); err != nil {
		t.Fatalf("register history: %v", err)
	}
	eff := now.Add(24 * time.Hour)
	if _, err := svc.AdjustPrescription(AdjustPrescriptionRequest{
		CourseID: "c1", Dose: Dose{Amount: 50, Unit: "mg"}, Frequency: Frequency{Every: 6 * time.Hour},
		EffectiveAt: eff, RequestID: "a1",
	}); err != nil {
		t.Fatalf("adjust: %v", err)
	}

	// Carrying the old version to register a future point fails.
	if _, err := svc.RegisterIntake(RegisterIntakeRequest{
		CourseID: "c1", ScheduledAt: eff, TakenAt: eff, ExpectedVersion: 1,
	}); !IsConflict(err, ConflictVersion) {
		t.Fatalf("stale future registration: err = %v", err)
	}

	// Expand far enough that no current plan point sits at the historical
	// instant under the new rhythm: the confirmed fact must still appear.
	c, _ := svc.GetCourse("c1")
	plan, err := ExpandPlan(c, now, eff.Add(6*time.Hour))
	if err != nil {
		t.Fatalf("expand: %v", err)
	}
	var history *PlannedPoint
	for i := range plan {
		if plan[i].PlannedAt.Equal(now) {
			history = &plan[i]
		}
	}
	if history == nil {
		t.Fatalf("historical plan point disappeared after adjustment: %+v", plan)
	}
	if history.Version != 1 || history.Dose.Amount != 100 ||
		history.Taken == nil || history.Taken.Version != 1 {
		t.Fatalf("historical point rewritten: %+v", history)
	}

	// Adjustment history records operator, reason, submitted and effective
	// times with strictly increasing version numbers.
	hist, err := svc.AdjustmentHistory("c1")
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	for i, v := range hist {
		if v.Version != i+1 {
			t.Fatalf("version not monotonic: %+v", hist)
		}
	}
}
