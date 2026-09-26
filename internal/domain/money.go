package domain

import "fmt"

// Money is a monetary amount held in minor units (e.g. cents) to avoid the
// rounding errors that come with floating-point representation of currency.
// Currency is an ISO-4217 alphabetic code such as "USD".
type Money struct {
	// Amount is the value in minor units. 1099 with currency "USD" is $10.99.
	Amount   int64  `json:"amount_minor"`
	Currency string `json:"currency"`
}

// NewMoney builds a Money value from minor units and a currency code.
func NewMoney(minor int64, currency string) Money {
	return Money{Amount: minor, Currency: currency}
}

// Add returns the sum of two amounts of the same currency.
func (m Money) Add(other Money) (Money, error) {
	if m.Currency != other.Currency {
		return Money{}, fmt.Errorf("%w: cannot add %s to %s", ErrCurrencyMismatch, other.Currency, m.Currency)
	}
	return Money{Amount: m.Amount + other.Amount, Currency: m.Currency}, nil
}

// String renders the amount in major.minor form, e.g. "10.99 USD".
func (m Money) String() string {
	sign := ""
	amt := m.Amount
	if amt < 0 {
		sign = "-"
		amt = -amt
	}
	return fmt.Sprintf("%s%d.%02d %s", sign, amt/100, amt%100, m.Currency)
}
