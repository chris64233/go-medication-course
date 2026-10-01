package gomedicationcourse

import "errors"

var (
	// ErrInvalidInput 表示请求参数缺失或明显不合理。
	ErrInvalidInput = errors.New("参数无效")
	// ErrCourseNotFound 表示指定的疗程不存在。
	ErrCourseNotFound = errors.New("疗程不存在")
	// ErrInvalidStatusTransition 表示当前疗程状态不允许执行该操作。
	ErrInvalidStatusTransition = errors.New("疗程状态不允许该操作")
)
