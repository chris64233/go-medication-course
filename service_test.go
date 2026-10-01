package gomedicationcourse

import (
	"context"
	"errors"
	"testing"
	"time"
)

var (
	testStart = time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
	testEnd   = testStart.AddDate(0, 0, 14)
)

func newTestService(now time.Time) *Service {
	return NewService(func() time.Time { return now })
}

func validInput() CreateCourseInput {
	return CreateCourseInput{
		PatientID:       "patient-001",
		Medication:      "阿莫西林",
		Dosage:          Dosage{Amount: 500, Unit: "mg"},
		FrequencyPerDay: 3,
		StartAt:         testStart,
		PlannedEndAt:    testEnd,
	}
}

func mustCreateCourse(t *testing.T, s *Service) *Course {
	t.Helper()
	course, err := s.CreateCourse(context.Background(), validInput())
	if err != nil {
		t.Fatalf("CreateCourse failed: %v", err)
	}
	return course
}

func TestCreateCourseSuccess(t *testing.T) {
	s := newTestService(testStart)
	course := mustCreateCourse(t, s)
	if course.ID == "" {
		t.Fatal("expected course ID to be assigned")
	}
	if course.Status != CourseStatusActive {
		t.Fatalf("expected status active, got %s", course.Status)
	}
}

func TestCreateCourseValidation(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*CreateCourseInput)
	}{
		{"missing patient", func(in *CreateCourseInput) { in.PatientID = "" }},
		{"missing medication", func(in *CreateCourseInput) { in.Medication = "" }},
		{"zero dosage", func(in *CreateCourseInput) { in.Dosage.Amount = 0 }},
		{"negative dosage", func(in *CreateCourseInput) { in.Dosage.Amount = -5 }},
		{"missing dosage unit", func(in *CreateCourseInput) { in.Dosage.Unit = "" }},
		{"zero frequency", func(in *CreateCourseInput) { in.FrequencyPerDay = 0 }},
		{"missing start", func(in *CreateCourseInput) { in.StartAt = time.Time{} }},
		{"missing end", func(in *CreateCourseInput) { in.PlannedEndAt = time.Time{} }},
		{"end before start", func(in *CreateCourseInput) { in.PlannedEndAt = in.StartAt.Add(-time.Hour) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestService(testStart)
			in := validInput()
			tc.mutate(&in)
			_, err := s.CreateCourse(context.Background(), in)
			if !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("expected ErrInvalidInput, got %v", err)
			}
		})
	}
}

func TestRecordDoseDuplicateReturnsExisting(t *testing.T) {
	s := newTestService(testStart)
	course := mustCreateCourse(t, s)
	scheduled := testStart.Add(4 * time.Hour)

	first, created, err := s.RecordDose(context.Background(), RecordDoseInput{
		CourseID:    course.ID,
		ScheduledAt: scheduled,
		Type:        RecordTypeTaken,
	})
	if err != nil || !created {
		t.Fatalf("first record: created=%v err=%v", created, err)
	}

	second, created, err := s.RecordDose(context.Background(), RecordDoseInput{
		CourseID:    course.ID,
		ScheduledAt: scheduled,
		Type:        RecordTypeTaken,
	})
	if err != nil {
		t.Fatalf("duplicate record returned error: %v", err)
	}
	if created {
		t.Fatal("duplicate submission should not create a new record")
	}
	if second.ID != first.ID {
		t.Fatalf("expected existing record %s, got %s", first.ID, second.ID)
	}

	records, err := s.ListDoseRecords(context.Background(), course.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("expected exactly 1 record after duplicate submission, got %d", len(records))
	}
}

func TestRecordBackfillRequiresOccurredAt(t *testing.T) {
	s := newTestService(testStart.Add(24 * time.Hour))
	course := mustCreateCourse(t, s)
	scheduled := testStart.Add(8 * time.Hour)

	_, _, err := s.RecordDose(context.Background(), RecordDoseInput{
		CourseID:    course.ID,
		ScheduledAt: scheduled,
		Type:        RecordTypeBackfill,
	})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput for backfill without occurred time, got %v", err)
	}

	occurred := scheduled.Add(30 * time.Minute)
	rec, created, err := s.RecordDose(context.Background(), RecordDoseInput{
		CourseID:    course.ID,
		ScheduledAt: scheduled,
		Type:        RecordTypeBackfill,
		OccurredAt:  occurred,
		Note:        "早上忘记登记",
	})
	if err != nil || !created {
		t.Fatalf("backfill: created=%v err=%v", created, err)
	}
	if !rec.OccurredAt.Equal(occurred) {
		t.Fatalf("backfill must keep real occurred time %v, got %v", occurred, rec.OccurredAt)
	}
	if rec.OccurredAt.Equal(rec.RecordedAt) {
		t.Fatal("backfill occurred time must not be disguised as registration time")
	}
}

func TestRecordDoseOutOfCourseRange(t *testing.T) {
	s := newTestService(testStart)
	course := mustCreateCourse(t, s)

	_, _, err := s.RecordDose(context.Background(), RecordDoseInput{
		CourseID:    course.ID,
		ScheduledAt: testEnd.Add(time.Hour),
		Type:        RecordTypeTaken,
	})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput, got %v", err)
	}
}

func TestPauseAndResumeCourse(t *testing.T) {
	s := newTestService(testStart)
	course := mustCreateCourse(t, s)

	paused, err := s.PauseCourse(context.Background(), course.ID)
	if err != nil {
		t.Fatal(err)
	}
	if paused.Status != CourseStatusPaused {
		t.Fatalf("expected paused, got %s", paused.Status)
	}

	_, _, err = s.RecordDose(context.Background(), RecordDoseInput{
		CourseID:    course.ID,
		ScheduledAt: testStart.Add(4 * time.Hour),
		Type:        RecordTypeTaken,
	})
	if !errors.Is(err, ErrInvalidStatusTransition) {
		t.Fatalf("expected ErrInvalidStatusTransition while paused, got %v", err)
	}

	resumed, err := s.ResumeCourse(context.Background(), course.ID)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Status != CourseStatusActive {
		t.Fatalf("expected active after resume, got %s", resumed.Status)
	}

	_, created, err := s.RecordDose(context.Background(), RecordDoseInput{
		CourseID:    course.ID,
		ScheduledAt: testStart.Add(4 * time.Hour),
		Type:        RecordTypeTaken,
	})
	if err != nil || !created {
		t.Fatalf("record after resume: created=%v err=%v", created, err)
	}
}

func TestCompleteCourseKeepsRecords(t *testing.T) {
	s := newTestService(testStart)
	course := mustCreateCourse(t, s)
	scheduled := testStart.Add(4 * time.Hour)

	if _, _, err := s.RecordDose(context.Background(), RecordDoseInput{
		CourseID:    course.ID,
		ScheduledAt: scheduled,
		Type:        RecordTypeTaken,
	}); err != nil {
		t.Fatal(err)
	}

	completed, err := s.CompleteCourse(context.Background(), course.ID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status != CourseStatusCompleted {
		t.Fatalf("expected completed, got %s", completed.Status)
	}

	_, _, err = s.RecordDose(context.Background(), RecordDoseInput{
		CourseID:    course.ID,
		ScheduledAt: testStart.Add(8 * time.Hour),
		Type:        RecordTypeTaken,
	})
	if !errors.Is(err, ErrInvalidStatusTransition) {
		t.Fatalf("expected ErrInvalidStatusTransition after complete, got %v", err)
	}

	records, err := s.ListDoseRecords(context.Background(), course.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].Type != RecordTypeTaken {
		t.Fatalf("completed course must keep recorded facts, got %+v", records)
	}
}

func TestCancelCourseRejectsNewRecords(t *testing.T) {
	s := newTestService(testStart)
	course := mustCreateCourse(t, s)

	if _, err := s.CancelCourse(context.Background(), course.ID); err != nil {
		t.Fatal(err)
	}

	_, _, err := s.RecordDose(context.Background(), RecordDoseInput{
		CourseID:    course.ID,
		ScheduledAt: testStart.Add(4 * time.Hour),
		Type:        RecordTypeTaken,
	})
	if !errors.Is(err, ErrInvalidStatusTransition) {
		t.Fatalf("expected ErrInvalidStatusTransition after cancel, got %v", err)
	}
}

func TestListQueriesStableOrder(t *testing.T) {
	s := newTestService(testStart)
	course := mustCreateCourse(t, s)

	times := []time.Time{
		testStart.Add(12 * time.Hour),
		testStart.Add(4 * time.Hour),
		testStart.Add(8 * time.Hour),
	}
	types := []RecordType{RecordTypeMissed, RecordTypeTaken, RecordTypeMissed}
	for i := range times {
		if _, _, err := s.RecordDose(context.Background(), RecordDoseInput{
			CourseID:    course.ID,
			ScheduledAt: times[i],
			Type:        types[i],
		}); err != nil {
			t.Fatal(err)
		}
	}

	records, err := s.ListDoseRecords(context.Background(), course.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 3 {
		t.Fatalf("expected 3 records, got %d", len(records))
	}
	for i := 1; i < len(records); i++ {
		if records[i].ScheduledAt.Before(records[i-1].ScheduledAt) {
			t.Fatalf("records not in stable ascending order: %v before %v",
				records[i].ScheduledAt, records[i-1].ScheduledAt)
		}
	}

	missed, err := s.ListMissedDoses(context.Background(), course.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(missed) != 2 {
		t.Fatalf("expected 2 missed records, got %d", len(missed))
	}

	courses := s.ListCoursesByPatient(context.Background(), "patient-001")
	if len(courses) != 1 || courses[0].ID != course.ID {
		t.Fatalf("expected to find course by patient, got %+v", courses)
	}

	if _, err := s.GetCourse(context.Background(), "course-999999"); !errors.Is(err, ErrCourseNotFound) {
		t.Fatalf("expected ErrCourseNotFound, got %v", err)
	}
}
