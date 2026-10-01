package medication

import (
	"errors"
	"sync"
	"testing"
	"time"
)

var testStart = time.Date(2026, 1, 1, 8, 0, 0, 0, time.UTC)

// fixedClock 返回可手动推进的时钟，保证测试确定。
type fixedClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fixedClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fixedClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func newTestService() (*Service, *fixedClock) {
	clock := &fixedClock{now: testStart}
	return NewService(clock.Now), clock
}

func validInput() CreateCourseInput {
	return CreateCourseInput{
		PatientID:       "patient-1",
		Medication:      "阿莫西林",
		Dose:            Dose{Amount: 250, Unit: "mg"},
		FrequencyPerDay: 2,
		StartTime:       testStart,
		PlannedEndTime:  testStart.Add(7 * 24 * time.Hour),
	}
}

func mustCreate(t *testing.T, s *Service) *Course {
	t.Helper()
	course, err := s.CreateCourse(validInput())
	if err != nil {
		t.Fatalf("CreateCourse failed: %v", err)
	}
	return course
}

func TestCreateCourseSuccess(t *testing.T) {
	s, _ := newTestService()
	course := mustCreate(t, s)

	if course.ID == "" {
		t.Fatal("expected course ID to be assigned")
	}
	if course.Status != StatusActive {
		t.Fatalf("expected status %q, got %q", StatusActive, course.Status)
	}
	if course.Dose.Amount != 250 || course.Dose.Unit != "mg" {
		t.Fatalf("unexpected dose: %+v", course.Dose)
	}
}

func TestCreateCourseRejectsInvalidInput(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*CreateCourseInput)
	}{
		{"missing patient", func(in *CreateCourseInput) { in.PatientID = "" }},
		{"missing medication", func(in *CreateCourseInput) { in.Medication = "  " }},
		{"zero dose", func(in *CreateCourseInput) { in.Dose.Amount = 0 }},
		{"negative dose", func(in *CreateCourseInput) { in.Dose.Amount = -5 }},
		{"missing dose unit", func(in *CreateCourseInput) { in.Dose.Unit = "" }},
		{"zero frequency", func(in *CreateCourseInput) { in.FrequencyPerDay = 0 }},
		{"excessive frequency", func(in *CreateCourseInput) { in.FrequencyPerDay = 99 }},
		{"missing start", func(in *CreateCourseInput) { in.StartTime = time.Time{} }},
		{"end before start", func(in *CreateCourseInput) { in.PlannedEndTime = in.StartTime.Add(-time.Hour) }},
		{"end equals start", func(in *CreateCourseInput) { in.PlannedEndTime = in.StartTime }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := newTestService()
			in := validInput()
			tc.mutate(&in)
			_, err := s.CreateCourse(in)
			if err == nil {
				t.Fatal("expected validation error, got nil")
			}
			if !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("expected ErrInvalidInput, got %v", err)
			}
			var ve *ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("expected ValidationError, got %T", err)
			}
			if ve.Message == "" {
				t.Fatal("expected user-facing message")
			}
		})
	}
}

func TestConfirmDoseIsIdempotent(t *testing.T) {
	s, clock := newTestService()
	course := mustCreate(t, s)
	scheduled := testStart

	clock.Advance(time.Minute)
	first, created, err := s.ConfirmDose(course.ID, scheduled)
	if err != nil {
		t.Fatalf("ConfirmDose failed: %v", err)
	}
	if !created {
		t.Fatal("first submission should be created")
	}

	clock.Advance(time.Minute)
	second, created, err := s.ConfirmDose(course.ID, scheduled)
	if err != nil {
		t.Fatalf("duplicate ConfirmDose failed: %v", err)
	}
	if created {
		t.Fatal("duplicate submission should not create a new record")
	}
	if second.ID != first.ID {
		t.Fatalf("expected existing record %q, got %q", first.ID, second.ID)
	}

	// 漏服与补记同样幂等，且不会增加服药数量。
	if _, _, err := s.MarkMissed(course.ID, scheduled); err != nil {
		t.Fatalf("MarkMissed on existing key failed: %v", err)
	}
	adherence, err := s.Adherence(course.ID)
	if err != nil {
		t.Fatalf("Adherence failed: %v", err)
	}
	if adherence.TakenCount != 1 {
		t.Fatalf("expected 1 taken record, got %d", adherence.TakenCount)
	}
	records, err := s.ListRecords(course.ID)
	if err != nil {
		t.Fatalf("ListRecords failed: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(records))
	}
}

func TestBackfillRequiresOccurredAt(t *testing.T) {
	s, clock := newTestService()
	course := mustCreate(t, s)

	if _, _, err := s.BackfillDose(course.ID, testStart, time.Time{}, ""); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput for missing occurred_at, got %v", err)
	}

	clock.Advance(2 * time.Hour)
	future := clock.Now().Add(time.Hour)
	if _, _, err := s.BackfillDose(course.ID, testStart, future, ""); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput for future occurred_at, got %v", err)
	}

	occurred := clock.Now().Add(-time.Hour)
	rec, created, err := s.BackfillDose(course.ID, testStart, occurred, "早上忘记登记")
	if err != nil {
		t.Fatalf("BackfillDose failed: %v", err)
	}
	if !created {
		t.Fatal("expected new backfill record")
	}
	if rec.Kind != RecordBackfilled {
		t.Fatalf("expected kind %q, got %q", RecordBackfilled, rec.Kind)
	}
	if !rec.OccurredAt.Equal(occurred) {
		t.Fatalf("occurred_at should be the historical time %v, got %v", occurred, rec.OccurredAt)
	}
	if rec.RecordedAt.Before(rec.OccurredAt) {
		t.Fatal("recorded_at must not be earlier than occurred_at for a backfill")
	}
}

func TestPauseBlocksAndResumeAllowsDosing(t *testing.T) {
	s, clock := newTestService()
	course := mustCreate(t, s)

	if _, err := s.PauseCourse(course.ID); err != nil {
		t.Fatalf("PauseCourse failed: %v", err)
	}

	clock.Advance(time.Hour)
	_, _, err := s.ConfirmDose(course.ID, testStart)
	if err == nil || !errors.Is(err, ErrInvalidState) {
		t.Fatalf("expected ErrInvalidState while paused, got %v", err)
	}

	// 重复暂停是幂等的，不会报错。
	if _, err := s.PauseCourse(course.ID); err != nil {
		t.Fatalf("re-pause should be idempotent, got %v", err)
	}

	if _, err := s.ResumeCourse(course.ID); err != nil {
		t.Fatalf("ResumeCourse failed: %v", err)
	}
	rec, created, err := s.ConfirmDose(course.ID, testStart)
	if err != nil {
		t.Fatalf("ConfirmDose after resume failed: %v", err)
	}
	if !created || rec.Kind != RecordTaken {
		t.Fatalf("unexpected record after resume: %+v", rec)
	}
}

func TestCompleteCourseBlocksFurtherDosing(t *testing.T) {
	s, clock := newTestService()
	course := mustCreate(t, s)

	clock.Advance(time.Hour)
	if _, _, err := s.ConfirmDose(course.ID, testStart); err != nil {
		t.Fatalf("ConfirmDose failed: %v", err)
	}

	done, err := s.CompleteCourse(course.ID)
	if err != nil {
		t.Fatalf("CompleteCourse failed: %v", err)
	}
	if done.Status != StatusCompleted {
		t.Fatalf("expected status %q, got %q", StatusCompleted, done.Status)
	}

	_, _, err = s.ConfirmDose(course.ID, testStart.Add(12*time.Hour))
	if !errors.Is(err, ErrInvalidState) {
		t.Fatalf("expected ErrInvalidState after completion, got %v", err)
	}

	// 已记录的服药事实保留。
	records, err := s.ListRecords(course.ID)
	if err != nil {
		t.Fatalf("ListRecords failed: %v", err)
	}
	if len(records) != 1 || records[0].Kind != RecordTaken {
		t.Fatalf("expected preserved taken record, got %+v", records)
	}

	// 已完成的疗程不能取消。
	if _, err := s.CancelCourse(course.ID); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("expected ErrInvalidState cancelling completed course, got %v", err)
	}
}

func TestCancelKeepsFactsAndBlocksFutureDosing(t *testing.T) {
	s, clock := newTestService()
	course := mustCreate(t, s)

	clock.Advance(26 * time.Hour)
	if _, _, err := s.ConfirmDose(course.ID, testStart); err != nil {
		t.Fatalf("ConfirmDose failed: %v", err)
	}
	if _, err := s.CancelCourse(course.ID); err != nil {
		t.Fatalf("CancelCourse failed: %v", err)
	}

	// 取消之后的时间点不能登记为正常服药。
	futureSlot := testStart.Add(36 * time.Hour)
	if _, _, err := s.ConfirmDose(course.ID, futureSlot); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("expected ErrInvalidState for post-cancel slot, got %v", err)
	}
	if _, _, err := s.BackfillDose(course.ID, futureSlot, clock.Now(), ""); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("expected ErrInvalidState for post-cancel backfill, got %v", err)
	}

	// 取消前的历史时间点仍允许补记事实。
	pastSlot := testStart.Add(12 * time.Hour)
	if _, _, err := s.BackfillDose(course.ID, pastSlot, pastSlot, "补记"); err != nil {
		t.Fatalf("backfill before cancellation should be allowed, got %v", err)
	}

	records, err := s.ListRecords(course.ID)
	if err != nil {
		t.Fatalf("ListRecords failed: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("expected 2 preserved records, got %d", len(records))
	}
}

func TestQueriesAreStableOrdered(t *testing.T) {
	s, clock := newTestService()
	course := mustCreate(t, s)
	other := validInput()
	other.PatientID = "patient-2"
	if _, err := s.CreateCourse(other); err != nil {
		t.Fatalf("CreateCourse failed: %v", err)
	}

	clock.Advance(3 * 24 * time.Hour)
	// 故意乱序登记。
	slots := []time.Time{
		testStart.Add(24 * time.Hour),
		testStart,
		testStart.Add(12 * time.Hour),
	}
	for _, slot := range slots {
		if _, _, err := s.ConfirmDose(course.ID, slot); err != nil {
			t.Fatalf("ConfirmDose failed: %v", err)
		}
	}
	if _, _, err := s.MarkMissed(course.ID, testStart.Add(36*time.Hour)); err != nil {
		t.Fatalf("MarkMissed failed: %v", err)
	}

	records, err := s.ListRecords(course.ID)
	if err != nil {
		t.Fatalf("ListRecords failed: %v", err)
	}
	for i := 1; i < len(records); i++ {
		if records[i].ScheduledTime.Before(records[i-1].ScheduledTime) {
			t.Fatalf("records not sorted by scheduled time: %v before %v",
				records[i].ScheduledTime, records[i-1].ScheduledTime)
		}
	}

	courses := s.ListCoursesByPatient("patient-1")
	if len(courses) != 1 || courses[0].ID != course.ID {
		t.Fatalf("unexpected courses for patient-1: %+v", courses)
	}

	adherence, err := s.Adherence(course.ID)
	if err != nil {
		t.Fatalf("Adherence failed: %v", err)
	}
	if adherence.TakenCount != 3 || adherence.MissedCount != 1 {
		t.Fatalf("unexpected adherence: %+v", adherence)
	}
	if adherence.DueCount != 7 { // 8:00 起每 12 小时一次，3 天内到点 7 次
		t.Fatalf("expected 7 due slots, got %d", adherence.DueCount)
	}
	if adherence.PendingCount != 3 {
		t.Fatalf("expected 3 pending slots, got %d", adherence.PendingCount)
	}

	planned, err := s.PlannedTimes(course.ID, testStart, testStart.Add(24*time.Hour))
	if err != nil {
		t.Fatalf("PlannedTimes failed: %v", err)
	}
	if len(planned) != 3 {
		t.Fatalf("expected 3 planned slots in first 24h, got %d", len(planned))
	}
}

func TestNotFoundErrors(t *testing.T) {
	s, _ := newTestService()
	if _, err := s.GetCourse("missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	if _, _, err := s.ConfirmDose("missing", testStart); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	if _, err := s.PauseCourse("missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}
