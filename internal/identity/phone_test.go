package identity

import "testing"

func TestNormalizePhone(t *testing.T) {
	cases := map[string]string{
		"+998 (90) 123-45-67": "+998901234567",
		"998901234567":        "+998901234567",
		"00998901234567":      "+998901234567",
		"90 123 45 67":        "+998901234567",
		"+12025550123":        "+12025550123",
		"+99890123456":        "",
		"++998901234567":      "",
		"+0998901234567":      "",
		"+998901234567 ext 1": "",
		"":                    "",
	}
	for input, want := range cases {
		t.Run(input, func(t *testing.T) {
			got, err := NormalizePhone(input)
			if got != want || (err != nil) != (want == "") {
				t.Fatalf("NormalizePhone(%q) = %q, %v; want %q", input, got, err, want)
			}
		})
	}
}
