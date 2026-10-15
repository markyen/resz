# SevenRooms: card-backed bookings

Some slots will only be held against a credit card. The availability
payload marks them with `require_credit_card`, and optionally `cost` and
`charge_type`. Those are the *advertised* price; the venue's checkout adds
service charge, tax and gratuity on top (see Pricing).

resz spends money only with a person's say-so. Every unattended caller
(`check`, `snipe`) authorizes nothing, so a slot that charges is refused
with `ErrChargeAtBooking` and goes to a person for approval. `resz book
--authorize N` is how an approved charge gets booked: `N` is a ceiling,
checked against the advertised price before any request and against the
venue's full quote once it has one.

## Request sequence

A card slot adds steps between the hold and the booking. They mirror the
widget, which picks between two Stripe flows by what the slot costs.

| # | Request | Notes |
|---|---|---|
| 1 | `POST /api-yoa/dining/hold/add` | Returns `reservation_hold_id`. |
| 2 | `GET /api-yoa/availability/ng/widget/checkout-details` | The slot's payment settings; see Pricing. Query: `venue` (internal id), `venue_date` (YYYY-MM-DD), `time` ("5:30 PM"), `party_size`, `access_persistent_id`, `channel`, `hold_id`. Answers 400 "No availability on this date and time." for a slot that is gone. |
| 3a | `POST /booking/widget/<venue_id>/setup_intent` | **Nothing due:** multipart, `amount=0`. Returns a Stripe SetupIntent object directly — `id`, `client_secret` — not the `{status, data}` envelope. |
| 3b | `POST /booking/widget/<venue_id>/payment_intent` | **Something due, or a no-show pre-authorization:** multipart `amount` (venue currency units, formatted like JS `Number#toString`: `141.75`), `source=RElOSU5H` (base64 "DINING"), and `pre_authorization=true` for a hold. The intent is wrapped: `{"payload": {"client_secret": ...}}`. |
| 4b | `GET https://api.stripe.com/v1/payment_intents/<id>` | resz only, not the widget: reads the intent's `amount` with the publishable key and client secret, and refuses if it exceeds the quote. Nothing has moved yet. |
| 5 | `POST https://api.stripe.com/v1/{setup,payment}_intents/<id>/confirm` | Straight to Stripe with the venue's **publishable** key and `_stripe_account`. The card never reaches SevenRooms and resz never needs a Stripe secret key. A SetupIntent ends `succeeded`; a PaymentIntent `succeeded`, or `requires_capture` for a pre-authorization. This is the step that moves money. |
| 6 | `POST /booking/dining/widget/<venue_id>/book` | The ordinary booking fields plus `card_token` and, when money is due, the charge fields below. |

Details that are easy to get wrong:

- **`card_token` holds the intent id** — `seti_…` or `pi_…` — not the
  payment method (`pm_…`) and not a card token.
- Steps 3a/3b sit under `/booking/widget/`, step 6 under
  `/booking/dining/widget/`, and they key on the venue's internal id
  rather than its URL slug.
- When money is due, the book form also carries `charge_base_amount`,
  `charge_service_charge`, `charge_tax`, `charge_gratuity`,
  `charge_amount` and `amount_due`. The widget omits any that are zero
  (it drops falsy values), and so does resz; a card-on-file booking sends
  none of them.
- The money moves at step 5 and the booking happens at step 6. If step 6
  fails after that, the error names the Stripe payment so the venue can
  refund or release it. That is also the widget's order.

The gateway is per-venue: `venue_info` publishes `payment_system`,
`payment_public_token` and `payment_account_id`. Only Stripe is
implemented. `Venue.TakesPayment` reports whether a venue can be paid at
all, and venues on other gateways are declined rather than guessed at.

## Pricing

checkout-details returns `{slot, policy, payment}`. `payment` carries
`charge_type`, `cost`, `apply_service_charge`, `service_charge`,
`tax_rate`, `apply_gratuity_charge`, `gratuity_type`, `gratuity`,
`default_gratuity`, `require_credit_card`, `cc_payment_rule`,
`cc_party_size_min` and `computed_auto_cancellation_charge_cents`.
`sevenroomsapi.ComputeCharge` reproduces the widget's arithmetic:

- base: `cost` × party size for `person`, once for `reservation`; the
  `person_slot` / `reservation_slot` variants also multiply by the slot's
  duration in quarter hours. Any other charge type bills nothing.
- service charge: base × `service_charge`% when `apply_service_charge`.
- tax: (base + service charge) × `tax_rate`%.
- gratuity on the base, when `apply_gratuity_charge`: `default_gratuity`
  for `DEFAULT_GRATUITY`, nothing for `CLIENT_GRATUITY` (the diner picks;
  resz picks none), otherwise `gratuity`.
- due now: the sum, each figure rounded half-up to the cent.
- pre-authorization: when nothing is due, `cc_payment_rule` is
  `save_for_later`, the party is larger than `cc_party_size_min` (if set),
  and `computed_auto_cancellation_charge_cents` is non-zero, that many
  cents are held on the card for the no-show fee.

A slot advertised with a price is never paid for without this quote. A
card-on-file slot whose quote can't be fetched falls back to storing the
card with a SetupIntent.

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
