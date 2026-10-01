package gomedicationcourse

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// CreateCourseInput 是创建疗程的入参。
type CreateCourseInput struct {
	PatientID       string
	Medication      string
	Dosage          Dosage
	FrequencyPerDay int
	StartAt         time.Time
	PlannedEndAt    time.Time
}

// RecordDoseInput 是登记服药记录的入参。
type RecordDoseInput struct {
	CourseID    string
	ScheduledAt time.Time
	Type        RecordType
	// OccurredAt 仅在补记（RecordTypeBackfill）时必填，表示真实发生时间。
	OccurredAt time.Time
	Note       string
}

// Service 提供用药疗程与服药记录的核心业务能力。
// 当前实现使用内存存储，通过注入时钟保证可测试性。
type Service struct {
	mu      sync.Mutex
	now     func() time.Time
	seq     int64
	courses map[string]*Course
	records map[string]*DoseRecord
	// doseIndex 以 "courseID|scheduledAt" 为键，保证同一疗程同一计划时间只确认一次。
	doseIndex map[string]string
}

// NewService 创建一个服务实例；now 为 nil 时使用系统时间。
func NewService(now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{
		now:       now,
		courses:   make(map[string]*Course),
		records:   make(map[string]*DoseRecord),
		doseIndex: make(map[string]string),
	}
}

func (s *Service) nextID(prefix string) string {
	s.seq++
	return fmt.Sprintf("%s-%06d", prefix, s.seq)
}

// CreateCourse 创建疗程，拒绝缺失或明显不合理的剂量与时间信息。
func (s *Service) CreateCourse(_ context.Context, in CreateCourseInput) (*Course, error) {
	if strings.TrimSpace(in.PatientID) == "" {
		return nil, fmt.Errorf("%w: 患者 ID 不能为空", ErrInvalidInput)
	}
	if strings.TrimSpace(in.Medication) == "" {
		return nil, fmt.Errorf("%w: 药品名称不能为空", ErrInvalidInput)
	}
	if in.Dosage.Amount <= 0 {
		return nil, fmt.Errorf("%w: 剂量必须大于 0，当前为 %v", ErrInvalidInput, in.Dosage.Amount)
	}
	if strings.TrimSpace(in.Dosage.Unit) == "" {
		return nil, fmt.Errorf("%w: 剂量单位不能为空", ErrInvalidInput)
	}
	if in.FrequencyPerDay <= 0 {
		return nil, fmt.Errorf("%w: 每日频次必须大于 0，当前为 %d", ErrInvalidInput, in.FrequencyPerDay)
	}
	if in.StartAt.IsZero() {
		return nil, fmt.Errorf("%w: 开始时间不能为空", ErrInvalidInput)
	}
	if in.PlannedEndAt.IsZero() {
		return nil, fmt.Errorf("%w: 计划结束时间不能为空", ErrInvalidInput)
	}
	if !in.PlannedEndAt.After(in.StartAt) {
		return nil, fmt.Errorf("%w: 计划结束时间必须晚于开始时间", ErrInvalidInput)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now()
	course := &Course{
		ID:              s.nextID("course"),
		PatientID:       in.PatientID,
		Medication:      in.Medication,
		Dosage:          in.Dosage,
		FrequencyPerDay: in.FrequencyPerDay,
		StartAt:         in.StartAt,
		PlannedEndAt:    in.PlannedEndAt,
		Status:          CourseStatusActive,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	s.courses[course.ID] = course
	cp := *course
	return &cp, nil
}

// RecordDose 登记服药、漏服或补记。
// 同一疗程同一计划时间重复提交时，返回已有记录且 created 为 false，不会重复计数。
func (s *Service) RecordDose(_ context.Context, in RecordDoseInput) (record *DoseRecord, created bool, err error) {
	if strings.TrimSpace(in.CourseID) == "" {
		return nil, false, fmt.Errorf("%w: 疗程 ID 不能为空", ErrInvalidInput)
	}
	if in.ScheduledAt.IsZero() {
		return nil, false, fmt.Errorf("%w: 计划时间不能为空", ErrInvalidInput)
	}
	switch in.Type {
	case RecordTypeTaken, RecordTypeMissed:
	case RecordTypeBackfill:
		if in.OccurredAt.IsZero() {
			return nil, false, fmt.Errorf("%w: 补记必须说明真实发生时间", ErrInvalidInput)
		}
	default:
		return nil, false, fmt.Errorf("%w: 未知的记录类型 %q", ErrInvalidInput, in.Type)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	course, ok := s.courses[in.CourseID]
	if !ok {
		return nil, false, fmt.Errorf("%w: %s", ErrCourseNotFound, in.CourseID)
	}

	now := s.now()
	key := doseKey(in.CourseID, in.ScheduledAt)
	if existingID, dup := s.doseIndex[key]; dup {
		existing := *s.records[existingID]
		return &existing, false, nil
	}

	switch course.Status {
	case CourseStatusActive:
	case CourseStatusPaused:
		return nil, false, fmt.Errorf("%w: 疗程已暂停，请先恢复后再登记", ErrInvalidStatusTransition)
	case CourseStatusCompleted:
		return nil, false, fmt.Errorf("%w: 疗程已完成，不能继续登记", ErrInvalidStatusTransition)
	case CourseStatusCancelled:
		return nil, false, fmt.Errorf("%w: 疗程已取消，不能登记为正常服药", ErrInvalidStatusTransition)
	}

	if in.ScheduledAt.Before(course.StartAt) || in.ScheduledAt.After(course.PlannedEndAt) {
		return nil, false, fmt.Errorf("%w: 计划时间不在疗程范围内（%s ~ %s）",
			ErrInvalidInput,
			course.StartAt.Format(time.RFC3339),
			course.PlannedEndAt.Format(time.RFC3339))
	}

	occurredAt := in.OccurredAt
	if in.Type == RecordTypeBackfill {
		if occurredAt.After(now) {
			return nil, false, fmt.Errorf("%w: 补记的发生时间不能晚于当前时间", ErrInvalidInput)
		}
	} else {
		occurredAt = now
	}

	record = &DoseRecord{
		ID:          s.nextID("dose"),
		CourseID:    in.CourseID,
		ScheduledAt: in.ScheduledAt,
		Type:        in.Type,
		OccurredAt:  occurredAt,
		RecordedAt:  now,
		Note:        in.Note,
		Seq:         s.seq,
	}
	s.records[record.ID] = record
	s.doseIndex[key] = record.ID
	cp := *record
	return &cp, true, nil
}

// PauseCourse 暂停疗程，暂停期间不能登记服药。
func (s *Service) PauseCourse(_ context.Context, courseID string) (*Course, error) {
	return s.transition(courseID, CourseStatusPaused, CourseStatusActive)
}

// ResumeCourse 恢复已暂停的疗程。
func (s *Service) ResumeCourse(_ context.Context, courseID string) (*Course, error) {
	return s.transition(courseID, CourseStatusActive, CourseStatusPaused)
}

// CompleteCourse 完成疗程，已记录的服药事实保留。
func (s *Service) CompleteCourse(_ context.Context, courseID string) (*Course, error) {
	return s.transition(courseID, CourseStatusCompleted, CourseStatusActive, CourseStatusPaused)
}

// CancelCourse 取消疗程，取消后不能继续登记为正常服药。
func (s *Service) CancelCourse(_ context.Context, courseID string) (*Course, error) {
	return s.transition(courseID, CourseStatusCancelled, CourseStatusActive, CourseStatusPaused)
}

func (s *Service) transition(courseID string, to CourseStatus, from ...CourseStatus) (*Course, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	course, ok := s.courses[courseID]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrCourseNotFound, courseID)
	}
	allowed := false
	for _, st := range from {
		if course.Status == st {
			allowed = true
			break
		}
	}
	if !allowed {
		return nil, fmt.Errorf("%w: 当前状态为 %s，不能变更为 %s", ErrInvalidStatusTransition, course.Status, to)
	}
	course.Status = to
	course.UpdatedAt = s.now()
	cp := *course
	return &cp, nil
}

// GetCourse 按 ID 查询疗程。
func (s *Service) GetCourse(_ context.Context, courseID string) (*Course, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	course, ok := s.courses[courseID]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrCourseNotFound, courseID)
	}
	cp := *course
	return &cp, nil
}

// ListCoursesByPatient 查询患者的全部疗程，按创建顺序返回。
func (s *Service) ListCoursesByPatient(_ context.Context, patientID string) []*Course {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*Course
	for _, c := range s.courses {
		if c.PatientID == patientID {
			cp := *c
			out = append(out, &cp)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// ListDoseRecords 查询疗程的全部服药记录，按计划时间升序、同时间点按登记序号排序，顺序稳定。
func (s *Service) ListDoseRecords(_ context.Context, courseID string) ([]*DoseRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.courses[courseID]; !ok {
		return nil, fmt.Errorf("%w: %s", ErrCourseNotFound, courseID)
	}
	var out []*DoseRecord
	for _, r := range s.records {
		if r.CourseID == courseID {
			cp := *r
			out = append(out, &cp)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ScheduledAt.Equal(out[j].ScheduledAt) {
			return out[i].Seq < out[j].Seq
		}
		return out[i].ScheduledAt.Before(out[j].ScheduledAt)
	})
	return out, nil
}

// ListMissedDoses 查询疗程的漏服记录，顺序与 ListDoseRecords 一致。
func (s *Service) ListMissedDoses(_ context.Context, courseID string) ([]*DoseRecord, error) {
	records, err := s.ListDoseRecords(context.Background(), courseID)
	if err != nil {
		return nil, err
	}
	var out []*DoseRecord
	for _, r := range records {
		if r.Type == RecordTypeMissed {
			out = append(out, r)
		}
	}
	return out, nil
}

func doseKey(courseID string, scheduledAt time.Time) string {
	return courseID + "|" + scheduledAt.UTC().Format(time.RFC3339Nano)
}
