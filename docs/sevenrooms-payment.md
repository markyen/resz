# SevenRooms: card-backed bookings

Some slots will only be held against a credit card. The availability
payload marks them with `require_credit_card`, and optionally `cost` and
`charge_type` (`person` bills per seat, anything else bills once).

resz stores a card against a table. It does not spend: a slot with a
non-zero total is refused with `ErrChargeAtBooking` before any network
call, and a human books it instead.

## Request sequence

A card slot needs two extra steps between the hold and the booking.

| # | Request | Notes |
|---|---|---|
| 1 | `POST /api-yoa/dining/hold/add` | Returns `reservation_hold_id`. |
| 2 | `POST /booking/widget/<venue_id>/setup_intent` | Multipart, one field `amount` (0 for card-on-file). Returns a Stripe SetupIntent object directly — `id`, `client_secret` — not the `{status, data}` envelope the `api-yoa` endpoints use. |
| 3 | `POST https://api.stripe.com/v1/setup_intents/<id>/confirm` | Sent straight to Stripe with the venue's **publishable** key and `_stripe_account`. The card never reaches SevenRooms and resz never needs a Stripe secret key. Ends `status: "succeeded"`. |
| 4 | `POST /booking/dining/widget/<venue_id>/book` | The ordinary booking fields plus `card_token`. |

Two details are easy to get wrong:

- **`card_token` holds the SetupIntent id** (`seti_…`), not the payment
  method (`pm_…`) and not a card token.
- Step 2 sits under `/booking/widget/`, step 4 under
  `/booking/dining/widget/`, and both key on the venue's internal id
  rather than its URL slug.

The gateway is per-venue: `venue_info` publishes `payment_system`,
`payment_public_token` and `payment_account_id`. Only Stripe is
implemented. `Venue.TakesPayment` reports whether a venue can be paid at
all, and venues on other gateways are declined rather than guessed at.

## Booking response

```json
{ "status": "success", "message": "<confirmation code>",
  "reservation_id": "...", "api_reservation_id": "..." }
```

The id arrives as `reservation_id`, not the bare `id` the other endpoints
use. `message` reads like a status field but carries the short
confirmation code; `Reservation.Confirmation` exposes it.

## Bot detection

The browser widget also sends `radar_options[hcaptcha_token]` to Stripe,
and `new_widget_recaptcha_token`, `recaptcha_payment_transaction_id` and
`new_widget_recaptcha_mode` to the book endpoint.

resz sends none of them and must not forge, solve or replay them. They
appear to be optional today. If that changes, the answer is not to defeat
the check: `check` already falls back to requesting approval when a
booking fails, which routes the booking to a person.

## The card

The card is read from `$RESZ_CARD` and never touches disk. `cardsecret`
keeps its fields unexported and redacts under every `fmt` verb and
`MarshalJSON`, so a value that reaches a log line prints as
`•••• 1234 exp 01/2030`. Code that needs the digits calls `Number` or
`CVC` explicitly, which keeps those call sites easy to audit. There is no
`UnmarshalJSON`: a card comes from the environment, never from a file.
