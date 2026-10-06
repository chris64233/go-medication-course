package gomedicationcourse

import "time"

// CourseStatus 表示疗程生命周期状态。
type CourseStatus string

const (
	CourseStatusActive    CourseStatus = "active"
	CourseStatusPaused    CourseStatus = "paused"
	CourseStatusCompleted CourseStatus = "completed"
	CourseStatusCancelled CourseStatus = "cancelled"
)

// PrescriptionVersion 是处方的一个版本。初始版本为 1，
// 每次成功的处方调整产生一个递增的新版本。
type PrescriptionVersion struct {
	Version           int
	DoseMg            float64
	IntervalHours     int
	Author            string
	Reason            string
	SubmittedAt       time.Time
	EffectiveAt       time.Time
	ExternalRequestID string
}

// DoseRecord 是已经登记的服药事实，一经创建不可修改。
// Version 与 DoseMg 永远保留计划时间点当时生效的处方版本与剂量，
// 即使是事后补记也不会被新版本重写。
type DoseRecord struct {
	ID          int
	CourseID    string
	ScheduledAt time.Time
	RecordedAt  time.Time
	Version     int
	DoseMg      float64
	Backfill    bool
}

// Course 聚合疗程的处方版本、服药事实与幂等请求记录。
type Course struct {
	ID       string
	Patient  string
	StartAt  time.Time
	Status   CourseStatus
	versions []*PrescriptionVersion
	doses    []*DoseRecord
	requests map[string]*PrescriptionVersion
}

// AdjustmentRequest 是提交处方调整的入参。
type AdjustmentRequest struct {
	ExternalRequestID string
	DoseMg            float64
	IntervalHours     int
	EffectiveAt       time.Time
	Author            string
	Reason            string
}

// PointStatus 是计划时间点在展开查询中的状态。
type PointStatus string

const (
	PointStatusPending PointStatus = "pending"
	PointStatusTaken   PointStatus = "taken"
	PointStatusMissed  PointStatus = "missed"
)

// PlannedPoint 是按时间展开的计划点，明确标注该点采用的处方版本。
type PlannedPoint struct {
	ScheduledAt time.Time
	Version     int
	DoseMg      float64
	Status      PointStatus
	Record      *DoseRecord
}
