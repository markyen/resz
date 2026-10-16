// Package bookflow composes the multi-call operations resz's subcommands
// need — opening a venue on whichever platform it lives on, filtering its
// calendar, picking the best bookable slot, and booking it. Keeping the
// dances here means search, availability, check, snipe and book all
// behave the same way whichever platform a venue is on.
//
// Reading availability is fully platform-neutral, expressed against
// platform.VenueClient. Booking cannot be: Resy books against a
// logged-in account while SevenRooms is a guest checkout, so Book
// dispatches to the right one.
package bookflow

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
	"unicode"

	"resz/internal/accountconfig"
	"resz/internal/cardsecret"
	"resz/internal/platform"
	"resz/internal/resyapi"
	"resz/internal/sevenroomsapi"
)

// PreferredTime is the target time for slot selection, expressed as
// "HH:MM" in the venue's local timezone. BestBookableSlot picks the slot
// whose start time is nearest to this.
const PreferredTime = "19:00"

// Open returns a client for a venue on the named platform. citySlug is
// only meaningful on Resy, where it supplies the venue's timezone;
// SevenRooms reports its own. Pass nil for client to use
// http.DefaultClient.
func Open(p platform.Platform, client *http.Client, venueID, citySlug string, logger func(string)) (platform.VenueClient, error) {
	switch p {
	case platform.Resy:
		return resyapi.Open(client, venueID, citySlug)
	case platform.SevenRooms:
		return sevenroomsapi.Open(client, venueID, logger)
	}
	return nil, fmt.Errorf("unknown platform %q", p)
}

// FilteredDates returns the venue's candidate dates strictly after today,
// excluding any in `blackout`, in ascending order.
//
// What "candidate" means varies by platform: Resy reports dates that
// genuinely have inventory, SevenRooms only dates the venue is open. Both
// are narrowed by BestBookableSlot, which confirms each date in turn.
func FilteredDates(vc platform.VenueClient, partySize int, blackout map[string]bool, logger func(string)) ([]string, error) {
	dates, err := vc.AvailableDates(partySize, logger)
	if err != nil {
		return nil, fmt.Errorf("calendar: %w", err)
	}
	today := time.Now().Format(platform.DateLayout)
	out := dates[:0]
	for _, d := range dates {
		if d > today && !blackout[d] {
			out = append(out, d)
		}
	}
	return out, nil
}

// BestBookableSlot returns the best slot (nearest to PreferredTime) on
// the earliest of `dates` that has any slot in the target window. Pass
// window=nil for the default weekday/weekend window.
//
// `dates` is typically the result of FilteredDates. The caller controls
// the ordering and may supply additional pre-filtering. Pass an empty or
// nil slice to short-circuit to ok=false.
//
// ok=false with no error means the slice was exhausted without finding a
// slot in the configured window. Any API error during the search
// propagates.
func BestBookableSlot(vc platform.VenueClient, partySize int, dates []string, window *platform.Window, logger func(string)) (date string, slot platform.Slot, ok bool, err error) {
	for _, d := range dates {
		slots, ferr := SlotsInWindow(vc, d, partySize, window, logger)
		if ferr != nil {
			return "", platform.Slot{}, false, ferr
		}
		idx := platform.BestSlotIndex(slots, PreferredTime)
		if idx < 0 {
			continue
		}
		return d, slots[idx], true, nil
	}
	return "", platform.Slot{}, false, nil
}

// SlotsInWindow returns the venue's slots on `date` that fall inside the
// booking window (or the default weekday/weekend window when window is
// nil).
func SlotsInWindow(vc platform.VenueClient, date string, partySize int, window *platform.Window, logger func(string)) ([]platform.Slot, error) {
	slots, err := vc.SlotsOn(date, partySize, logger)
	if err != nil {
		return nil, fmt.Errorf("find %s: %w", date, err)
	}
	matched, err := platform.FilterWindow(slots, date, window)
	if err != nil {
		return nil, err
	}
	return matched, nil
}

// Candidates walks a venue's in-window slots best-first: earliest of
// `dates` first, and nearest PreferredTime first within a date — the
// same order BestBookableSlot picks from. Dates are fetched lazily, so a
// caller that books the first candidate costs no more than
// BestBookableSlot does.
//
// It exists for callers that may have to pass over a slot, such as one
// needing add-ons resz can't select (see resyapi.ErrAddOnsRequired), and
// try the next-best instead. Exclude drops a slot's whole template from
// the rest of the walk.
type Candidates struct {
	vc        platform.VenueClient
	partySize int
	dates     []string
	window    *platform.Window
	logger    func(string)

	pending  []platform.Slot
	excluded map[string]bool
}

// NewCandidates returns a Candidates walk over dates. Pass window=nil
// for the default weekday/weekend window.
func NewCandidates(vc platform.VenueClient, partySize int, dates []string, window *platform.Window, logger func(string)) *Candidates {
	return &Candidates{vc: vc, partySize: partySize, dates: dates, window: window, logger: logger}
}

// Next returns the next candidate slot. ok=false with no error means the
// walk is exhausted. Any API error propagates.
func (c *Candidates) Next() (slot platform.Slot, ok bool, err error) {
	for {
		for len(c.pending) > 0 {
			s := c.pending[0]
			c.pending = c.pending[1:]
			if !c.excluded[resyapi.TemplateID(s.Ref)] {
				return s, true, nil
			}
		}
		if len(c.dates) == 0 {
			return platform.Slot{}, false, nil
		}
		d := c.dates[0]
		c.dates = c.dates[1:]
		slots, err := SlotsInWindow(c.vc, d, c.partySize, c.window, c.logger)
		if err != nil {
			return platform.Slot{}, false, err
		}
		platform.SortByPreference(slots, PreferredTime)
		c.pending = slots
	}
}

// Exclude skips every remaining slot that shares s's Resy template. It
// is a no-op for slots on other platforms.
func (c *Candidates) Exclude(s platform.Slot) {
	t := resyapi.TemplateID(s.Ref)
	if t == "" {
		return
	}
	if c.excluded == nil {
		c.excluded = map[string]bool{}
	}
	c.excluded[t] = true
}

// Book reserves a slot, dispatching on the platform it came from.
//
// The two platforms authenticate very differently. Resy books against a
// logged-in account, so it needs the cached token (refreshed from email
// and password). SevenRooms has no account at all: bookings are guest
// checkouts that carry the diner's name, email and phone inline, read
// from config/account.json.
//
// Returns the raw response body on success, or the body (which may be
// nil) plus a wrapped error on failure.
func Book(client *http.Client, slot platform.Slot, email, password string, logger func(string)) ([]byte, error) {
	switch slot.Platform {
	case platform.Resy:
		return AuthAndBook(client, slot.Ref, email, password, logger)
	case platform.SevenRooms:
		return BookSevenRooms(client, slot.Ref, logger)
	}
	return nil, fmt.Errorf("unknown platform %q on slot %s", slot.Platform, slot.Ref)
}

// BookSevenRooms books an "sr://" slot reference as a guest, using the
// diner details in config/account.json.
//
// A slot that needs a card is booked with the card from $RESZ_CARD when
// one is configured. A missing card is not an error here: the slot is
// refused with ErrPaymentRequired and the caller decides what to do,
// which for check means asking a human.
func BookSevenRooms(client *http.Client, ref string, logger func(string)) ([]byte, error) {
	res, err := BookSevenRoomsAuthorized(client, ref, 0, logger)
	if err != nil {
		return nil, err
	}
	return res.Raw, nil
}

// BookSevenRoomsAuthorized is BookSevenRooms with a ceiling on what the
// booking may charge, in the venue's currency. Zero authorizes nothing,
// which is what scheduled callers pass; a non-zero value comes from a
// person who has seen the amount and approved it. It returns the parsed
// reservation, which says what was charged and under which payment.
func BookSevenRoomsAuthorized(client *http.Client, ref string, authorizedUSD float64, logger func(string)) (*sevenroomsapi.Reservation, error) {
	parsed, err := sevenroomsapi.ParseRef(ref)
	if err != nil {
		return nil, err
	}
	guest, err := SevenRoomsGuest()
	if err != nil {
		return nil, err
	}
	var card *cardsecret.Card
	if parsed.RequiresPayment {
		if c, cerr := cardsecret.FromEnv(); cerr == nil {
			card = c
			if logger != nil {
				logger("slot needs a card; using " + c.Redacted())
			}
		} else if logger != nil {
			logger("slot needs a card and none is configured: " + cerr.Error())
		}
	}
	return sevenroomsapi.ReserveWithCard(client, parsed, guest, card, authorizedUSD, logger)
}

// SevenRoomsGuest reads the diner details from config/account.json and
// converts them into the form the booking API wants.
func SevenRoomsGuest() (sevenroomsapi.Guest, error) {
	cfg, err := accountconfig.LoadSevenRooms()
	if err != nil {
		return sevenroomsapi.Guest{}, err
	}
	national, dialCode, countryCode, err := cfg.PhoneParts()
	if err != nil {
		return sevenroomsapi.Guest{}, err
	}
	return sevenroomsapi.Guest{
		FirstName:   cfg.FirstName,
		LastName:    cfg.LastName,
		Email:       cfg.Email,
		PhoneNumber: national,
		DialCode:    dialCode,
		CountryCode: countryCode,
	}, nil
}

// AuthAndBook performs the Resy auth → details → book sequence used by
// `resz check` after a slot has been identified, authorizing no charge:
// a slot that takes a deposit or prepayment is refused with
// resyapi.ErrChargeAtBooking before anything is booked.
//
// Returns the raw response body from /3/book on success, or the body
// (which may be nil) plus a wrapped error on failure.
func AuthAndBook(client *http.Client, configID, email, password string, logger func(string)) ([]byte, error) {
	body, _, err := AuthAndBookAuthorized(client, configID, email, password, 0, logger)
	return body, err
}

// AuthAndBookAuthorized is AuthAndBook with a ceiling on what the booking
// may charge at booking, in dollars. Zero authorizes nothing, which is
// what scheduled callers pass; a non-zero value comes from a person who
// has seen the amount and approved it. If the cached token is rejected at
// /3/details with a 401/419, the cache is cleared and one refresh + retry
// is attempted before giving up.
//
// It also returns the slot's details, which say what was charged.
func AuthAndBookAuthorized(client *http.Client, configID, email, password string, authorizedUSD float64, logger func(string)) ([]byte, *resyapi.Detail, error) {
	auth, err := resyapi.EnsureAuth(email, password, logger)
	if err != nil {
		return nil, nil, fmt.Errorf("auth: %w", err)
	}
	detail, err := resyapi.Details(client, auth.Token, configID, logger)
	if err != nil {
		if resyapi.IsAuthError(err) {
			if logger != nil {
				logger("details: auth rejected, forcing refresh")
			}
			_ = resyapi.ClearAuth()
			auth, err = resyapi.EnsureAuth(email, password, logger)
			if err != nil {
				return nil, nil, fmt.Errorf("auth refresh: %w", err)
			}
			detail, err = resyapi.Details(client, auth.Token, configID, logger)
		}
		if err != nil {
			return nil, nil, fmt.Errorf("details: %w", err)
		}
	}
	if logger != nil && (detail.Charge > 0 || detail.CancellationFee > 0) {
		logger(fmt.Sprintf("details: charge at booking $%.2f, cancellation fee $%.2f", detail.Charge, detail.CancellationFee))
	}
	// /3/book charges the card on the Resy account without asking, so
	// this is the last point a deposit can be refused.
	if err := detail.Authorize(authorizedUSD); err != nil {
		return nil, detail, err
	}
	body, err := resyapi.Book(client, auth.Token, detail.BookToken, auth.PaymentMethodID, logger)
	if err != nil {
		return body, detail, fmt.Errorf("book: %w", err)
	}
	return body, detail, nil
}

// Search runs a venue search across the given platforms and merges the
// results, best match first.
//
// Merging makes ranking necessary rather than optional: Resy's
// venuesearch is broadly fuzzy and happily returns 75 loosely-related
// venues for a specific name, which would bury an exact match found on
// the other platform. So results are re-ordered by how closely the venue
// name matches the query, with each platform's own ordering preserved
// among equally good matches.
//
// A platform that fails is reported through errs (keyed by platform)
// rather than failing the whole search, so a Resy outage doesn't hide
// SevenRooms hits and vice versa.
func Search(client *http.Client, platforms []platform.Platform, query string, lat, long float64, logger func(string)) (hits []platform.SearchHit, errs map[platform.Platform]error) {
	errs = map[platform.Platform]error{}
	// A booking URL names its own platform. Sending one to the others is
	// worse than useless: Resy's full-text search happily returns an
	// unrelated venue for a URL it can't parse, and since no venue name
	// resembles a URL, ranking can't tell that hit apart from the real
	// one.
	if p, ok := platformOfURL(query); ok {
		platforms = intersect(platforms, p)
	}
	for _, p := range platforms {
		var (
			found []platform.SearchHit
			err   error
		)
		switch p {
		case platform.Resy:
			found, err = resyapi.Search(query, lat, long)
		case platform.SevenRooms:
			found, err = sevenroomsapi.Search(client, query, logger)
		default:
			err = fmt.Errorf("unknown platform %q", p)
		}
		if err != nil {
			errs[p] = err
			continue
		}
		hits = append(hits, found...)
	}
	sort.SliceStable(hits, func(i, j int) bool {
		return matchRank(hits[i].Name, query) < matchRank(hits[j].Name, query)
	})
	return hits, errs
}

// platformOfURL reports which platform a query identifies, when the
// query is a booking URL that says so.
func platformOfURL(query string) (platform.Platform, bool) {
	q := strings.ToLower(strings.TrimSpace(query))
	if !strings.Contains(q, "/") {
		return "", false
	}
	switch {
	case strings.Contains(q, "sevenrooms.com"):
		return platform.SevenRooms, true
	case strings.Contains(q, "resy.com"):
		return platform.Resy, true
	}
	return "", false
}

// intersect narrows platforms to want, or leaves it untouched if the
// caller explicitly excluded want (asking for --platform resy with a
// SevenRooms URL should find nothing, not silently search SevenRooms).
func intersect(platforms []platform.Platform, want platform.Platform) []platform.Platform {
	for _, p := range platforms {
		if p == want {
			return []platform.Platform{want}
		}
	}
	return nil
}

// matchRank scores how well a venue name answers a query — lower is
// better. Comparison ignores case, punctuation and spacing, so "Café
// Heron" matches a query of "blue heron" and a "theoldmill"
// slug's name matches "The Old Mill".
func matchRank(name, query string) int {
	n, q := foldName(name), foldName(query)
	switch {
	case q == "":
		return 3
	case n == q:
		return 0
	case strings.HasPrefix(n, q):
		return 1
	case strings.Contains(n, q):
		return 2
	default:
		return 3
	}
}

// foldName reduces a name to its comparable core: lowercase letters and
// digits only. Accented characters are left as-is — they fold to
// themselves on both sides of a comparison, and resz has no dependency
// that would strip diacritics.
func foldName(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}
