package gomedicationcourse

import (
	"fmt"
	"sort"
	"sync"
	"time"
)

// Service 提供疗程、处方调整与服药登记能力。
// 所有写操作在同一把互斥锁下完成校验与变更，
// 保证并发下以同一处方版本与疗程状态裁决，且失败不产生部分变更。
type Service struct {
	mu        sync.Mutex
	now       func() time.Time
	courses   map[string]*Course
	courseSeq int
	doseSeq   int
}

func NewService() *Service {
	return &Service{
		now:     time.Now,
		courses: make(map[string]*Course),
	}
}

// SetClock 注入时钟，仅用于测试。
func (s *Service) SetClock(now func() time.Time) { s.now = now }

// CreateCourse 创建疗程并生成初始处方版本（版本 1）。
func (s *Service) CreateCourse(patient string, startAt time.Time, doseMg float64, intervalHours int, author, reason string) (*Course, error) {
	if doseMg <= 0 || intervalHours <= 0 {
		return nil, ErrInvalidAdjustment
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.courseSeq++
	c := &Course{
		ID:       fmt.Sprintf("course-%d", s.courseSeq),
		Patient:  patient,
		StartAt:  startAt,
		Status:   CourseStatusActive,
		requests: make(map[string]*PrescriptionVersion),
	}
	c.versions = append(c.versions, &PrescriptionVersion{
		Version:       1,
		DoseMg:        doseMg,
		IntervalHours: intervalHours,
		Author:        author,
		Reason:        reason,
		SubmittedAt:   s.now(),
		EffectiveAt:   startAt,
	})
	s.courses[c.ID] = c
	return c, nil
}

// versionAt 返回在时刻 t 生效的处方版本。
func versionAt(c *Course, t time.Time) *PrescriptionVersion {
	var best *PrescriptionVersion
	for _, v := range c.versions {
		if !v.EffectiveAt.After(t) && (best == nil || v.Version > best.Version) {
			best = v
		}
	}
	return best
}

// plannedAt 判断 t 是否为一个计划服药时间点。
func plannedAt(v *PrescriptionVersion, t time.Time) bool {
	if v == nil || t.Before(v.EffectiveAt) {
		return false
	}
	interval := time.Duration(v.IntervalHours) * time.Hour
	return t.Sub(v.EffectiveAt)%interval == 0
}

// lastConfirmed 返回已确认服药事实中最晚的计划时间。
func lastConfirmed(c *Course) (time.Time, bool) {
	var max time.Time
	ok := false
	for _, d := range c.doses {
		if !ok || d.ScheduledAt.After(max) {
			max, ok = d.ScheduledAt, true
		}
	}
	return max, ok
}

func (s *Service) getCourse(id string) (*Course, error) {
	c, ok := s.courses[id]
	if !ok {
		return nil, ErrCourseNotFound
	}
	return c, nil
}

// SubmitAdjustment 提交新的处方版本。只影响生效时间之后未发生的计划点，
// 已登记的服药事实保持不变。按外部请求号幂等。
func (s *Service) SubmitAdjustment(courseID string, req AdjustmentRequest) (*PrescriptionVersion, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	c, err := s.getCourse(courseID)
	if err != nil {
		return nil, err
	}

	// 幂等：同号同内容返回原调整，同号不同内容返回冲突。
	if prev, ok := c.requests[req.ExternalRequestID]; ok {
		if prev.DoseMg == req.DoseMg &&
			prev.IntervalHours == req.IntervalHours &&
			prev.EffectiveAt.Equal(req.EffectiveAt) {
			return prev, nil
		}
		return nil, ErrRequestConflict
	}

	// 全部校验先于任何变更，失败时计划与服药记录不发生部分改变。
	switch c.Status {
	case CourseStatusActive:
	case CourseStatusPaused:
		return nil, ErrCoursePaused
	default:
		return nil, ErrCourseTerminated
	}
	if req.DoseMg <= 0 || req.IntervalHours <= 0 {
		return nil, ErrInvalidAdjustment
	}
	if req.EffectiveAt.Before(c.StartAt) {
		return nil, ErrInvalidEffectiveAt
	}
	if last, ok := lastConfirmed(c); ok && req.EffectiveAt.Before(last) {
		return nil, ErrEffectiveBeforeConfirmed
	}

	v := &PrescriptionVersion{
		Version:           c.versions[len(c.versions)-1].Version + 1,
		DoseMg:            req.DoseMg,
		IntervalHours:     req.IntervalHours,
		Author:            req.Author,
		Reason:            req.Reason,
		SubmittedAt:       s.now(),
		EffectiveAt:       req.EffectiveAt,
		ExternalRequestID: req.ExternalRequestID,
	}
	c.versions = append(c.versions, v)
	c.requests[req.ExternalRequestID] = v
	return v, nil
}

// RegisterDose 登记计划时间点 scheduledAt 的服药事实。
// expectedVersion 必须与该时间点当前生效的处方版本一致，
// 否则返回版本冲突。过去时间点的登记视为补记，仍保留原版本与原剂量。
func (s *Service) RegisterDose(courseID string, scheduledAt time.Time, expectedVersion int) (*DoseRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	c, err := s.getCourse(courseID)
	if err != nil {
		return nil, err
	}
	switch c.Status {
	case CourseStatusActive:
	case CourseStatusPaused:
		return nil, ErrCoursePaused
	default:
		return nil, ErrCourseTerminated
	}

	v := versionAt(c, scheduledAt)
	if !plannedAt(v, scheduledAt) {
		return nil, ErrNoPlannedDose
	}
	if expectedVersion != v.Version {
		return nil, ErrVersionConflict
	}
	for _, d := range c.doses {
		if d.ScheduledAt.Equal(scheduledAt) {
			return nil, ErrDoseAlreadyRecorded
		}
	}

	s.doseSeq++
	rec := &DoseRecord{
		ID:          s.doseSeq,
		CourseID:    c.ID,
		ScheduledAt: scheduledAt,
		RecordedAt:  s.now(),
		Version:     v.Version,
		DoseMg:      v.DoseMg,
		Backfill:    scheduledAt.Before(s.now()),
	}
	c.doses = append(c.doses, rec)
	return rec, nil
}

// PauseCourse 暂停疗程，暂停期间不能登记服药或提交调整。
func (s *Service) PauseCourse(courseID string) error {
	return s.transition(courseID, CourseStatusActive, CourseStatusPaused)
}

// ResumeCourse 恢复已暂停的疗程。
func (s *Service) ResumeCourse(courseID string) error {
	return s.transition(courseID, CourseStatusPaused, CourseStatusActive)
}

// CompleteCourse 完成疗程，之后不能再调整或登记。
func (s *Service) CompleteCourse(courseID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.getCourse(courseID)
	if err != nil {
		return err
	}
	if c.Status != CourseStatusActive && c.Status != CourseStatusPaused {
		return ErrInvalidStatusTransition
	}
	c.Status = CourseStatusCompleted
	return nil
}

// CancelCourse 取消疗程，之后不能再调整或登记。
func (s *Service) CancelCourse(courseID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.getCourse(courseID)
	if err != nil {
		return err
	}
	if c.Status == CourseStatusCompleted || c.Status == CourseStatusCancelled {
		return ErrInvalidStatusTransition
	}
	c.Status = CourseStatusCancelled
	return nil
}

func (s *Service) transition(courseID string, from, to CourseStatus) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.getCourse(courseID)
	if err != nil {
		return err
	}
	if c.Status != from {
		return ErrInvalidStatusTransition
	}
	c.Status = to
	return nil
}

// AdjustmentHistory 返回疗程的全部处方版本（含初始版本），按版本号升序。
func (s *Service) AdjustmentHistory(courseID string) ([]*PrescriptionVersion, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.getCourse(courseID)
	if err != nil {
		return nil, err
	}
	out := make([]*PrescriptionVersion, len(c.versions))
	copy(out, c.versions)
	return out, nil
}

// Plan 按时间展开 [from, to) 内的计划点，每个点标注当时生效的
// 处方版本与剂量；已登记的点附带不可变的服药记录。
func (s *Service) Plan(courseID string, from, to time.Time) ([]PlannedPoint, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.getCourse(courseID)
	if err != nil {
		return nil, err
	}

	bySlot := make(map[time.Time]*DoseRecord, len(c.doses))
	for _, d := range c.doses {
		bySlot[d.ScheduledAt] = d
	}

	vs := make([]*PrescriptionVersion, len(c.versions))
	copy(vs, c.versions)
	sort.Slice(vs, func(i, j int) bool {
		if vs[i].EffectiveAt.Equal(vs[j].EffectiveAt) {
			return vs[i].Version < vs[j].Version
		}
		return vs[i].EffectiveAt.Before(vs[j].EffectiveAt)
	})

	now := s.now()
	var points []PlannedPoint
	for i, v := range vs {
		segEnd := to
		if i+1 < len(vs) && vs[i+1].EffectiveAt.After(v.EffectiveAt) && vs[i+1].EffectiveAt.Before(segEnd) {
			segEnd = vs[i+1].EffectiveAt
		}
		if !segEnd.After(v.EffectiveAt) {
			continue
		}
		interval := time.Duration(v.IntervalHours) * time.Hour
		start := v.EffectiveAt
		if start.Before(from) {
			start = start.Add(((from.Sub(start)) / interval) * interval)
			if start.Before(from) {
				start = start.Add(interval)
			}
		}
		for t := start; t.Before(segEnd); t = t.Add(interval) {
			p := PlannedPoint{
				ScheduledAt: t,
				Version:     v.Version,
				DoseMg:      v.DoseMg,
				Status:      PointStatusPending,
			}
			if rec, ok := bySlot[t]; ok {
				p.Status = PointStatusTaken
				p.Record = rec
			} else if t.Before(now) {
				p.Status = PointStatusMissed
			}
			points = append(points, p)
		}
	}
	sort.Slice(points, func(i, j int) bool { return points[i].ScheduledAt.Before(points[j].ScheduledAt) })
	return points, nil
}
