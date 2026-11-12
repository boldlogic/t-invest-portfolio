package portfolio

import (
	"fmt"
	"strings"

	"github.com/shopspring/decimal"
	pb "opensource.tbank.ru/invest/invest-go/proto"
)

// MapPortfolio переводит ответ GetPortfolio в текущее состояние одного счёта.
// Итоговые суммы должны быть в RUB. Цены позиций остаются в валюте инструмента:
// у иностранной бумаги это не ошибка, но сверка с итогом тогда неполная.
func MapPortfolio(meta AccountMeta, resp *pb.PortfolioResponse) (Account, error) {
	if resp == nil {
		return Account{}, fmt.Errorf("пустой ответ портфеля счёта %s", meta.ID)
	}

	totals, err := mapTotals(resp)
	if err != nil {
		return Account{}, fmt.Errorf("счёт %s: %w", meta.ID, err)
	}

	account := Account{
		ID:        meta.ID,
		Name:      meta.Name,
		Type:      meta.Type,
		TypeLabel: accountTypeLabel(meta.Type),
		Totals:    totals,
	}

	sum := decimal.Zero
	for _, raw := range resp.GetPositions() {
		position, warnings := mapPosition(raw)
		account.Positions = append(account.Positions, position)
		account.Warnings = append(account.Warnings, warnings...)
		if !position.MarketValueKnown {
			account.ValueCheckPartial = true
			continue
		}
		if position.MarketValue.Currency != "RUB" {
			account.ValueCheckPartial = true
			continue
		}
		sum = sum.Add(position.MarketValue.Amount)
	}

	account.PositionsValue = Money{Amount: sum, Currency: "RUB"}
	account.TotalDelta = Money{
		Amount:   sum.Sub(totals.Portfolio.Amount),
		Currency: "RUB",
	}

	for _, raw := range resp.GetVirtualPositions() {
		account.VirtualPositions = append(account.VirtualPositions, mapVirtual(raw))
	}
	return account, nil
}

func mapTotals(resp *pb.PortfolioResponse) (Totals, error) {
	var totals Totals
	var err error
	if totals.Shares, err = rubMoney(resp.GetTotalAmountShares(), "акции"); err != nil {
		return Totals{}, err
	}
	if totals.Bonds, err = rubMoney(resp.GetTotalAmountBonds(), "облигации"); err != nil {
		return Totals{}, err
	}
	if totals.ETF, err = rubMoney(resp.GetTotalAmountEtf(), "фонды"); err != nil {
		return Totals{}, err
	}
	if totals.Currencies, err = rubMoney(resp.GetTotalAmountCurrencies(), "валюты"); err != nil {
		return Totals{}, err
	}
	if totals.Futures, err = rubMoney(resp.GetTotalAmountFutures(), "фьючерсы"); err != nil {
		return Totals{}, err
	}
	if totals.Options, err = rubMoney(resp.GetTotalAmountOptions(), "опционы"); err != nil {
		return Totals{}, err
	}
	if totals.StructuredNotes, err = rubMoney(resp.GetTotalAmountSp(), "структурные ноты"); err != nil {
		return Totals{}, err
	}
	if totals.DFA, err = rubMoney(resp.GetTotalAmountDfa(), "смарт-активы"); err != nil {
		return Totals{}, err
	}
	if totals.Portfolio, err = rubMoney(resp.GetTotalAmountPortfolio(), "итог портфеля"); err != nil {
		return Totals{}, err
	}
	if totals.VarMargin, err = rubMoney(resp.GetTotalVarMargin(), "вариационная маржа"); err != nil {
		return Totals{}, err
	}
	if totals.DailyYield, err = rubMoney(resp.GetDailyYield(), "доходность за день"); err != nil {
		return Totals{}, err
	}
	totals.ExpectedYield = percentOf(resp.GetExpectedYield())
	totals.DailyYieldPercent = percentOf(resp.GetDailyYieldRelative())
	return totals, nil
}

func mapPosition(raw *pb.PortfolioPosition) (Position, []string) {
	if raw == nil {
		return Position{}, []string{"пустая позиция в ответе"}
	}

	quantity, _ := quotation(raw.GetQuantity())
	blockedLots, _ := quotation(raw.GetBlockedLots())
	price, priceOK := moneyValue(raw.GetCurrentPrice())
	nkd, nkdOK := moneyValue(raw.GetCurrentNkd())
	average, _ := moneyValue(raw.GetAveragePositionPrice())
	averageFIFO, _ := moneyValue(raw.GetAveragePositionPriceFifo())
	varMargin, _ := moneyValue(raw.GetVarMargin())

	position := Position{
		Figi:             raw.GetFigi(),
		Ticker:           raw.GetTicker(),
		ClassCode:        raw.GetClassCode(),
		InstrumentType:   raw.GetInstrumentType(),
		InstrumentUID:    raw.GetInstrumentUid(),
		Quantity:         quantity,
		AveragePrice:     average,
		AveragePriceFIFO: averageFIFO,
		CurrentPrice:     price,
		CurrentNKD:       nkd,
		ExpectedYield:    percentOf(raw.GetExpectedYield()),
		Blocked:          raw.GetBlocked(),
		BlockedLots:      blockedLots,
		VarMargin:        varMargin,
		Cash:             isCash(raw.GetFigi(), raw.GetInstrumentType()),
	}
	if position.Cash && position.Name == "" && raw.GetFigi() == CashRUBFigi {
		position.Name = "Российский рубль"
		if position.Ticker == "" {
			position.Ticker = "RUB"
		}
	}

	var warnings []string
	if !priceOK {
		return position, warnings
	}

	value := price.Amount.Mul(quantity)
	currency := price.Currency
	if currency == "" {
		currency = "RUB"
	}
	if nkdOK && !nkd.Amount.IsZero() {
		nkdCurrency := nkd.Currency
		if nkdCurrency == "" {
			nkdCurrency = currency
		}
		if nkdCurrency != currency {
			warnings = append(warnings, fmt.Sprintf(
				"%s: НКД в %s не прибавлен к цене в %s",
				positionLabel(position), nkdCurrency, currency,
			))
		} else {
			// В ответе current_nkd — НКД одной облигации. Итог брокера сходится с ценой × количество + НКД × количество.
			value = value.Add(nkd.Amount.Mul(quantity))
		}
	}
	position.MarketValue = Money{Amount: value, Currency: currency}
	position.MarketValueKnown = true
	return position, warnings
}

func mapVirtual(raw *pb.VirtualPortfolioPosition) VirtualPosition {
	if raw == nil {
		return VirtualPosition{}
	}
	quantity, _ := quotation(raw.GetQuantity())
	average, _ := moneyValue(raw.GetAveragePositionPrice())
	price, _ := moneyValue(raw.GetCurrentPrice())
	position := VirtualPosition{
		Figi:           raw.GetFigi(),
		Ticker:         raw.GetTicker(),
		InstrumentType: raw.GetInstrumentType(),
		InstrumentUID:  raw.GetInstrumentUid(),
		Quantity:       quantity,
		AveragePrice:   average,
		CurrentPrice:   price,
		ExpectedYield:  percentOf(raw.GetExpectedYield()),
	}
	if stamp := raw.GetExpireDate(); stamp != nil {
		position.ExpireDate = stamp.AsTime()
		position.HasExpireDate = true
	}
	return position
}

func percentOf(q *pb.Quotation) Percent {
	value, ok := quotation(q)
	return Percent{Value: value, Known: ok}
}

func isCash(figi, instrumentType string) bool {
	return figi == CashRUBFigi || strings.EqualFold(instrumentType, "currency")
}

func positionLabel(position Position) string {
	if position.Ticker != "" {
		return position.Ticker
	}
	if position.Figi != "" {
		return position.Figi
	}
	return position.InstrumentUID
}

func accountTypeLabel(accountType string) string {
	switch accountType {
	case "ACCOUNT_TYPE_TINKOFF":
		return "Брокерский"
	case "ACCOUNT_TYPE_TINKOFF_IIS":
		return "ИИС"
	case AccountTypeInvestBox:
		return "Инвесткопилка"
	case "ACCOUNT_TYPE_INVEST_FUND":
		return "Фонд денежного рынка"
	default:
		if accountType == "" {
			return "Счёт"
		}
		return accountType
	}
}

// ApplyInstrument дописывает тикер и название, если их не было в позиции.
func ApplyInstrument(position *Position, meta InstrumentMeta) {
	if position.Ticker == "" {
		position.Ticker = meta.Ticker
	}
	if position.Name == "" {
		position.Name = meta.Name
	}
}

// ApplyVirtualInstrument дописывает тикер и название виртуальной позиции.
func ApplyVirtualInstrument(position *VirtualPosition, meta InstrumentMeta) {
	if position.Ticker == "" {
		position.Ticker = meta.Ticker
	}
	if position.Name == "" {
		position.Name = meta.Name
	}
}
