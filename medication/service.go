package medication

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"
)

// maxFrequencyPerDay 限定每日服药频次上限，超出视为明显不合理。
const maxFrequencyPerDay = 12

// CreateCourseInput 是创建疗程的入参。
type CreateCourseInput struct {
	PatientID       string
	Medication      string
	Dose            Dose
	FrequencyPerDay int
	StartTime       time.Time
	PlannedEndTime  time.Time
}

// Service 提供用药疗程与服药记录能力，当前为内存实现， goroutine 安全。
type Service struct {
	mu      sync.Mutex
	now     func() time.Time
	seq     int
	courses map[string]*Course
	// records 按疗程保存服药记录；recordKeys 以 疗程ID+计划时间 去重，保证幂等。
	records    map[string][]*DoseRecord
	recordKeys map[string]*DoseRecord
}

// NewService 创建服务实例。now 为 nil 时使用系统时间，测试可注入固定时钟。
func NewService(now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{
		now:        now,
		courses:    make(map[string]*Course),
		records:    make(map[string][]*DoseRecord),
		recordKeys: make(map[string]*DoseRecord),
	}
}

// CreateCourse 创建疗程，校验患者、药品、剂量、频次与时间。
func (s *Service) CreateCourse(in CreateCourseInput) (*Course, error) {
	if strings.TrimSpace(in.PatientID) == "" {
		return nil, invalidInput("patient_id", "患者不能为空")
	}
	if strings.TrimSpace(in.Medication) == "" {
		return nil, invalidInput("medication", "药品名称不能为空")
	}
	if err := validateDose(in.Dose); err != nil {
		return nil, err
	}
	if in.FrequencyPerDay < 1 || in.FrequencyPerDay > maxFrequencyPerDay {
		return nil, invalidInput("frequency_per_day", "每日服药频次需在 1 到 %d 次之间", maxFrequencyPerDay)
	}
	if in.StartTime.IsZero() {
		return nil, invalidInput("start_time", "开始时间不能为空")
	}
	if in.PlannedEndTime.IsZero() {
		return nil, invalidInput("planned_end_time", "计划结束时间不能为空")
	}
	if !in.PlannedEndTime.After(in.StartTime) {
		return nil, invalidInput("planned_end_time", "计划结束时间必须晚于开始时间 %s", in.StartTime.Format(time.RFC3339))
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now()
	s.seq++
	course := &Course{
		ID:              fmt.Sprintf("course-%06d", s.seq),
		PatientID:       strings.TrimSpace(in.PatientID),
		Medication:      strings.TrimSpace(in.Medication),
		Dose:            in.Dose,
		FrequencyPerDay: in.FrequencyPerDay,
		StartTime:       in.StartTime.UTC(),
		PlannedEndTime:  in.PlannedEndTime.UTC(),
		Status:          StatusActive,
		CreatedAt:       now,
		StatusChangedAt: now,
	}
	s.courses[course.ID] = course
	cp := *course
	return &cp, nil
}

func validateDose(d Dose) error {
	if math.IsNaN(d.Amount) || math.IsInf(d.Amount, 0) || d.Amount <= 0 {
		return invalidInput("dose.amount", "剂量必须为正数，当前值 %v 不合理", d.Amount)
	}
	if strings.TrimSpace(d.Unit) == "" {
		return invalidInput("dose.unit", "剂量单位不能为空，例如 mg、ml、片")
	}
	return nil
}

// ConfirmDose 按计划时间确认服药。同一疗程同一计划时间重复提交时返回已有记录，
// created 为 false，不会重复计数。
func (s *Service) ConfirmDose(courseID string, scheduledTime time.Time) (*DoseRecord, bool, error) {
	return s.addRecord(courseID, scheduledTime, RecordTaken, time.Time{}, "")
}

// MarkMissed 将某个计划时间登记为漏服，同样幂等。
func (s *Service) MarkMissed(courseID string, scheduledTime time.Time) (*DoseRecord, bool, error) {
	return s.addRecord(courseID, scheduledTime, RecordMissed, time.Time{}, "")
}

// BackfillDose 补记历史服药，必须提供实际发生时间，且不能晚于当前时间，
// 避免把历史记录伪装成刚刚服用。
func (s *Service) BackfillDose(courseID string, scheduledTime, occurredAt time.Time, note string) (*DoseRecord, bool, error) {
	if occurredAt.IsZero() {
		return nil, false, invalidInput("occurred_at", "补记必须说明实际服药时间")
	}
	if occurredAt.After(s.now()) {
		return nil, false, invalidInput("occurred_at", "补记的实际服药时间不能晚于当前时间")
	}
	return s.addRecord(courseID, scheduledTime, RecordBackfilled, occurredAt, note)
}

func (s *Service) addRecord(courseID string, scheduledTime time.Time, kind RecordKind, occurredAt time.Time, note string) (*DoseRecord, bool, error) {
	if scheduledTime.IsZero() {
		return nil, false, invalidInput("scheduled_time", "计划服药时间不能为空")
	}
	scheduledTime = scheduledTime.UTC()

	s.mu.Lock()
	defer s.mu.Unlock()

	course, ok := s.courses[courseID]
	if !ok {
		return nil, false, fmt.Errorf("%w：疗程 %s", ErrNotFound, courseID)
	}

	key := courseID + "|" + scheduledTime.Format(time.RFC3339Nano)
	if existing, dup := s.recordKeys[key]; dup {
		cp := *existing
		return &cp, false, nil
	}

	now := s.now()
	switch course.Status {
	case StatusActive:
		// 正常登记。
	case StatusPaused:
		return nil, false, &StateError{CourseID: courseID, Status: course.Status, Message: "疗程已暂停，请先恢复后再登记服药"}
	case StatusCompleted:
		return nil, false, &StateError{CourseID: courseID, Status: course.Status, Message: "疗程已完成，不能再登记服药"}
	case StatusCancelled:
		// 取消前的历史时间点允许补记事实，取消后的时间点不能登记为服药。
		if kind != RecordMissed && !scheduledTime.Before(course.StatusChangedAt) {
			return nil, false, &StateError{CourseID: courseID, Status: course.Status, Message: "疗程已取消，取消之后的时间点不能登记服药"}
		}
	default:
		return nil, false, &StateError{CourseID: courseID, Status: course.Status, Message: "未知状态"}
	}

	if kind != RecordMissed && occurredAt.IsZero() {
		occurredAt = now
	}

	s.seq++
	rec := &DoseRecord{
		ID:            fmt.Sprintf("dose-%06d", s.seq),
		CourseID:      courseID,
		ScheduledTime: scheduledTime,
		Kind:          kind,
		OccurredAt:    occurredAt,
		RecordedAt:    now,
		Note:          note,
	}
	s.recordKeys[key] = rec
	s.records[courseID] = append(s.records[courseID], rec)
	cp := *rec
	return &cp, true, nil
}

// PauseCourse 暂停疗程，暂停期间不能登记服药。
func (s *Service) PauseCourse(courseID string) (*Course, error) {
	return s.transition(courseID, StatusActive, StatusPaused)
}

// ResumeCourse 恢复已暂停的疗程。
func (s *Service) ResumeCourse(courseID string) (*Course, error) {
	return s.transition(courseID, StatusPaused, StatusActive)
}

// CompleteCourse 完成疗程，之后不能再登记服药。
func (s *Service) CompleteCourse(courseID string) (*Course, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	course, err := s.lockCourse(courseID)
	if err != nil {
		return nil, err
	}
	if course.Status != StatusActive && course.Status != StatusPaused {
		return nil, &StateError{CourseID: courseID, Status: course.Status, Message: "只有进行中或暂停中的疗程可以完成"}
	}
	course.Status = StatusCompleted
	course.StatusChangedAt = s.now()
	cp := *course
	return &cp, nil
}

// CancelCourse 取消疗程，已记录的服药事实保留。
func (s *Service) CancelCourse(courseID string) (*Course, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	course, err := s.lockCourse(courseID)
	if err != nil {
		return nil, err
	}
	if course.Status == StatusCompleted || course.Status == StatusCancelled {
		return nil, &StateError{CourseID: courseID, Status: course.Status, Message: "疗程已结束，不能重复取消"}
	}
	course.Status = StatusCancelled
	course.StatusChangedAt = s.now()
	cp := *course
	return &cp, nil
}

func (s *Service) transition(courseID string, from, to CourseStatus) (*Course, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	course, err := s.lockCourse(courseID)
	if err != nil {
		return nil, err
	}
	if course.Status == to {
		cp := *course
		return &cp, nil
	}
	if course.Status != from {
		return nil, &StateError{CourseID: courseID, Status: course.Status, Message: "当前状态不允许该操作"}
	}
	course.Status = to
	course.StatusChangedAt = s.now()
	cp := *course
	return &cp, nil
}

func (s *Service) lockCourse(courseID string) (*Course, error) {
	course, ok := s.courses[courseID]
	if !ok {
		return nil, fmt.Errorf("%w：疗程 %s", ErrNotFound, courseID)
	}
	return course, nil
}

// GetCourse 查询单个疗程。
func (s *Service) GetCourse(courseID string) (*Course, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	course, err := s.lockCourse(courseID)
	if err != nil {
		return nil, err
	}
	cp := *course
	return &cp, nil
}

// ListCoursesByPatient 按患者查询疗程，按创建时间、ID 稳定排序。
func (s *Service) ListCoursesByPatient(patientID string) []*Course {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*Course
	for _, c := range s.courses {
		if c.PatientID == patientID {
			cp := *c
			out = append(out, &cp)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out
}

// ListRecords 查询疗程的全部服药记录，按计划时间、登记时间、ID 稳定排序。
func (s *Service) ListRecords(courseID string) ([]*DoseRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.lockCourse(courseID); err != nil {
		return nil, err
	}
	recs := s.records[courseID]
	out := make([]*DoseRecord, 0, len(recs))
	for _, r := range recs {
		cp := *r
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].ScheduledTime.Equal(out[j].ScheduledTime) {
			return out[i].ScheduledTime.Before(out[j].ScheduledTime)
		}
		if !out[i].RecordedAt.Equal(out[j].RecordedAt) {
			return out[i].RecordedAt.Before(out[j].RecordedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// PlannedTimes 生成 [from, to] 范围内的计划服药时间点，按时间升序。
func (s *Service) PlannedTimes(courseID string, from, to time.Time) ([]time.Time, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	course, err := s.lockCourse(courseID)
	if err != nil {
		return nil, err
	}
	interval := time.Duration(24) * time.Hour / time.Duration(course.FrequencyPerDay)
	var out []time.Time
	for t := course.StartTime; !t.After(course.PlannedEndTime); t = t.Add(interval) {
		if t.Before(from) {
			continue
		}
		if t.After(to) {
			break
		}
		out = append(out, t)
	}
	return out, nil
}

// Adherence 汇总疗程截至当前的依从情况。
func (s *Service) Adherence(courseID string) (*Adherence, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	course, err := s.lockCourse(courseID)
	if err != nil {
		return nil, err
	}
	now := s.now()
	interval := time.Duration(24) * time.Hour / time.Duration(course.FrequencyPerDay)
	due := 0
	for t := course.StartTime; !t.After(course.PlannedEndTime) && !t.After(now); t = t.Add(interval) {
		due++
	}
	a := &Adherence{CourseID: courseID, Status: course.Status, DueCount: due}
	for _, r := range s.records[courseID] {
		switch r.Kind {
		case RecordTaken:
			a.TakenCount++
		case RecordMissed:
			a.MissedCount++
		case RecordBackfilled:
			a.BackfillCount++
		}
	}
	recorded := a.TakenCount + a.MissedCount + a.BackfillCount
	if due > recorded {
		a.PendingCount = due - recorded
	}
	return a, nil
}
