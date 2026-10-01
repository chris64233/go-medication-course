// Package medication 提供用药疗程与服药记录的核心业务能力。
package medication

import "time"

// CourseStatus 表示疗程的生命周期状态。
type CourseStatus string

const (
	StatusActive    CourseStatus = "active"
	StatusPaused    CourseStatus = "paused"
	StatusCompleted CourseStatus = "completed"
	StatusCancelled CourseStatus = "cancelled"
)

// RecordKind 表示服药记录的类型。
type RecordKind string

const (
	// RecordTaken 按计划时间正常确认服药。
	RecordTaken RecordKind = "taken"
	// RecordMissed 计划时间已过，登记为漏服。
	RecordMissed RecordKind = "missed"
	// RecordBackfilled 事后补记，必须携带实际发生时间。
	RecordBackfilled RecordKind = "backfilled"
)

// Dose 描述单次剂量。
type Dose struct {
	Amount float64
	Unit   string
}

// Course 表示一段有时间要求的用药疗程。
type Course struct {
	ID              string
	PatientID       string
	Medication      string
	Dose            Dose
	FrequencyPerDay int
	StartTime       time.Time
	PlannedEndTime  time.Time
	Status          CourseStatus
	CreatedAt       time.Time
	// StatusChangedAt 记录最近一次状态变更时间（暂停/恢复/完成/取消）。
	StatusChangedAt time.Time
}

// DoseRecord 表示一次服药事实，登记后不可修改、不可删除。
type DoseRecord struct {
	ID            string
	CourseID      string
	ScheduledTime time.Time
	Kind          RecordKind
	// OccurredAt 是实际服药时间；正常服药时等于登记时间，补记时为过去的时间点。
	OccurredAt time.Time
	// RecordedAt 是记录在系统中的登记时间，用于区分补记与实时服药。
	RecordedAt time.Time
	Note       string
}

// Adherence 汇总一个疗程截至目前的服药依从情况。
type Adherence struct {
	CourseID      string
	Status        CourseStatus
	DueCount      int // 截至统计时间已到点的计划服药次数
	TakenCount    int
	MissedCount   int
	BackfillCount int
	PendingCount  int // 已到点但尚无记录的次数
}
