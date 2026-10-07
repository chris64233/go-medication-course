package gomedicationcourse

import "sort"

func sortPoints(p []PlannedPoint) {
	sort.SliceStable(p, func(i, j int) bool { return p[i].PlannedAt.Before(p[j].PlannedAt) })
}

func cloneCourse(c *Course) *Course {
	if c == nil {
		return nil
	}
	out := *c
	if c.Versions != nil {
		out.Versions = append([]PrescriptionVersion(nil), c.Versions...)
	}
	if c.Pauses != nil {
		out.Pauses = append([]PauseWindow(nil), c.Pauses...)
	}
	if c.Intakes != nil {
		out.Intakes = make([]Intake, len(c.Intakes))
		for i := range c.Intakes {
			in := c.Intakes[i]
			out.Intakes[i] = in
		}
	}
	if c.adjustments != nil {
		out.adjustments = make(map[string]int, len(c.adjustments))
		for k, v := range c.adjustments {
			out.adjustments[k] = v
		}
	} else {
		out.adjustments = map[string]int{}
	}
	return &out
}
