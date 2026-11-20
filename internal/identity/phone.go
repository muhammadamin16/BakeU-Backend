// Package identity contains normalization shared by account migrations and authentication.
package identity

import (
	"errors"
	"strings"
)

var ErrInvalidPhone = errors.New("invalid phone number")

// NormalizePhone accepts international numbers and Uzbekistan's nine-digit
// national form. It validates E.164 syntax, not assignment or ownership.
func NormalizePhone(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	number := strings.ReplaceAll(raw, " ", "")
	number = strings.NewReplacer("-", "", "(", "", ")", "", "\u00a0", "").Replace(number)
	switch {
	case strings.HasPrefix(number, "+"):
		number = number[1:]
	case strings.HasPrefix(number, "00"):
		number = number[2:]
	case len(number) == 9:
		number = "998" + number
	case len(number) == 12 && strings.HasPrefix(number, "998"):
	default:
		return "", ErrInvalidPhone
	}

	if len(number) < 2 || len(number) > 15 || number[0] == '0' ||
		(strings.HasPrefix(number, "998") && len(number) != 12) {
		return "", ErrInvalidPhone
	}
	for _, digit := range number {
		if digit < '0' || digit > '9' {
			return "", ErrInvalidPhone
		}
	}
	return "+" + number, nil
}
