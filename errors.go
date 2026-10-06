package gomedicationcourse

import "errors"

var (
	ErrCourseNotFound           = errors.New("course not found")
	ErrCourseNotActive          = errors.New("course is not active")
	ErrCoursePaused             = errors.New("course is paused")
	ErrCourseTerminated         = errors.New("course is completed or cancelled")
	ErrInvalidStatusTransition  = errors.New("invalid course status transition")
	ErrNoPlannedDose            = errors.New("no planned dose at the given time")
	ErrDoseAlreadyRecorded      = errors.New("dose already recorded for the planned point")
	ErrVersionConflict          = errors.New("prescription version conflict")
	ErrEffectiveBeforeConfirmed = errors.New("effective time is earlier than a confirmed dose fact")
	ErrInvalidAdjustment        = errors.New("invalid adjustment: dose and interval must be positive")
	ErrInvalidEffectiveAt       = errors.New("effective time is before course start")
	ErrRequestConflict          = errors.New("external request id already used with different content")
)
