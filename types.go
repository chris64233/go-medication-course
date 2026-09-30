package gomedicationcourse

import "time"

// CourseStatus 疗程状态。
type CourseStatus string

const (
	// CourseActive 进行中（可能存在已暂停的历史区间）。
	CourseActive CourseStatus = "active"
	// CoursePaused 当前处于暂停状态。
	CoursePaused CourseStatus = "paused"
	// CourseFinished 全部剂次进入终态（完成或错过）。
	CourseFinished CourseStatus = "finished"
)

// DoseStatus 剂次执行状态（从事件账本投影得到）。
type DoseStatus string

const (
	DosePending   DoseStatus = "pending"
	DoseCompleted DoseStatus = "completed"
	DoseMissed    DoseStatus = "missed"
)

// VersionReason 计划版本变更原因。
type VersionReason string

const (
	VersionCreated VersionReason = "created"
	VersionAdjust  VersionReason = "adjusted"
	VersionResume  VersionReason = "resumed"
)

// EventType 事件类型。事件只追加、不可修改，是账本事实的唯一来源。
type EventType string

const (
	EvCourseCreated  EventType = "course_created"
	EvCoursePaused   EventType = "course_paused"
	EvCourseResumed  EventType = "course_resumed"
	EvCourseAdjusted EventType = "course_adjusted"
	EvDoseAdministered EventType = "dose_administered"
	EvDoseReversed    EventType = "dose_reversed"
	EvDoseMissed      EventType = "dose_missed"
	EvCourseFinished  EventType = "course_finished"
)

// Course 疗程聚合。
type Course struct {
	ID             string       `json:"id"`
	Medication     string       `json:"medication"`
	Status         CourseStatus `json:"status"`
	TotalDoses     int          `json:"total_doses"`
	CurrentVersion int          `json:"current_version"`
	CreatedAt      time.Time    `json:"created_at"`
}

// Dose 计划剂次。剂次被某个版本创建后，其计划时间与窗口不可修改。
type Dose struct {
	ID           string       `json:"id"`
	CourseID     string       `json:"course_id"`
	Version      int          `json:"version"`
	Sequence     int          `json:"sequence"`
	ScheduledAt  time.Time    `json:"scheduled_at"`
	WindowStart  time.Time    `json:"window_start"`
	WindowEnd    time.Time    `json:"window_end"`
	// Superseded 为 true 时表示该剂次因计划新版本产生而被废弃，
	// 不再参与匹配与依从性统计；历史事实仍保留在账本中。
	Superseded   bool         `json:"superseded,omitempty"`
	// CarriedFrom 若剂次从上一版本平移保留，记录原剂次 ID。
	CarriedFrom  string       `json:"carried_from,omitempty"`
}

// PlanVersion 不可变的计划版本快照。
type PlanVersion struct {
	CourseID     string        `json:"course_id"`
	Version      int           `json:"version"`
	Reason       VersionReason `json:"reason"`
	StartAt      time.Time     `json:"start_at"`
	Interval     time.Duration `json:"interval_ns"`
	TotalDoses   int           `json:"total_doses"`
	EarlyWindow  time.Duration `json:"early_window_ns"`
	LateWindow   time.Duration `json:"late_window_ns"`
	EffectiveAt  time.Time     `json:"effective_at"`
	SupersededAt time.Time     `json:"superseded_at,omitempty"`
	Doses        []Dose        `json:"doses"`
}

// PauseInterval 暂停区间，ResumeAt 为零值表示当前仍在暂停。
type PauseInterval struct {
	PauseAt  time.Time `json:"pause_at"`
	ResumeAt time.Time `json:"resume_at,omitempty"`
}

// Event 账本事件，append-only。
type Event struct {
	Seq          int64          `json:"seq"`
	CourseID     string         `json:"course_id"`
	Type         EventType      `json:"type"`
	OccurredAt   time.Time      `json:"occurred_at"`
	DoseID       string         `json:"dose_id,omitempty"`
	RecordID     string         `json:"record_id,omitempty"`
	ExternalRef  string         `json:"external_ref,omitempty"`
	ContentHash  string         `json:"content_hash,omitempty"`
	AdministeredAt time.Time    `json:"administered_at,omitempty"`
	ToVersion    int            `json:"to_version,omitempty"`
	Detail       string         `json:"detail,omitempty"`
}

// AdministrationRecord 一次外部给药登记。
type AdministrationRecord struct {
	ID             string    `json:"id"`
	CourseID       string    `json:"course_id"`
	ExternalRef    string    `json:"external_ref"`
	AdministeredAt time.Time `json:"administered_at"`
	Note           string    `json:"note,omitempty"`
	ContentHash    string    `json:"content_hash"`
	DoseID         string    `json:"dose_id"`
	ReversedBy     string    `json:"reversed_by,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
}

// CorrectionRecord 一次错误给药的更正（冲销原记录，不删除历史）。
type CorrectionRecord struct {
	ID            string    `json:"id"`
	CourseID      string    `json:"course_id"`
	ExternalRef   string    `json:"external_ref"`
	OriginalRef   string    `json:"original_ref"`
	Reason        string    `json:"reason,omitempty"`
	ContentHash   string    `json:"content_hash"`
	ReversedDoseID string   `json:"reversed_dose_id"`
	CreatedAt     time.Time `json:"created_at"`
}

// CreateCourseInput 创建疗程参数。
type CreateCourseInput struct {
	ID          string
	Medication  string
	StartAt     time.Time
	Interval    time.Duration
	TotalDoses  int
	EarlyWindow time.Duration
	LateWindow  time.Duration
}

// AdjustCourseInput 调整疗程参数。调整永远产生新版本，
// 已经完成/错过的历史剂次原样保留，尚未开始的剂次重新排列。
type AdjustCourseInput struct {
	CourseID    string
	Interval    time.Duration
	TotalDoses  int
	FromTime    time.Time
	EarlyWindow time.Duration
	LateWindow  time.Duration
	Reason      string
}

// RegisterInput 给药登记参数。
type RegisterInput struct {
	CourseID       string
	ExternalRef    string
	AdministeredAt time.Time
	Note           string
}

// RegisterResult 给药登记结果。幂等重放时 IdempotentHit 为 true。
type RegisterResult struct {
	Record        AdministrationRecord
	Dose          Dose
	IdempotentHit bool
}

// CorrectionInput 更正参数。
type CorrectionInput struct {
	CourseID    string
	ExternalRef string
	OriginalRef string
	Reason      string
}

// CorrectionResult 更正结果。
type CorrectionResult struct {
	Correction CorrectionRecord
	Dose       Dose
	// Reopened 为 true 表示原剂次仍在窗口内，已重新开放；否则剂次标记为错过。
	Reopened bool
}

// AdherenceReport 依从性统计，基于事件账本重放计算，保证与账本一致。
type AdherenceReport struct {
	CourseID       string
	CurrentVersion int
	Total          int
	Completed      int
	Missed         int
	Pending        int
	Reversed       int
	OnTime         int
	Late           int
	Early          int
	// CompletionRate 已完成 / (已完成 + 已错过)。
	CompletionRate float64
	// AdherenceRate 已完成 / 当前版本计划剂次总数。
	AdherenceRate float64
}
