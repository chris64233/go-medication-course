package gomedicationcourse

import "errors"

// 领域错误。调用方使用 errors.Is 进行判断。
var (
	// ErrCourseNotFound 疗程不存在。
	ErrCourseNotFound = errors.New("course not found")
	// ErrInvalidArgument 参数非法。
	ErrInvalidArgument = errors.New("invalid argument")
	// ErrCoursePaused 疗程处于暂停状态，不能接受新的给药记录。
	ErrCoursePaused = errors.New("course is paused")
	// ErrCourseFinished 疗程已结束。
	ErrCourseFinished = errors.New("course is finished")
	// ErrNoMatchingDose 当前计划中没有处于记录窗口内的合法剂次。
	ErrNoMatchingDose = errors.New("no matching dose within record window")
	// ErrDoseAlreadyCompleted 目标剂次已被其他记录完成（并发争用失败）。
	ErrDoseAlreadyCompleted = errors.New("dose already completed")
	// ErrContentConflict 同一外部记录号重复提交但内容不一致。
	ErrContentConflict = errors.New("external record content conflict")
	// ErrAdministeredDuringPause 给药时间落在疗程的某个暂停区间内。
	ErrAdministeredDuringPause = errors.New("administration time falls within a pause interval")
	// ErrStalePlanVersion 迟到记录只属于旧计划版本，不能完成当前版本的剂次。
	ErrStalePlanVersion = errors.New("record belongs to a superseded plan version")
	// ErrOriginalRecordNotFound 更正记录引用的原给药记录不存在。
	ErrOriginalRecordNotFound = errors.New("original administration record not found")
	// ErrOriginalAlreadyReversed 原给药记录已被更正冲销。
	ErrOriginalAlreadyReversed = errors.New("original administration already reversed")
	// ErrDuplicateCorrection 该外部更正记录号已存在。
	ErrDuplicateCorrection = errors.New("correction record already exists")
	// ErrExternalRefExists 外部记录号已被使用（跨操作冲突）。
	ErrExternalRefExists = errors.New("external record ref already exists")
	// ErrCourseNotPaused 疗程当前未暂停，无法恢复。
	ErrCourseNotPaused = errors.New("course is not paused")
	// ErrOriginalReversed 原记录已被冲销，无法重复登记。
	ErrOriginalReversed = errors.New("original administration was reversed")
)
