package gomedicationcourse

import (
	"fmt"
	"sort"
	"sync"
	"time"
)

// Clock 抽象当前时间，便于测试。
type Clock func() time.Time

// CreateCourseCommand 创建疗程的初始参数。
type CreateCourseCommand struct {
	ID        string
	Dosage    Dosage
	Frequency Frequency
	StartAt   time.Time
	CreatedBy string
	Reason    string
}

// Service 是疗程领域服务，内存存储，所有方法对同一疗程串行裁决。
// 服药登记、处方调整、暂停/恢复与疗程完成共用同一把锁，
// 因此总是以同一处方版本链与疗程状态进行并发裁决。
type Service struct {
	mu      sync.Mutex
	courses map[string]*Course
	now     Clock
}

// NewService 创建服务，clock 为 nil 时使用 time.Now。
func NewService(clock Clock) *Service {
	if clock == nil {
		clock = time.Now
	}
	return &Service{courses: map[string]*Course{}, now: clock}
}

func (s *Service) mustCourse(id string) (*Course, error) {
	c, ok := s.courses[id]
	if !ok {
		return nil, fmt.Errorf("%w: course not found", ErrInvalidArgument)
	}
	return c, nil
}

// CreateCourse 以初始处方（版本 1）创建疗程并展开截至当前的计划点。
func (s *Service) CreateCourse(cmd CreateCourseCommand) (*Course, error) {
	if cmd.ID == "" || cmd.Frequency.Interval <= 0 || cmd.Dosage.Amount <= 0 {
		return nil, fmt.Errorf("%w: id, positive dosage and frequency are required", ErrInvalidArgument)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.courses[cmd.ID]; exists {
		return nil, fmt.Errorf("%w: course already exists", ErrInvalidArgument)
	}
	now := s.now()
	c := &Course{
		ID:        cmd.ID,
		Status:    StatusActive,
		CreatedAt: now,
		CreatedBy: cmd.CreatedBy,
		cursor:    cmd.StartAt,
		requests:  map[string]idempotentRecord{},
	}
	c.Versions = append(c.Versions, PrescriptionVersion{
		Version:     1,
		Dosage:      cmd.Dosage,
		Frequency:   cmd.Frequency,
		EffectiveAt: cmd.StartAt,
		SubmittedAt: now,
		Adjuster:    cmd.CreatedBy,
		Reason:      cmd.Reason,
	})
	s.courses[cmd.ID] = c
	s.advance(c, now)
	return c, nil
}

// GetCourse 返回疗程快照（切片均为复制，调用方可安全持有）。
func (s *Service) GetCourse(id string) (*CourseSnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.mustCourse(id)
	if err != nil {
		return nil, err
	}
	return snapshot(c), nil
}

// Adjust 提交一个新的处方版本（调整剂量或频次）。
// 调整只影响生效时间之后尚未登记的计划点；已确认的服药事实、
// 生效时间之前的历史计划点都不会被重算或覆盖。
func (s *Service) Adjust(cmd AdjustCommand) (PrescriptionVersion, error) {
	if cmd.Dosage.Amount <= 0 || cmd.Frequency.Interval <= 0 {
		return PrescriptionVersion{}, fmt.Errorf("%w: positive dosage and frequency are required", ErrInvalidArgument)
	}
	if cmd.RequestID == "" {
		return PrescriptionVersion{}, fmt.Errorf("%w: request id is required", ErrInvalidArgument)
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	c, err := s.mustCourse(cmd.CourseID)
	if err != nil {
		return PrescriptionVersion{}, err
	}
	now := s.now()
	fp := adjustFingerprint(cmd)
	if rec, ok := c.requests[cmd.RequestID]; ok {
		// 同号同内容幂等返回原调整；任一字段不同均为冲突。
		if rec.kind != "adjust" || rec.fingerprint != fp {
			return PrescriptionVersion{}, ErrRequestConflict
		}
		return c.Versions[rec.adjustVersion-1], nil
	}

	// 所有校验先于任何写入；失败时计划与服药记录不发生部分改变。
	if c.Status == StatusCompleted || c.Status == StatusCancelled {
		return PrescriptionVersion{}, ErrCourseClosed
	}
	if c.Status == StatusPaused {
		return PrescriptionVersion{}, ErrCoursePaused
	}
	if cmd.EffectiveAt.Before(now) {
		return PrescriptionVersion{}, ErrEffectiveInPast
	}
	last := c.Versions[len(c.Versions)-1]
	if !cmd.EffectiveAt.After(last.EffectiveAt) {
		return PrescriptionVersion{}, fmt.Errorf("%w: effective time must be later than previous version", ErrInvalidArgument)
	}
	if len(c.Intakes) > 0 {
		latest := latestIntakeTime(c)
		if cmd.EffectiveAt.Before(latest) {
			return PrescriptionVersion{}, ErrEffectiveBeforeIntake
		}
	}

	newVersion := PrescriptionVersion{
		Version:     last.Version + 1,
		Dosage:      cmd.Dosage,
		Frequency:   cmd.Frequency,
		EffectiveAt: cmd.EffectiveAt,
		SubmittedAt: now,
		Adjuster:    cmd.Adjuster,
		Reason:      cmd.Reason,
	}
	c.Versions = append(c.Versions, newVersion)

	// 重排：生效时间之后未登记的计划点删除，历史点与已登记点保留。
	kept := c.Points[:0]
	for _, p := range c.Points {
		if p.Status == PointRegistered || p.ScheduledAt.Before(cmd.EffectiveAt) {
			kept = append(kept, p)
		}
	}
	c.Points = kept
	// 新版本生效时间成为新的展开锚点。
	c.cursor = cmd.EffectiveAt

	c.requests[cmd.RequestID] = idempotentRecord{
		kind: "adjust", fingerprint: fp, adjustVersion: newVersion.Version,
	}
	s.advance(c, now)
	return newVersion, nil
}

// RegisterIntake 登记一次服药。
// ExpectedVersion 必须与该计划点冻结的版本一致：
//   - 调整后携带旧版本登记未来时间点 -> ErrVersionConflict；
//   - 补记历史点必须携带该历史点的版本与剂量，记录标记 Backfilled，
//     不会伪装成新版本的正常服药。
func (s *Service) RegisterIntake(cmd RegisterCommand) (IntakeRecord, error) {
	if cmd.ExpectedVersion <= 0 {
		return IntakeRecord{}, fmt.Errorf("%w: expected version is required", ErrInvalidArgument)
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	c, err := s.mustCourse(cmd.CourseID)
	if err != nil {
		return IntakeRecord{}, err
	}
	now := s.now()

	if cmd.RequestID != "" {
		fp := registerFingerprint(cmd)
		if rec, ok := c.requests[cmd.RequestID]; ok {
			if rec.kind != "register" || rec.fingerprint != fp {
				return IntakeRecord{}, ErrRequestConflict
			}
			if in, ok := findIntake(c, cmd.PlannedAt); ok {
				return in, nil
			}
		}
	}

	if c.Status == StatusCancelled {
		return IntakeRecord{}, ErrCourseClosed
	}
	if c.Status == StatusPaused {
		return IntakeRecord{}, ErrCoursePaused
	}

	// 展开到目标计划时间（仅活跃疗程自动展开，覆盖“提前登记未来点”）。
	if c.Status == StatusActive {
		s.advance(c, cmd.PlannedAt)
	}
	idx := indexOfPoint(c, cmd.PlannedAt)
	if idx < 0 {
		return IntakeRecord{}, ErrNoSuchPlan
	}
	p := &c.Points[idx]
	if p.Status == PointVoided {
		return IntakeRecord{}, ErrCourseClosed
	}
	if c.Status == StatusCompleted && p.ScheduledAt.After(c.closedAt) {
		return IntakeRecord{}, ErrCourseClosed
	}
	if p.Status == PointRegistered {
		return IntakeRecord{}, ErrAlreadyRegistered
	}
	if p.Version != cmd.ExpectedVersion {
		return IntakeRecord{}, fmt.Errorf("%w: point is version %d, request carries %d",
			ErrVersionConflict, p.Version, cmd.ExpectedVersion)
	}
	takenAt := cmd.TakenAt
	if takenAt.IsZero() {
		takenAt = now
	}
	rec := IntakeRecord{
		ScheduledAt:  p.ScheduledAt,
		TakenAt:      takenAt,
		RegisteredAt: now,
		Version:      p.Version,
		Dosage:       p.Dosage,
		Backfilled:   now.After(p.ScheduledAt),
	}
	c.Intakes = append(c.Intakes, rec)
	p.Status = PointRegistered
	if cmd.RequestID != "" {
		c.requests[cmd.RequestID] = idempotentRecord{kind: "register", fingerprint: registerFingerprint(cmd)}
	}
	return rec, nil
}

// Pause 暂停疗程：未来未登记的计划点被移除（恢复后重排），历史点保留。
func (s *Service) Pause(courseID, operator string, at time.Time, reason string) (PauseRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.mustCourse(courseID)
	if err != nil {
		return PauseRecord{}, err
	}
	if c.Status == StatusCompleted || c.Status == StatusCancelled {
		return PauseRecord{}, ErrCourseClosed
	}
	if c.Status == StatusPaused {
		return PauseRecord{}, ErrInvalidStatus
	}
	if at.IsZero() {
		at = s.now()
	}
	c.Pauses = append(c.Pauses, PauseRecord{PausedAt: at, PausedBy: operator, Reason: reason})
	c.Status = StatusPaused
	kept := c.Points[:0]
	for _, p := range c.Points {
		if p.Status == PointRegistered || !p.ScheduledAt.After(at) {
			kept = append(kept, p)
		}
	}
	c.Points = kept
	return c.Pauses[len(c.Pauses)-1], nil
}

// Resume 恢复疗程：从恢复时刻起按当时生效的处方版本重新展开计划点。
func (s *Service) Resume(courseID, operator string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.mustCourse(courseID)
	if err != nil {
		return err
	}
	if c.Status != StatusPaused {
		return ErrInvalidStatus
	}
	if at.IsZero() {
		at = s.now()
	}
	c.Pauses[len(c.Pauses)-1].ResumedAt = at
	c.Pauses[len(c.Pauses)-1].ResumedBy = operator
	c.Status = StatusActive
	// 恢复时刻成为新锚点，版本链决定剂量与频次。
	c.cursor = at
	s.advance(c, s.now())
	return nil
}

// Complete 完成疗程：完成时刻之后的未服药计划点置为 voided（保留审计，不删除）；
// 完成时刻之前的 pending 点仍允许携带其历史版本补记。
func (s *Service) Complete(courseID, operator string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.mustCourse(courseID)
	if err != nil {
		return err
	}
	if c.Status == StatusCompleted || c.Status == StatusCancelled {
		return ErrCourseClosed
	}
	if at.IsZero() {
		at = s.now()
	}
	s.advance(c, at)
	for i := range c.Points {
		if c.Points[i].Status == PointPending && c.Points[i].ScheduledAt.After(at) {
			c.Points[i].Status = PointVoided
		}
	}
	c.Status = StatusCompleted
	c.closedAt = at
	return nil
}

// Cancel 取消疗程：所有未服药计划点置为 voided，之后不能再登记或调整。
func (s *Service) Cancel(courseID, operator string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.mustCourse(courseID)
	if err != nil {
		return err
	}
	if c.Status == StatusCompleted || c.Status == StatusCancelled {
		return ErrCourseClosed
	}
	if at.IsZero() {
		at = s.now()
	}
	for i := range c.Points {
		if c.Points[i].Status == PointPending {
			c.Points[i].Status = PointVoided
		}
	}
	c.Status = StatusCancelled
	c.closedAt = at
	return nil
}

// Schedule 返回 [from, to] 区间按时间展开的计划，每行明确标注采用的处方版本。
// 历史行来自冻结的展开结果；未来行按当前版本链即时展开，版本切换可跨行可见。
func (s *Service) Schedule(courseID string, from, to time.Time) ([]ScheduleEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.mustCourse(courseID)
	if err != nil {
		return nil, err
	}
	if c.Status == StatusActive {
		horizon := to
		if now := s.now(); now.After(horizon) {
			horizon = now
		}
		s.advance(c, horizon)
	}
	out := make([]ScheduleEntry, 0, len(c.Points))
	for _, p := range c.Points {
		if p.ScheduledAt.Before(from) || p.ScheduledAt.After(to) {
			continue
		}
		e := ScheduleEntry{
			ScheduledAt: p.ScheduledAt,
			Version:     p.Version,
			Dosage:      p.Dosage,
			Status:      p.Status,
		}
		if in, ok := findIntake(c, p.ScheduledAt); ok {
			v := in
			e.Intake = &v
		}
		out = append(out, e)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ScheduledAt.Before(out[j].ScheduledAt) })
	return out, nil
}

// History 返回处方调整历史（含初始版本，按版本号升序）。
func (s *Service) History(courseID string) ([]PrescriptionVersion, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.mustCourse(courseID)
	if err != nil {
		return nil, err
	}
	vs := make([]PrescriptionVersion, len(c.Versions))
	copy(vs, c.Versions)
	return vs, nil
}

// Intakes 返回全部已确认的服药事实（不可变，按登记顺序）。
func (s *Service) Intakes(courseID string) ([]IntakeRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.mustCourse(courseID)
	if err != nil {
		return nil, err
	}
	is := make([]IntakeRecord, len(c.Intakes))
	copy(is, c.Intakes)
	return is, nil
}
