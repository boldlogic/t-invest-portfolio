package portfolio

import (
	"strings"

	"github.com/shopspring/decimal"
	pb "opensource.tbank.ru/invest/invest-go/proto"
)

// Money — сумма без двоичной дроби.
// Amount хранит units + nano/1e9.
type Money struct {
	Amount   decimal.Decimal `json:"amount"`
	Currency string          `json:"currency"`
}

// Percent — котировка, которую API отдаёт как units и nano.
// Known=false, если поле в ответе отсутствовало (в песочнице так бывает у доходности).
type Percent struct {
	Value decimal.Decimal `json:"value"`
	Known bool            `json:"known"`
}

func quotation(q *pb.Quotation) (decimal.Decimal, bool) {
	if q == nil {
		return decimal.Zero, false
	}
	return unitsNano(q.GetUnits(), q.GetNano()), true
}

func moneyValue(m *pb.MoneyValue) (Money, bool) {
	if m == nil {
		return Money{}, false
	}
	return Money{
		Amount:   unitsNano(m.GetUnits(), m.GetNano()),
		Currency: strings.ToUpper(strings.TrimSpace(m.GetCurrency())),
	}, true
}

func unitsNano(units int64, nano int32) decimal.Decimal {
	return decimal.NewFromInt(units).Add(decimal.New(int64(nano), -9))
}

// rubMoney требует рубль у итоговых сумм портфеля.
// Пустое поле — ноль рублей. Чужая валюта — ошибка, ноль с чужой валютой не подставляется.
func rubMoney(m *pb.MoneyValue, field string) (Money, error) {
	value, ok := moneyValue(m)
	if !ok {
		return Money{Amount: decimal.Zero, Currency: "RUB"}, nil
	}
	if value.Currency == "" {
		value.Currency = "RUB"
	}
	if value.Currency != "RUB" {
		return Money{}, errForeignCurrency(field, value.Currency)
	}
	return value, nil
}

type currencyError struct {
	field    string
	currency string
}

func (e currencyError) Error() string {
	return e.field + ": ожидалась валюта RUB, получена " + e.currency
}

func errForeignCurrency(field, currency string) error {
	return currencyError{field: field, currency: currency}
}
