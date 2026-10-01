package medication

import (
	"errors"
	"fmt"
)

// 可通过 errors.Is 判断的哨兵错误，调用方无需解析文案。
var (
	ErrNotFound     = errors.New("记录不存在")
	ErrInvalidInput = errors.New("输入不合法")
	ErrInvalidState = errors.New("疗程状态不允许该操作")
)

// ValidationError 描述某个字段的校验失败，Message 面向调用方，可直接展示。
type ValidationError struct {
	Field   string
	Message string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("%s: %s", e.Field, e.Message)
}

func (e *ValidationError) Is(target error) bool { return target == ErrInvalidInput }

// StateError 描述因疗程状态导致的拒绝，Message 面向调用方。
type StateError struct {
	CourseID string
	Status   CourseStatus
	Message  string
}

func (e *StateError) Error() string {
	return fmt.Sprintf("疗程 %s 当前状态为 %s：%s", e.CourseID, e.Status, e.Message)
}

func (e *StateError) Is(target error) bool { return target == ErrInvalidState }

func invalidInput(field, format string, args ...any) error {
	return &ValidationError{Field: field, Message: fmt.Sprintf(format, args...)}
}
