# SevenRooms booking

`resz` books SevenRooms venues as well as Resy ones. This note records how
the flow works and which details are load-bearing.

## There is no account

SevenRooms bookings are **guest checkouts**. There is no password, no API
key, and no token to refresh — every request simply states who the diner
is. So `resz` needs only the sort of thing you'd type into the
restaurant's own booking form, which lives in `config/account.json`:

```json
{
  "sevenrooms": {
    "first_name": "Jane",
    "last_name": "Doe",
    "email": "diner@example.com",
    "phone": "+15551234567"
  }
}
```

The phone number is written in E.164 form and split automatically into
the national number, dial code and ISO country the API wants. `+1`
resolves to the US; add `"country_code": "ca"` for a Canadian number, or
set both `country_code` and `dial_code` for any country `resz` doesn't
recognize (it refuses rather than guessing, since a wrong guess would put
bad data on a real reservation).

Nothing secret goes in this file. Resy's password still comes from
`$RESY_PASSWORD` and its token cache from `state/auth.json`.

SevenRooms *does* have an optional guest account ("Global Client"), but
it only pre-fills these same fields. Login is SMS one-time-passcode or
Google/Apple sign-in — there is no password — and the resulting
`Authorization: Bearer` token is optional on every booking call. Since it
buys nothing over guest checkout and the OTP is reCAPTCHA-gated, `resz`
does not use it.

## Credit cards

Some venues hold a reservation against a card. The requirement is visible
per-slot in the availability response (`require_credit_card`, already
evaluated for the requested party size, plus `cost` / `charge_type`) and
is recorded on the `sr://` reference as `cc=1`.

`resz` books these when a card is configured: the card is read from the
environment and confirmed straight to Stripe, and only the intent id
reaches SevenRooms. With no card configured the slot is refused with
`ErrPaymentRequired` before a table is held.

Slots that take money up front are refused with `ErrChargeAtBooking`
unless a person has approved the amount (`resz book --authorize N`);
`check` and `snipe` never authorize a charge and ask instead. See
`sevenrooms-payment.md`.

## The flow

`resz availability` prints an `sr://` reference carrying everything a
booking needs — venue, time, party size, access rule, shift, duration.
`resz book` then runs these calls, with the extra steps a card slot
needs documented in `sevenrooms-payment.md`:

1. **Hold** — `POST /api-yoa/dining/hold/add`, JSON:
   `{venue, date, time, party_size, access_persistent_id,
   shift_persistent_id, channel}` → `{reservation_hold_id,
   hold_duration_sec, ...}`. The hold lasts 300s.

2. **Book** — `POST /booking/dining/widget/{slug}/book`,
   `multipart/form-data` with the slot identity, the hold id and the
   guest fields, plus `card_token` for a card-backed slot. Headers: `X-Widget-Origin: new-widget` and
   `X-Checkout-Hash`, the hex SHA-256 of `"<first>|<last>"` lowercased
   and trimmed.

3. **Release on failure** — `POST
   /booking/dining/widget/{slug}/release-reservation-hold`, form
   `{reservation_hold_id, shift_persistent_id, date}`. Always attempted
   when the book call fails — including when a card is declined — so a
   failed attempt doesn't strand a real table for five minutes.

## Details that are easy to get wrong

- **The hold's `venue` must be the URL slug.** Passing the internal venue
  id (`venue_info.id`) fails with a 470.
- **`picked_duration` must be omitted** from the hold unless the venue
  offers selectable durations. Sending it fails with a 470, which reads
  identically to "no table available".
- **The two endpoints disagree about date format.** The hold wants
  `YYYY-MM-DD`; the book form wants `MM-DD-YYYY`.
- **Times are 12-hour with no leading zero** — `9:45 PM`, not `21:45`.
- **Marketing and SMS opt-ins are omitted, not set false.** The widget
  drops falsy fields from the form, so omitting them opts out.
- **`/book` rate-limits hard** and answers with an HTML page rather than
  JSON. `sevenroomsapi.IsRateLimited` recognizes it.
- **Hold failures are opaque** — 470/473 with no detail. In practice they
  mean the slot went away between availability and checkout.

## Read endpoints

| Purpose | Endpoint |
|---|---|
| Venue metadata, timezone, booking horizon | `GET /api-yoa/dining/venue_info?venue_url_key=<slug>` |
| Dates the venue is open for booking | `GET /api-yoa/availability/dates` |
| Slots for up to 3 days | `GET /api-yoa/availability/widget/range` |

- `widget/range` validates `num_days` strictly and accepts **only 1 or
  3**. Anything else is a 400.
- `availability/dates` reports dates the venue is *open*, not dates with
  inventory — unlike Resy's calendar. A fully-booked venue still lists
  every open date, so each has to be confirmed, which is why a
  no-availability scan costs ~30 requests.
- A slot's `type` is `"book"` (reservable now) or `"request"` (the venue
  will only take a request). Only `"book"` counts as availability.

## Testing

`internal/sevenroomsapi/live_test.go` holds opt-in tests against the real
API, enabled with `RESZ_LIVE_SEVENROOMS=1`. They cover venue lookup and a
hold/release round trip, and deliberately stop short of `Book` — a test
should never create a real obligation at a real restaurant.
