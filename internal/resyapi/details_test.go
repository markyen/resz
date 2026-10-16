package resyapi

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func detailAgainst(t *testing.T, body string) (*Detail, error) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(body))
	}))
	defer srv.Close()
	return Details(&http.Client{Transport: rewriteTransport{srv}}, "tok", "rgs://resy/1/2/1/2026-11-14/2026-11-14/19:00:00/2/Table", nil)
}

// The parts of /3/details that say what booking costs, as Resy's booking
// widget reads them: payment.amounts and cancellation.fee.
func TestDetailsReportsWhatBookingCharges(t *testing.T) {
	for _, tc := range []struct {
		name          string
		body          string
		charge, fee   float64
		refusedAtZero bool
	}{
		{"deposit", `{"book_token":{"value":"bt"},"payment":{"amounts":{"reservation_charge":200.0,"quantity":2,"total":200.0},"config":{"type":"deposit"}},"cancellation":{"fee":null}}`, 200, 0, true},
		{"prepaid with fees", `{"book_token":{"value":"bt"},"payment":{"amounts":{"reservation_charge":300,"total":342.75}},"cancellation":{"fee":null}}`, 342.75, 0, true},
		{"numbers as strings", `{"book_token":{"value":"bt"},"payment":{"amounts":{"reservation_charge":"50.00","total":"50.00"}}}`, 50, 0, true},
		{"no-show fee only", `{"book_token":{"value":"bt"},"payment":{"amounts":{"reservation_charge":0,"total":0},"config":{"type":"free"}},"cancellation":{"fee":{"amount":25.0}}}`, 0, 25, false},
		{"free", `{"book_token":{"value":"bt"},"payment":{"amounts":{"total":0},"config":{"type":"free"}},"cancellation":{"fee":null}}`, 0, 0, false},
		{"no payment block", `{"book_token":{"value":"bt"}}`, 0, 0, false},
		{"fee in an unexpected shape", `{"book_token":{"value":"bt"},"payment":{"amounts":{"total":0}},"cancellation":{"fee":"25 USD"}}`, 0, 0, false},
	} {
		d, err := detailAgainst(t, tc.body)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if d.BookToken != "bt" || d.Charge != tc.charge || d.CancellationFee != tc.fee {
			t.Errorf("%s: got %+v; want charge %v, fee %v", tc.name, d, tc.charge, tc.fee)
		}
		if refused := errors.Is(d.Authorize(0), ErrChargeAtBooking); refused != tc.refusedAtZero {
			t.Errorf("%s: refused with nothing authorized = %v; want %v", tc.name, refused, tc.refusedAtZero)
		}
	}
}

// A charge Resy sends in a shape resz can't read must fail the call, not
// read as zero and slip past the guard.
func TestDetailsRejectsAnUnreadableCharge(t *testing.T) {
	if _, err := detailAgainst(t, `{"book_token":{"value":"bt"},"payment":{"amounts":{"total":{"value":200}}}}`); err == nil {
		t.Fatal("an unreadable total was accepted")
	}
}

func TestAuthorizeIsACeilingInCents(t *testing.T) {
	d := &Detail{Charge: 200}
	for _, tc := range []struct {
		authorized float64
		ok         bool
	}{{0, false}, {199.99, false}, {200, true}, {200.001, true}, {250, true}} {
		err := d.Authorize(tc.authorized)
		if (err == nil) != tc.ok {
			t.Errorf("Authorize(%v) = %v; want ok=%v", tc.authorized, err, tc.ok)
		}
		var ce *ChargeError
		if err != nil && (!errors.As(err, &ce) || ce.Charge != 200) {
			t.Errorf("Authorize(%v) = %v; want a ChargeError quoting $200", tc.authorized, err)
		}
	}
	if got := d.Authorize(100).Error(); got != "slot charges at booking: this slot charges $200.00 at booking, above the $100.00 authorized" {
		t.Errorf("message = %q", got)
	}
}
