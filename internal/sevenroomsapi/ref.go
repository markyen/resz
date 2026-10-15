package sevenroomsapi

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// RefScheme prefixes every SevenRooms booking reference, mirroring the
// "rgs://" config ids Resy hands out.
const RefScheme = "sr://"

// Ref identifies one bookable SevenRooms slot well enough to hold and
// book it later.
//
// Resy's rgs:// config id is a single opaque token that carries the whole
// slot identity, so `resz availability | resz book` works by passing one
// string around. SevenRooms has no such token: a hold request needs the
// venue, date, time, party size, access rule and shift together. Ref
// packs exactly those into one URI so the same pipe keeps working.
type Ref struct {
	Venue     string
	TimeISO   string // "2006-01-02 15:04:05", venue-local wall clock
	PartySize int
	// AccessPersistentID is the access rule the slot was offered under.
	// Two slots at the same time in different seating areas differ only
	// by this, so it can't be dropped.
	AccessPersistentID string
	ShiftPersistentID  string
	Duration           int // minutes

	// RequiresPayment records that the venue wanted a card for this slot.
	// It is carried on the ref so `resz book` can refuse before holding a
	// table, rather than discovering it mid-checkout.
	RequiresPayment bool

	// Cost is the venue's advertised charge for the slot, in the venue's
	// currency. ChargeType says what it is charged per: "person" bills it
	// for every seat, anything else bills it once. Both ride on the ref so
	// the amount can be quoted to a human before anyone authorizes it —
	// see ChargeTotal.
	Cost       float64
	ChargeType string
}

// ChargeTotal is the slot's advertised price: Cost per person for a
// per-person charge, otherwise Cost once, and for the "_slot" charge types
// scaled by the slot's length in quarter hours, as the widget bills them.
// It reports 0 when the slot carries no price, which includes card-on-file
// slots that authorize nothing up front.
//
// It is a floor, not the bill: the venue adds any service charge, tax and
// gratuity at checkout. SlotCharge asks for the full amount.
func (r Ref) ChargeTotal() float64 {
	if r.Cost <= 0 {
		return 0
	}
	total := r.Cost
	switch r.ChargeType {
	case "person", "person_slot":
		if r.PartySize > 0 {
			total *= float64(r.PartySize)
		}
	}
	if strings.HasSuffix(r.ChargeType, "_slot") && r.Duration > 0 {
		total *= float64(r.Duration) / 15
	}
	return total
}

// String renders the ref as
// "sr://<venue>/<YYYY-MM-DDTHH:MM:SS>?party_size=...&access=...".
func (r Ref) String() string {
	q := url.Values{}
	q.Set("party_size", strconv.Itoa(r.PartySize))
	if r.AccessPersistentID != "" {
		q.Set("access", r.AccessPersistentID)
	}
	if r.ShiftPersistentID != "" {
		q.Set("shift", r.ShiftPersistentID)
	}
	if r.Duration > 0 {
		q.Set("duration", strconv.Itoa(r.Duration))
	}
	if r.RequiresPayment {
		q.Set("cc", "1")
	}
	if r.Cost > 0 {
		q.Set("cost", strconv.FormatFloat(r.Cost, 'f', -1, 64))
	}
	if r.ChargeType != "" {
		q.Set("charge", r.ChargeType)
	}
	stamp := strings.Replace(r.TimeISO, " ", "T", 1)
	return RefScheme + r.Venue + "/" + stamp + "?" + q.Encode()
}

// Date returns the ref's calendar date (YYYY-MM-DD).
func (r Ref) Date() string {
	if len(r.TimeISO) < 10 {
		return ""
	}
	return r.TimeISO[:10]
}

// BookDate renders the date in the MM-DD-YYYY form the book endpoint
// wants — which, confusingly, differs from the YYYY-MM-DD the hold
// endpoint wants.
func (r Ref) BookDate() (string, error) {
	t, err := time.Parse(slotDateLayout, r.TimeISO)
	if err != nil {
		return "", err
	}
	return t.Format("01-02-2006"), nil
}

// ClockTime renders the slot time the way both booking endpoints expect
// it: a 12-hour clock with no leading zero, e.g. "9:45 PM".
func (r Ref) ClockTime() (string, error) {
	t, err := time.Parse(slotDateLayout, r.TimeISO)
	if err != nil {
		return "", err
	}
	return t.Format("3:04 PM"), nil
}

// WidgetURL is the public reservation page for the ref's venue — where a
// card booking has to be completed while resz cannot charge one itself.
func (r Ref) WidgetURL() string {
	return BaseURL + "/reservations/" + r.Venue
}

// IsRef reports whether s looks like a SevenRooms booking reference.
func IsRef(s string) bool { return strings.HasPrefix(s, RefScheme) }

// ParseRef parses a reference produced by Ref.String.
func ParseRef(s string) (Ref, error) {
	if !IsRef(s) {
		return Ref{}, fmt.Errorf("not a SevenRooms reference: want a %q prefix", RefScheme)
	}
	rest := strings.TrimPrefix(s, RefScheme)
	path, rawQuery, _ := strings.Cut(rest, "?")
	venue, stamp, ok := strings.Cut(path, "/")
	if !ok || venue == "" || stamp == "" {
		return Ref{}, fmt.Errorf("malformed SevenRooms reference %q: want %s<venue>/<timestamp>", s, RefScheme)
	}
	timeISO := strings.Replace(stamp, "T", " ", 1)
	if _, err := time.Parse(slotDateLayout, timeISO); err != nil {
		return Ref{}, fmt.Errorf("malformed timestamp %q in %q: want YYYY-MM-DDTHH:MM:SS", stamp, s)
	}
	q, err := url.ParseQuery(rawQuery)
	if err != nil {
		return Ref{}, fmt.Errorf("malformed SevenRooms reference %q: %w", s, err)
	}
	r := Ref{
		Venue:              venue,
		TimeISO:            timeISO,
		AccessPersistentID: q.Get("access"),
		ShiftPersistentID:  q.Get("shift"),
		RequiresPayment:    q.Get("cc") == "1",
		ChargeType:         q.Get("charge"),
	}
	if v := q.Get("cost"); v != "" {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return Ref{}, fmt.Errorf("malformed cost %q in %q", v, s)
		}
		r.Cost = f
	}
	if v := q.Get("party_size"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return Ref{}, fmt.Errorf("malformed party_size %q in %q", v, s)
		}
		r.PartySize = n
	}
	if v := q.Get("duration"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return Ref{}, fmt.Errorf("malformed duration %q in %q", v, s)
		}
		r.Duration = n
	}
	return r, nil
}
