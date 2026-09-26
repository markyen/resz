package accountconfig

import "testing"

func TestPhonePartsE164(t *testing.T) {
	tests := []struct {
		name                            string
		in                              SevenRooms
		national, dialCode, countryCode string
	}{
		{
			name:     "us number",
			in:       SevenRooms{Phone: "+15551234567"},
			national: "5551234567", dialCode: "1", countryCode: "us",
		},
		{
			name:     "formatting is ignored",
			in:       SevenRooms{Phone: "+1 (555) 123-4567"},
			national: "5551234567", dialCode: "1", countryCode: "us",
		},
		{
			name:     "uk number",
			in:       SevenRooms{Phone: "+442071234567"},
			national: "2071234567", dialCode: "44", countryCode: "gb",
		},
		{
			// Longest-prefix matching: 852 must win over any shorter code.
			name:     "hong kong number",
			in:       SevenRooms{Phone: "+85221234567"},
			national: "21234567", dialCode: "852", countryCode: "hk",
		},
		{
			// +1 defaults to the US, so Canada needs the override.
			name:     "explicit country override",
			in:       SevenRooms{Phone: "+16045551234", CountryCode: "CA"},
			national: "6045551234", dialCode: "1", countryCode: "ca",
		},
		{
			name:     "explicit dial code without a plus",
			in:       SevenRooms{Phone: "5551234567", DialCode: "1", CountryCode: "us"},
			national: "5551234567", dialCode: "1", countryCode: "us",
		},
	}
	for _, tt := range tests {
		n, d, c, err := tt.in.PhoneParts()
		if err != nil {
			t.Errorf("%s: PhoneParts: %v", tt.name, err)
			continue
		}
		if n != tt.national || d != tt.dialCode || c != tt.countryCode {
			t.Errorf("%s: got (%q,%q,%q); want (%q,%q,%q)",
				tt.name, n, d, c, tt.national, tt.dialCode, tt.countryCode)
		}
	}
}

func TestPhonePartsRejectsAmbiguous(t *testing.T) {
	tests := []struct {
		name string
		in   SevenRooms
	}{
		// Without a "+" there's no country information to work from, and
		// guessing would put bad data on a real reservation.
		{"no plus, no overrides", SevenRooms{Phone: "5551234567"}},
		{"unknown dial code", SevenRooms{Phone: "+9991234567"}},
		{"country code only", SevenRooms{Phone: "+1"}},
		{"no digits", SevenRooms{Phone: "not a phone"}},
	}
	for _, tt := range tests {
		if _, _, _, err := tt.in.PhoneParts(); err == nil {
			t.Errorf("%s: PhoneParts(%q) succeeded; want an error", tt.name, tt.in.Phone)
		}
	}
}

func TestValidate(t *testing.T) {
	ok := SevenRooms{FirstName: "Jane", LastName: "Doe", Email: "a@b.com", Phone: "+15551234567"}
	if err := ok.Validate(); err != nil {
		t.Errorf("Validate on a complete config: %v", err)
	}

	tests := []struct {
		name string
		in   SevenRooms
	}{
		{"missing first name", SevenRooms{LastName: "Doe", Email: "a@b.com", Phone: "+15551234567"}},
		{"missing last name", SevenRooms{FirstName: "Jane", Email: "a@b.com", Phone: "+15551234567"}},
		{"missing email", SevenRooms{FirstName: "Jane", LastName: "Doe", Phone: "+15551234567"}},
		{"missing phone", SevenRooms{FirstName: "Jane", LastName: "Doe", Email: "a@b.com"}},
		{"blank is not present", SevenRooms{FirstName: "  ", LastName: "Doe", Email: "a@b.com", Phone: "+15551234567"}},
		{"email without @", SevenRooms{FirstName: "Jane", LastName: "Doe", Email: "nope", Phone: "+15551234567"}},
		{"unparseable phone", SevenRooms{FirstName: "Jane", LastName: "Doe", Email: "a@b.com", Phone: "12345"}},
	}
	for _, tt := range tests {
		if err := tt.in.Validate(); err == nil {
			t.Errorf("%s: Validate succeeded; want an error", tt.name)
		}
	}
}
