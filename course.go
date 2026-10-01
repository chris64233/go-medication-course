package gomedicationcourse

import "time"

// CourseStatus 表示疗程的生命周期状态。
type CourseStatus string

const (
	CourseStatusActive    CourseStatus = "active"
	CourseStatusPaused    CourseStatus = "paused"
	CourseStatusCompleted CourseStatus = "completed"
	CourseStatusCancelled CourseStatus = "cancelled"
)

// Dosage 描述单次服药剂量。
type Dosage struct {
	Amount float64 `json:"amount"`
	Unit   string  `json:"unit"`
}

// Course 表示一段有时间要求的用药疗程。
type Course struct {
	ID              string       `json:"id"`
	PatientID       string       `json:"patient_id"`
	Medication      string       `json:"medication"`
	Dosage          Dosage       `json:"dosage"`
	FrequencyPerDay int          `json:"frequency_per_day"`
	StartAt         time.Time    `json:"start_at"`
	PlannedEndAt    time.Time    `json:"planned_end_at"`
	Status          CourseStatus `json:"status"`
	CreatedAt       time.Time    `json:"created_at"`
	UpdatedAt       time.Time    `json:"updated_at"`
}

// RecordType 表示服药记录的登记类型。
type RecordType string

const (
	// RecordTypeTaken 按计划时间正常服药。
	RecordTypeTaken RecordType = "taken"
	// RecordTypeMissed 计划时间漏服。
	RecordTypeMissed RecordType = "missed"
	// RecordTypeBackfill 事后补记，必须携带真实发生时间。
	RecordTypeBackfill RecordType = "backfill"
)

// DoseRecord 表示一条不可变的服药事实记录。
type DoseRecord struct {
	ID          string     `json:"id"`
	CourseID    string     `json:"course_id"`
	ScheduledAt time.Time  `json:"scheduled_at"`
	Type        RecordType `json:"type"`
	// OccurredAt 是事件真实发生时间；补记时必填，且不能伪装成登记当下。
	OccurredAt time.Time `json:"occurred_at"`
	// RecordedAt 是系统登记时间，用于追溯。
	RecordedAt time.Time `json:"recorded_at"`
	Note       string    `json:"note,omitempty"`
	// Seq 是全局单调序号，保证同一时间点的记录顺序稳定。
	Seq int64 `json:"seq"`
}
