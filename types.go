package gomedicationcourse

import "time"

// Frequency is the dosing interval of a prescription version. It is expressed
// as a fixed interval between planned dose time points; interval-based dosing
// keeps schedule expansion deterministic across prescription revisions.
type Frequency struct {
	Every time.Duration
}

// Dose is the prescribed amount taken at a single time point.
type Dose struct {
	// Amount is measured in Unit, e.g. 100 for 100 milligrams.
	Amount float64
	Unit   string
}

// PrescriptionVersion is one immutable version of a course prescription.
// Version numbers start at 1 and must strictly increase.
type PrescriptionVersion struct {
	Version     int
	Dose        Dose
	Frequency   Frequency
	EffectiveAt time.Time
	// AdjustedBy identifies the operator that submitted the revision. The
	// initial version records the course creator.
	AdjustedBy string
	Reason     string
	// SubmittedAt is when the revision was accepted by the system.
	SubmittedAt time.Time
	// RequestID carries the external idempotency key when the version was
	// created through an adjustment request; empty for the initial version.
	RequestID string
}

// CourseStatus describes the lifecycle state of a course.
type CourseStatus string

const (
	// StatusActive allows plan expansion, intake registration and adjustments.
	StatusActive CourseStatus = "active"
	// StatusPaused suppresses new plan time points between pause and resume;
	// no intake registration is accepted while paused.
	StatusPaused CourseStatus = "paused"
	// StatusCompleted is a terminal state.
	StatusCompleted CourseStatus = "completed"
	// StatusCancelled is a terminal state.
	StatusCancelled CourseStatus = "cancelled"
)

// PauseWindow records one closed-open [From, To) suspension interval. To is the
// zero time while the course is currently paused.
type PauseWindow struct {
	From time.Time
	To   time.Time
}

// Course is the aggregate root for a medication course.
type Course struct {
	ID         string
	Medication string
	Status     CourseStatus
	// StartAt anchors the schedule of version 1.
	StartAt time.Time
	// EndAt optionally bounds plan expansion; the zero time means open ended.
	EndAt time.Time
	// Versions are ordered, immutable prescription versions.
	Versions []PrescriptionVersion
	// Pauses records all suspension windows, including the open one.
	Pauses []PauseWindow
	// Intakes are confirmed intake facts; they are never rewritten.
	Intakes []Intake
	// adjustments maps external request id to produced version number.
	adjustments map[string]int
}

// Intake is a confirmed medication taking fact.
type Intake struct {
	// ScheduledAt is the planned time point the fact is registered against.
	ScheduledAt time.Time
	// TakenAt is when the dose was actually taken.
	TakenAt time.Time
	// Version is the prescription version that governed ScheduledAt. It is
	// frozen at registration time and never recomputed after adjustments.
	Version int
	// Dose is frozen at registration time.
	Dose Dose
	// Backfill marks facts registered after their scheduled time. Backfilled
	// facts are never disguised as a normal intake under a newer version.
	Backfill bool
	// RegisteredBy records the operator.
	RegisteredBy string
	// Reason may explain a backfill or other remark.
	Reason string
}

// PlannedPoint is one time point of the schedule expanded from the version
// that was effective at PlannedAt.
type PlannedPoint struct {
	PlannedAt time.Time
	Version   int
	Dose      Dose
	// Taken references the confirmed intake at this time point, if any. The
	// intake version and dose are presented as registered even when a later
	// adjustment changed the plan.
	Taken *Intake
}

// CreateCourseRequest opens a new course with prescription version 1.
type CreateCourseRequest struct {
	ID         string
	Medication string
	Dose       Dose
	Frequency  Frequency
	StartAt    time.Time
	EndAt      time.Time
	Operator   string
	Reason     string
}

// AdjustPrescriptionRequest submits a new prescription version.
type AdjustPrescriptionRequest struct {
	CourseID    string
	Dose        Dose
	Frequency   Frequency
	EffectiveAt time.Time
	Operator    string
	Reason      string
	SubmittedAt time.Time
	// RequestID is the external idempotency key and must be non-empty.
	RequestID string
	// ExpectedVersion, when non-zero, additionally requires the current
	// latest version to equal it before the adjustment is applied.
	ExpectedVersion int
}

// RegisterIntakeRequest records a medication taking fact.
type RegisterIntakeRequest struct {
	CourseID string
	// ScheduledAt must be a time point of the currently expanded plan.
	ScheduledAt time.Time
	TakenAt     time.Time
	// ExpectedVersion must equal the version governing ScheduledAt.
	ExpectedVersion int
	Operator        string
	Reason          string
}

// StatusEventRequest changes pause/resume/completion/cancellation state.
type StatusEventRequest struct {
	CourseID string
	At       time.Time
	Operator string
	Reason   string
	// ExpectedVersion enforces that the client observed the current latest
	// prescription version, so status transitions and registrations are
	// arbitrated against the same prescription version.
	ExpectedVersion int
}
