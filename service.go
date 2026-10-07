package gomedicationcourse

import (
	"sort"
	"sync"
	"time"
)

// Store is the persistence boundary. Implementations must be safe for
// concurrent access; MemoryStore is the in-memory reference implementation.
type Store interface {
	Get(id string) (*Course, bool)
	Put(c *Course)
}

// MemoryStore keeps courses in memory. Every service operation is applied as a
// single in-memory transaction: a failed request never leaves partial state.
type MemoryStore struct {
	mu      sync.Mutex
	courses map[string]*Course
}

// NewMemoryStore creates an empty MemoryStore.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{courses: map[string]*Course{}}
}

// Get returns a defensive copy of the course.
func (s *MemoryStore) Get(id string) (*Course, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.courses[id]
	if !ok {
		return nil, false
	}
	return cloneCourse(c), true
}

// Put stores a defensive copy of the course.
func (s *MemoryStore) Put(c *Course) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.courses[c.ID] = cloneCourse(c)
}

// Service is the application service for medication courses.
type Service struct {
	store Store
	now   func() time.Time
	mu    sync.Mutex
}

// NewService builds a Service over store. A nil store creates a MemoryStore.
func NewService(store Store) *Service {
	if store == nil {
		store = NewMemoryStore()
	}
	return &Service{store: store, now: time.Now}
}

// WithClock overrides the time source, mainly for deterministic tests. It must
// not be called concurrently with in-flight requests.
func (s *Service) WithClock(now func() time.Time) *Service {
	s.now = now
	return s
}

func (s *Service) defaultTime(t time.Time) time.Time {
	if t.IsZero() {
		return s.now()
	}
	return t
}

func validateDose(d Dose) error {
	if d.Amount <= 0 {
		return NewConflict(ConflictInvalid, "dose amount must be positive")
	}
	if d.Unit == "" {
		return NewConflict(ConflictInvalid, "dose unit is required")
	}
	return nil
}

// CreateCourse opens a course with prescription version 1 effective at StartAt.
func (s *Service) CreateCourse(req CreateCourseRequest) (*Course, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if req.ID == "" {
		return nil, NewConflict(ConflictInvalid, "course id is required")
	}
	if _, ok := s.store.Get(req.ID); ok {
		return nil, NewConflict(ConflictDuplicate, "course %q already exists", req.ID)
	}
	if err := validateDose(req.Dose); err != nil {
		return nil, err
	}
	if req.Frequency.Every <= 0 {
		return nil, NewConflict(ConflictInvalid, "frequency interval must be positive")
	}
	if req.StartAt.IsZero() {
		return nil, NewConflict(ConflictInvalid, "start time is required")
	}
	if !req.EndAt.IsZero() && !req.EndAt.After(req.StartAt) {
		return nil, NewConflict(ConflictInvalid, "end time must be after start time")
	}

	c := &Course{
		ID:         req.ID,
		Medication: req.Medication,
		Status:     StatusActive,
		StartAt:    req.StartAt,
		EndAt:      req.EndAt,
		Versions: []PrescriptionVersion{{
			Version:     1,
			Dose:        req.Dose,
			Frequency:   req.Frequency,
			EffectiveAt: req.StartAt,
			AdjustedBy:  req.Operator,
			Reason:      req.Reason,
			SubmittedAt: req.StartAt,
		}},
		adjustments: map[string]int{},
	}
	s.store.Put(c)
	return cloneCourse(c), nil
}

// GetCourse returns a snapshot of the course.
func (s *Service) GetCourse(id string) (*Course, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.store.Get(id)
	if !ok {
		return nil, NewConflict(ConflictInvalid, "course %q not found", id)
	}
	return c, nil
}

// AdjustPrescription commits a new prescription version. Revisions only affect
// plan time points at or after EffectiveAt; confirmed intake facts are never
// recomputed. The request is idempotent on RequestID: the same id with the same
// dose, frequency and effective time returns the original adjustment, while a
// different payload conflicts.
func (s *Service) AdjustPrescription(req AdjustPrescriptionRequest) (*PrescriptionVersion, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	c, ok := s.store.Get(req.CourseID)
	if !ok {
		return nil, NewConflict(ConflictInvalid, "course %q not found", req.CourseID)
	}
	if req.RequestID == "" {
		return nil, NewConflict(ConflictInvalid, "request id is required for adjustments")
	}
	if err := validateDose(req.Dose); err != nil {
		return nil, err
	}
	if req.Frequency.Every <= 0 {
		return nil, NewConflict(ConflictInvalid, "frequency interval must be positive")
	}
	if req.EffectiveAt.IsZero() {
		return nil, NewConflict(ConflictInvalid, "effective time is required")
	}
	if c.Status == StatusCompleted || c.Status == StatusCancelled {
		return nil, NewConflict(ConflictStatus, "course %q is %s and cannot be adjusted", c.ID, c.Status)
	}
	latest := c.Versions[len(c.Versions)-1]
	if req.ExpectedVersion != 0 && req.ExpectedVersion != latest.Version {
		return nil, NewConflict(ConflictVersion,
			"expected version %d but current version is %d", req.ExpectedVersion, latest.Version)
	}

	// A committed request id always resolves deterministically regardless of
	// the rest of the payload.
	if ver, exists := c.adjustments[req.RequestID]; exists {
		committed := findVersion(c.Versions, ver)
		if committed.Dose == req.Dose && committed.Frequency.Every == req.Frequency.Every &&
			committed.EffectiveAt.Equal(req.EffectiveAt) {
			v := committed
			return &v, nil
		}
		return nil, NewConflict(ConflictIdempotency,
			"request id %q already committed with different dose, frequency or effective time",
			req.RequestID)
	}

	// The effective time must be strictly later than every confirmed intake
	// fact; an effective time equal to a fact is rejected because that time
	// point already belongs to history and cannot change owner.
	var latestFact time.Time
	for _, in := range c.Intakes {
		if in.ScheduledAt.After(latestFact) {
			latestFact = in.ScheduledAt
		}
		if in.TakenAt.After(latestFact) {
			latestFact = in.TakenAt
		}
	}
	if !latestFact.IsZero() && !req.EffectiveAt.After(latestFact) {
		return nil, NewConflict(ConflictEffectiveTime,
			"effective time %s must be later than confirmed intake fact %s",
			req.EffectiveAt.Format(time.RFC3339), latestFact.Format(time.RFC3339))
	}
	if !req.EffectiveAt.After(latest.EffectiveAt) {
		return nil, NewConflict(ConflictEffectiveTime,
			"effective time %s must be later than current version effective time %s",
			req.EffectiveAt.Format(time.RFC3339), latest.EffectiveAt.Format(time.RFC3339))
	}

	nv := PrescriptionVersion{
		Version:     latest.Version + 1,
		Dose:        req.Dose,
		Frequency:   req.Frequency,
		EffectiveAt: req.EffectiveAt,
		AdjustedBy:  req.Operator,
		Reason:      req.Reason,
		SubmittedAt: s.defaultTime(req.SubmittedAt),
		RequestID:   req.RequestID,
	}
	c.Versions = append(c.Versions, nv)
	c.adjustments[req.RequestID] = nv.Version
	s.store.Put(c)
	v := nv
	return &v, nil
}

// AdjustmentHistory returns all prescription versions in order. Version 1 is
// the initial prescription; later entries are adjustments.
func (s *Service) AdjustmentHistory(courseID string) ([]PrescriptionVersion, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.store.Get(courseID)
	if !ok {
		return nil, NewConflict(ConflictInvalid, "course %q not found", courseID)
	}
	out := make([]PrescriptionVersion, len(c.Versions))
	copy(out, c.Versions)
	return out, nil
}

// RegisterIntake records an intake fact against a planned time point.
// ExpectedVersion must be the version governing ScheduledAt in the current
// plan; a stale version (for example to log a future dose under an older
// prescription) returns ConflictVersion. Facts registered after their
// scheduled time are marked Backfill and keep the version that governed the
// scheduled time; they are never disguised as a normal intake under a newer
// version.
func (s *Service) RegisterIntake(req RegisterIntakeRequest) (*Intake, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	c, ok := s.store.Get(req.CourseID)
	if !ok {
		return nil, NewConflict(ConflictInvalid, "course %q not found", req.CourseID)
	}
	if c.Status != StatusActive {
		return nil, NewConflict(ConflictStatus,
			"course %q is %s; intake registration requires active state", c.ID, c.Status)
	}
	if req.ScheduledAt.IsZero() {
		return nil, NewConflict(ConflictInvalid, "scheduled time is required")
	}
	if req.ExpectedVersion <= 0 {
		return nil, NewConflict(ConflictInvalid, "expected prescription version is required")
	}

	governing, err := versionAt(c, req.ScheduledAt)
	if err != nil {
		return nil, err
	}
	if governing.Version != req.ExpectedVersion {
		return nil, NewConflict(ConflictVersion,
			"time %s is governed by version %d, not %d",
			req.ScheduledAt.Format(time.RFC3339), governing.Version, req.ExpectedVersion)
	}
	if !isPlannedPoint(c, governing, req.ScheduledAt) {
		return nil, NewConflict(ConflictInvalid, "time %s is not a planned dose time point",
			req.ScheduledAt.Format(time.RFC3339))
	}
	if inPausedWindow(c, req.ScheduledAt) {
		return nil, NewConflict(ConflictInvalid, "time %s falls inside a pause window",
			req.ScheduledAt.Format(time.RFC3339))
	}
	for _, in := range c.Intakes {
		if in.ScheduledAt.Equal(req.ScheduledAt) {
			return nil, NewConflict(ConflictDuplicate, "intake at %s already registered",
				req.ScheduledAt.Format(time.RFC3339))
		}
	}

	takenAt := s.defaultTime(req.TakenAt)
	in := Intake{
		ScheduledAt:  req.ScheduledAt,
		TakenAt:      takenAt,
		Version:      governing.Version,
		Dose:         governing.Dose,
		Backfill:     takenAt.After(req.ScheduledAt),
		RegisteredBy: req.Operator,
		Reason:       req.Reason,
	}
	c.Intakes = append(c.Intakes, in)
	sort.SliceStable(c.Intakes, func(i, j int) bool {
		return c.Intakes[i].ScheduledAt.Before(c.Intakes[j].ScheduledAt)
	})
	s.store.Put(c)
	out := in
	return &out, nil
}

// Pause moves an active course into the paused state and opens a pause window.
func (s *Service) Pause(req StatusEventRequest) error {
	return s.transition(req, StatusPaused, "pause", func(c *Course, at time.Time) error {
		if c.Status != StatusActive {
			return NewConflict(ConflictStatus, "cannot pause course in state %s", c.Status)
		}
		c.Pauses = append(c.Pauses, PauseWindow{From: at})
		return nil
	})
}

// Resume reopens a paused course and closes its open pause window.
func (s *Service) Resume(req StatusEventRequest) error {
	return s.transition(req, StatusActive, "resume", func(c *Course, at time.Time) error {
		if c.Status != StatusPaused {
			return NewConflict(ConflictStatus, "cannot resume course in state %s", c.Status)
		}
		for i := range c.Pauses {
			if c.Pauses[i].To.IsZero() {
				c.Pauses[i].To = at
				return nil
			}
		}
		return NewConflict(ConflictStatus, "course %q has no open pause window", c.ID)
	})
}

// Complete terminates the course successfully.
func (s *Service) Complete(req StatusEventRequest) error {
	return s.transition(req, StatusCompleted, "complete", func(c *Course, at time.Time) error {
		if c.Status == StatusCompleted || c.Status == StatusCancelled {
			return NewConflict(ConflictStatus, "cannot complete course in terminal state %s", c.Status)
		}
		for i := range c.Pauses {
			if c.Pauses[i].To.IsZero() {
				c.Pauses[i].To = at
			}
		}
		return nil
	})
}

// Cancel terminates the course without completion.
func (s *Service) Cancel(req StatusEventRequest) error {
	return s.transition(req, StatusCancelled, "cancel", func(c *Course, at time.Time) error {
		if c.Status == StatusCompleted || c.Status == StatusCancelled {
			return NewConflict(ConflictStatus, "cannot cancel course in terminal state %s", c.Status)
		}
		for i := range c.Pauses {
			if c.Pauses[i].To.IsZero() {
				c.Pauses[i].To = at
			}
		}
		return nil
	})
}

func (s *Service) transition(req StatusEventRequest, target CourseStatus, op string,
	apply func(c *Course, at time.Time) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	c, ok := s.store.Get(req.CourseID)
	if !ok {
		return NewConflict(ConflictInvalid, "course %q not found", req.CourseID)
	}
	latest := c.Versions[len(c.Versions)-1]
	if req.ExpectedVersion != 0 && req.ExpectedVersion != latest.Version {
		return NewConflict(ConflictVersion,
			"expected version %d but current version is %d", req.ExpectedVersion, latest.Version)
	}
	at := s.defaultTime(req.At)
	if err := apply(c, at); err != nil {
		return err
	}
	c.Status = target
	s.store.Put(c)
	return nil
}
