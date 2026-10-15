package bookflow

import (
	"testing"
	"time"

	"resz/internal/platform"
)

// fakeVenue serves canned slots per date and counts lookups.
type fakeVenue struct {
	slots map[string][]platform.Slot
	finds int
}

func (f *fakeVenue) Platform() platform.Platform { return platform.Resy }
func (f *fakeVenue) ID() string                  { return "1" }
func (f *fakeVenue) Name() string                { return "Fake" }
func (f *fakeVenue) Timezone() string            { return "" }
func (f *fakeVenue) AvailableDates(int, func(string)) ([]string, error) {
	return nil, nil
}
func (f *fakeVenue) SlotsOn(date string, _ int, _ func(string)) ([]platform.Slot, error) {
	f.finds++
	return f.slots[date], nil
}

func resySlot(date, hm, template string) platform.Slot {
	start, _ := time.Parse("2006-01-02 15:04", date+" "+hm)
	return platform.Slot{
		Platform: platform.Resy,
		Start:    start,
		Ref:      "rgs://resy/1/" + template + "/1/" + date + "/" + date + "/" + hm + ":00/2/x",
	}
}

func TestCandidatesWalkBestFirstAndExcludeTemplates(t *testing.T) {
	// Saturdays, so the default window is 11:30-20:00.
	d1, d2 := "2026-11-14", "2026-11-21"
	fv := &fakeVenue{slots: map[string][]platform.Slot{
		d1: {resySlot(d1, "15:30", "togo"), resySlot(d1, "19:00", "togo"), resySlot(d1, "12:00", "counter")},
		d2: {resySlot(d2, "18:00", "counter")},
	}}
	c := NewCandidates(fv, 2, []string{d1, d2}, nil, nil)

	first, ok, err := c.Next()
	if err != nil || !ok || first.Ref != resySlot(d1, "19:00", "togo").Ref {
		t.Fatalf("first = %v %v %v; want d1 19:00 togo", first.Ref, ok, err)
	}
	if fv.finds != 1 {
		t.Errorf("finds = %d; dates should be fetched lazily", fv.finds)
	}
	// Excluding the template drops its 15:30 slot too.
	c.Exclude(first)
	next, ok, _ := c.Next()
	if !ok || next.Ref != resySlot(d1, "12:00", "counter").Ref {
		t.Fatalf("next = %v; want d1 12:00 counter", next.Ref)
	}
	last, ok, _ := c.Next()
	if !ok || last.Ref != resySlot(d2, "18:00", "counter").Ref {
		t.Fatalf("last = %v; want d2 18:00 counter", last.Ref)
	}
	if _, ok, err := c.Next(); ok || err != nil {
		t.Errorf("walk should be exhausted, got ok=%v err=%v", ok, err)
	}
}
