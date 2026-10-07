package gomedicationcourse

import "time"

// versionAt returns the prescription version governing t: the latest version
// whose EffectiveAt is not after t. The exact effective instant belongs to the
// new version, so a boundary time is planned under the new version.
func versionAt(c *Course, t time.Time) (PrescriptionVersion, error) {
	var chosen PrescriptionVersion
	found := false
	for _, v := range c.Versions {
		if !t.Before(v.EffectiveAt) {
			chosen = v
			found = true
		}
	}
	if !found {
		return PrescriptionVersion{}, NewConflict(ConflictInvalid,
			"time %s precedes initial prescription %s",
			t.Format(time.RFC3339), c.Versions[0].EffectiveAt.Format(time.RFC3339))
	}
	return chosen, nil
}

func findVersion(versions []PrescriptionVersion, n int) PrescriptionVersion {
	for _, v := range versions {
		if v.Version == n {
			return v
		}
	}
	return PrescriptionVersion{}
}

// segmentRange returns the closed-open [from,to) interval during which v owns
// plan time points. A version anchors its own sequence at its EffectiveAt; the
// next version's EffectiveAt ends the segment.
func segmentRange(c *Course, v PrescriptionVersion) (time.Time, time.Time) {
	from := v.EffectiveAt
	to := time.Time{}
	for _, other := range c.Versions {
		if other.Version == v.Version {
			continue
		}
		if other.EffectiveAt.After(from) && (to.IsZero() || other.EffectiveAt.Before(to)) {
			to = other.EffectiveAt
		}
	}
	return from, to
}

// isPlannedPoint reports whether t belongs to v's anchored sequence within v's
// segment, while also respecting course bounds. Because each version re-anchors
// at its own EffectiveAt, future points can be rearranged by an adjustment
// without ever rewriting historical points.
func isPlannedPoint(c *Course, v PrescriptionVersion, t time.Time) bool {
	from, to := segmentRange(c, v)
	if t.Before(from) {
		return false
	}
	if !to.IsZero() && !t.Before(to) {
		return false
	}
	if !c.EndAt.IsZero() && t.After(c.EndAt) {
		return false
	}
	delta := t.Sub(from)
	every := v.Frequency.Every
	if every <= 0 || delta < 0 || delta%every != 0 {
		return false
	}
	return true
}

// inPausedWindow reports whether t falls into any pause interval. Open-ended
// (currently active) pauses cover every time at or after From.
func inPausedWindow(c *Course, t time.Time) bool {
	for _, w := range c.Pauses {
		if t.Before(w.From) {
			continue
		}
		if w.To.IsZero() || t.Before(w.To) {
			return true
		}
	}
	return false
}

// ExpandPlan unfolds the schedule from start (inclusive) to end (exclusive, or
// bounded by the course end). Every returned point states the prescription
// version governing PlannedAt. Points covered by a pause window are omitted.
// Confirmed intakes are joined onto the points: an intake recorded before an
// adjustment stays attached at its original scheduled time and displays its
// original version and dose, even if the current plan no longer contains that
// time point.
func ExpandPlan(c *Course, start, end time.Time) ([]PlannedPoint, error) {
	if c == nil {
		return nil, NewConflict(ConflictInvalid, "course is nil")
	}
	if start.IsZero() {
		start = c.StartAt
	}
	if end.IsZero() {
		if c.EndAt.IsZero() {
			return nil, NewConflict(ConflictInvalid, "open-ended courses require an explicit end time")
		}
		end = c.EndAt
	}
	if end.Before(start) {
		return nil, NewConflict(ConflictInvalid, "end time must not be before start time")
	}

	points := make([]PlannedPoint, 0)
	for _, v := range c.Versions {
		from, to := segmentRange(c, v)
		segStart := maxTime(from, start)
		segEnd := end
		if !to.IsZero() && to.Before(segEnd) {
			segEnd = to
		}
		if !c.EndAt.IsZero() && c.EndAt.Before(segEnd) {
			segEnd = c.EndAt
		}
		if !segEnd.After(segStart) {
			continue
		}
		first := nextAnchorAtOrAfter(segStart, from, v.Frequency.Every)
		for t := first; t.Before(segEnd); t = t.Add(v.Frequency.Every) {
			if inPausedWindow(c, t) {
				continue
			}
			points = append(points, PlannedPoint{PlannedAt: t, Version: v.Version, Dose: v.Dose})
		}
	}

	byTime := make(map[time.Time]int, len(points))
	for i := range points {
		byTime[points[i].PlannedAt] = i
	}
	// Historical facts outside the current plan (because a later adjustment
	// rearranged future points) are appended so history is never deleted.
	for _, in := range c.Intakes {
		if in.ScheduledAt.Before(start) || !in.ScheduledAt.Before(end) {
			continue
		}
		fact := in
		if idx, ok := byTime[in.ScheduledAt]; ok {
			points[idx].Taken = &fact
		} else {
			points = append(points, PlannedPoint{
				PlannedAt: fact.ScheduledAt,
				Version:   fact.Version,
				Dose:      fact.Dose,
				Taken:     &fact,
			})
		}
	}
	sortPoints(points)
	return points, nil
}

func nextAnchorAtOrAfter(t, anchor time.Time, every time.Duration) time.Time {
	if !t.After(anchor) {
		return anchor
	}
	steps := (t.Sub(anchor) + every - 1) / every
	return anchor.Add(steps * every)
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}
