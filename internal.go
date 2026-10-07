package gomedicationcourse

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"time"
)

// CourseSnapshot 是疗程状态的只读复制。
type CourseSnapshot struct {
	ID        string
	Status    CourseStatus
	Versions  []PrescriptionVersion
	Points    []PlannedPoint
	Intakes   []IntakeRecord
	Pauses    []PauseRecord
	CreatedAt time.Time
	CreatedBy string
}

func snapshot(c *Course) *CourseSnapshot {
	snap := &CourseSnapshot{
		ID:        c.ID,
		Status:    c.Status,
		CreatedAt: c.CreatedAt,
		CreatedBy: c.CreatedBy,
		Versions:  append([]PrescriptionVersion(nil), c.Versions...),
		Points:    append([]PlannedPoint(nil), c.Points...),
		Intakes:   append([]IntakeRecord(nil), c.Intakes...),
		Pauses:    append([]PauseRecord(nil), c.Pauses...),
	}
	return snap
}

// versionAt 返回计划时间点 t 当时生效的处方版本。
// 规则：EffectiveAt <= t 的最后一个版本。EffectiveAt 相等时取新版本。
func versionAt(c *Course, t time.Time) PrescriptionVersion {
	v := c.Versions[0]
	for _, cand := range c.Versions[1:] {
		if !t.Before(cand.EffectiveAt) {
			v = cand
		}
	}
	return v
}

// advance 按版本链把计划点展开到 horizon（含）。
// 每次迈步都重新选择当时生效的版本，因此跨版本边界时
// 间隔与剂量会自动切换；已存在的时间点不会重复生成。
func (s *Service) advance(c *Course, horizon time.Time) {
	for !c.cursor.After(horizon) {
		if indexOfPoint(c, c.cursor) < 0 {
			v := versionAt(c, c.cursor)
			c.Points = append(c.Points, PlannedPoint{
				ScheduledAt: c.cursor,
				Version:     v.Version,
				Dosage:      v.Dosage,
				Status:      PointPending,
			})
		}
		v := versionAt(c, c.cursor)
		c.cursor = c.cursor.Add(v.Frequency.Interval)
	}
}

func indexOfPoint(c *Course, at time.Time) int {
	for i := range c.Points {
		if c.Points[i].ScheduledAt.Equal(at) {
			return i
		}
	}
	return -1
}

func findIntake(c *Course, scheduledAt time.Time) (IntakeRecord, bool) {
	for _, in := range c.Intakes {
		if in.ScheduledAt.Equal(scheduledAt) {
			return in, true
		}
	}
	return IntakeRecord{}, false
}

func latestIntakeTime(c *Course) time.Time {
	var latest time.Time
	for _, in := range c.Intakes {
		if in.ScheduledAt.After(latest) {
			latest = in.ScheduledAt
		}
	}
	return latest
}

func hashFields(fields ...string) string {
	h := sha256.New()
	for _, f := range fields {
		h.Write([]byte(f))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// adjustFingerprint 覆盖所有业务内容字段；同一请求号下任一不同即冲突。
func adjustFingerprint(cmd AdjustCommand) string {
	return hashFields(
		"adjust",
		cmd.Dosage.Unit,
		formatFloat(cmd.Dosage.Amount),
		cmd.Frequency.Interval.String(),
		cmd.EffectiveAt.UTC().Format(time.RFC3339Nano),
		cmd.Adjuster,
		cmd.Reason,
	)
}

func registerFingerprint(cmd RegisterCommand) string {
	return hashFields(
		"register",
		cmd.PlannedAt.UTC().Format(time.RFC3339Nano),
		formatInt(cmd.ExpectedVersion),
		cmd.TakenAt.UTC().Format(time.RFC3339Nano),
		cmd.Registrar,
	)
}

func formatFloat(f float64) string {
	return strconv.FormatFloat(f, 'g', -1, 64)
}

func formatInt(i int) string {
	return strconv.Itoa(i)
}
