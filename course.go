package gomedicationcourse

import (
	"errors"
	"time"
)

// CourseStatus 描述疗程当前所处的状态。
type CourseStatus string

const (
	StatusActive    CourseStatus = "active"
	StatusPaused    CourseStatus = "paused"
	StatusCompleted CourseStatus = "completed"
	StatusCancelled CourseStatus = "cancelled"
)

// 领域错误：调用方可用 errors.Is 判定。
var (
	// ErrCourseClosed 疗程已完成或取消，拒绝任何变更操作。
	ErrCourseClosed = errors.New("course is completed or cancelled")
	// ErrCoursePaused 疗程处于暂停状态，不能登记或调整。
	ErrCoursePaused = errors.New("course is paused")
	// ErrInvalidStatus 当前疗程状态不允许该状态迁移。
	ErrInvalidStatus = errors.New("invalid course status for this operation")
	// ErrVersionConflict 携带的处方版本与计划点当前版本不一致。
	ErrVersionConflict = errors.New("prescription version conflict")
	// ErrNoSuchPlan 计划时间点不存在（例如暂停区间内的补记）。
	ErrNoSuchPlan = errors.New("no planned dose at the given time")
	// ErrAlreadyRegistered 该计划点已有确认的服药事实。
	ErrAlreadyRegistered = errors.New("planned dose is already registered")
	// ErrEffectiveInPast 生效时间早于提交时刻。
	ErrEffectiveInPast = errors.New("effective time is in the past")
	// ErrEffectiveBeforeIntake 生效时间早于已经确认的服药事实。
	ErrEffectiveBeforeIntake = errors.New("effective time must not be earlier than confirmed intake")
	// ErrInvalidArgument 参数不合法。
	ErrInvalidArgument = errors.New("invalid argument")
	// ErrRequestConflict 同一外部请求号但请求内容不一致。
	ErrRequestConflict = errors.New("idempotency request content conflict")
)

// Dosage 是单次服药剂量的快照值。
type Dosage struct {
	Amount float64
	Unit   string
}

// Frequency 描述服药频次，以相邻两次计划服药的间隔表示。
type Frequency struct {
	Interval time.Duration
}

// PrescriptionVersion 是处方的一个版本。版本号从 1 开始严格递增。
type PrescriptionVersion struct {
	Version     int
	Dosage      Dosage
	Frequency   Frequency
	EffectiveAt time.Time
	SubmittedAt time.Time
	Adjuster    string
	Reason      string
}

// PointStatus 是计划时间点的状态。
type PointStatus string

const (
	PointPending    PointStatus = "pending"
	PointRegistered PointStatus = "registered"
	// PointVoided 表示疗程完成/取消时尚未服药的计划点，仅保留用于审计。
	PointVoided PointStatus = "voided"
)

// PlannedPoint 是一个已经展开的计划服药时间点，携带当时生效的版本快照。
type PlannedPoint struct {
	ScheduledAt time.Time
	Version     int
	Dosage      Dosage
	Status      PointStatus
}

// IntakeRecord 是一次已经确认的服药事实，一经写入不可重算或覆盖。
type IntakeRecord struct {
	ScheduledAt  time.Time
	TakenAt      time.Time
	RegisteredAt time.Time
	Version      int
	Dosage       Dosage
	// Backfilled 为 true 表示补记（计划时间点过后才登记），
	// 补记保留历史版本与剂量，不会伪装成新版本的正常服药。
	Backfilled bool
}

// ScheduleEntry 是按时间展开的计划查询结果中的一行。
type ScheduleEntry struct {
	ScheduledAt time.Time
	Version     int
	Dosage      Dosage
	Status      PointStatus
	// Intake 非空时指向该时间点登记的服药事实快照。
	Intake *IntakeRecord
}

// AdjustCommand 是提交处方调整版本的命令。
type AdjustCommand struct {
	CourseID    string
	Dosage      Dosage
	Frequency   Frequency
	EffectiveAt time.Time
	Adjuster    string
	Reason      string
	// RequestID 是外部请求号，用于幂等处理。
	RequestID string
}

// RegisterCommand 是登记一次服药的命令。
type RegisterCommand struct {
	CourseID string
	// PlannedAt 必须精确命中一个计划时间点。
	PlannedAt time.Time
	TakenAt   time.Time
	// ExpectedVersion 是客户端看到的处方版本，必须与计划点版本一致。
	ExpectedVersion int
	Registrar       string
	RequestID       string
}

// Course 是一个服药疗程的全部状态。Service 通过互斥锁保护并发访问。
type Course struct {
	ID        string
	Status    CourseStatus
	Versions  []PrescriptionVersion
	Points    []PlannedPoint
	Intakes   []IntakeRecord
	Pauses    []PauseRecord
	CreatedAt time.Time
	CreatedBy string
	// closedAt 是完成/取消时刻；之后的计划点不可再登记。
	closedAt time.Time

	// cursor 是下一个待展开计划点的时间。
	cursor time.Time
	// requests 记录外部请求号 -> 请求内容指纹与结果类型。
	requests map[string]idempotentRecord
}

// PauseRecord 记录一次暂停/恢复区间。
type PauseRecord struct {
	PausedAt  time.Time
	PausedBy  string
	Reason    string
	ResumedAt time.Time
	ResumedBy string
}

type idempotentRecord struct {
	kind        string
	fingerprint string
	// adjustVersion / intakeKey 指向首次成功请求的结果。
	adjustVersion int
}
