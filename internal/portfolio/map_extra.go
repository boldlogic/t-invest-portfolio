package portfolio

import (
	"fmt"

	"github.com/shopspring/decimal"
	pb "opensource.tbank.ru/invest/invest-go/proto"
)

func mapMoneyList(items []*pb.MoneyValue) []Money {
	out := make([]Money, 0, len(items))
	for _, item := range items {
		value, ok := moneyValue(item)
		if ok {
			out = append(out, value)
		}
	}
	return out
}

// ApplyPositions добавляет штуки и валюту из GetPositions.
func ApplyPositions(account *Account, resp *pb.PositionsResponse) {
	if resp == nil {
		return
	}
	account.PositionMoney = mapMoneyList(resp.GetMoney())
	account.PositionBlocked = mapMoneyList(resp.GetBlocked())
	account.LimitsLoading = resp.GetLimitsLoadingInProgress()
	if account.LimitsLoading {
		account.Warnings = append(account.Warnings, "остатки ещё выгружаются, количеству пока нельзя верить")
	}
	for _, item := range resp.GetSecurities() {
		account.Holdings = append(account.Holdings, Holding{
			Kind:            "security",
			Figi:            item.GetFigi(),
			Ticker:          item.GetTicker(),
			ClassCode:       item.GetClassCode(),
			InstrumentUID:   item.GetInstrumentUid(),
			InstrumentType:  item.GetInstrumentType(),
			Balance:         item.GetBalance(),
			Blocked:         item.GetBlocked(),
			ExchangeBlocked: item.GetExchangeBlocked(),
		})
	}
	for _, item := range resp.GetFutures() {
		account.Holdings = append(account.Holdings, Holding{
			Kind:          "future",
			Figi:          item.GetFigi(),
			Ticker:        item.GetTicker(),
			ClassCode:     item.GetClassCode(),
			InstrumentUID: item.GetInstrumentUid(),
			Balance:       item.GetBalance(),
			Blocked:       item.GetBlocked(),
		})
	}
	for _, item := range resp.GetOptions() {
		account.Holdings = append(account.Holdings, Holding{
			Kind:          "option",
			Ticker:        item.GetTicker(),
			ClassCode:     item.GetClassCode(),
			InstrumentUID: item.GetInstrumentUid(),
			Balance:       item.GetBalance(),
			Blocked:       item.GetBlocked(),
		})
	}
}

// ApplyWithdraw добавляет лимиты вывода.
func ApplyWithdraw(account *Account, resp *pb.WithdrawLimitsResponse) {
	if resp == nil {
		return
	}
	account.Withdraw = Withdraw{
		Money:     mapMoneyList(resp.GetMoney()),
		Blocked:   mapMoneyList(resp.GetBlocked()),
		Guarantee: mapMoneyList(resp.GetBlockedGuarantee()),
	}
}

// ApplyMargin заполняет маржу из успешного ответа.
func ApplyMargin(account *Account, resp *pb.GetMarginAttributesResponse) {
	if resp == nil {
		return
	}
	liquid, _ := moneyValue(resp.GetLiquidPortfolio())
	starting, _ := moneyValue(resp.GetStartingMargin())
	minimal, _ := moneyValue(resp.GetMinimalMargin())
	missing, _ := moneyValue(resp.GetAmountOfMissingFunds())
	account.Margin = Margin{
		Known:           true,
		LiquidPortfolio: liquid,
		StartingMargin:  starting,
		MinimalMargin:   minimal,
		MissingFunds:    missing,
		Sufficiency:     percentOf(resp.GetFundsSufficiencyLevel()),
	}
}

// MapOperation переводит строку истории. Идентификатор операции отбрасывается.
func MapOperation(item *pb.OperationItem) Operation {
	if item == nil {
		return Operation{}
	}
	payment, _ := moneyValue(item.GetPayment())
	operation := Operation{
		Type:          item.GetType().String(),
		Name:          item.GetName(),
		State:         item.GetState().String(),
		Figi:          item.GetFigi(),
		Ticker:        item.GetTicker(),
		InstrumentUID: item.GetInstrumentUid(),
		Payment:       payment,
		Quantity:      item.GetQuantityDone(),
	}
	if operation.Quantity == 0 {
		operation.Quantity = item.GetQuantity()
	}
	if stamp := item.GetDate(); stamp != nil {
		operation.At = stamp.AsTime()
	}
	if item.GetTradesInfo() != nil {
		for _, trade := range item.GetTradesInfo().GetTrades() {
			if trade == nil {
				continue
			}
			price, _ := moneyValue(trade.GetPrice())
			fill := Fill{
				TradeNum:      trade.GetNum(),
				InstrumentUID: item.GetInstrumentUid(),
				Figi:          item.GetFigi(),
				Ticker:        item.GetTicker(),
				Price:         price,
				Quantity:      trade.GetQuantity(),
			}
			if stamp := trade.GetDate(); stamp != nil {
				fill.At = stamp.AsTime()
			}
			operation.Fills = append(operation.Fills, fill)
		}
	}
	return operation
}

// MapBrokerTrade переводит строку брокерского отчёта.
func MapBrokerTrade(item *pb.BrokerReport) BrokerTrade {
	if item == nil {
		return BrokerTrade{}
	}
	amount, _ := moneyValue(item.GetTotalOrderAmount())
	brokerFee, _ := moneyValue(item.GetBrokerCommission())
	exchangeFee, _ := moneyValue(item.GetExchangeCommission())
	trade := BrokerTrade{
		TradeID:            item.GetTradeId(),
		Ticker:             item.GetTicker(),
		Name:               item.GetName(),
		Direction:          item.GetDirection(),
		Quantity:           item.GetQuantity(),
		Amount:             amount,
		BrokerCommission:   brokerFee,
		ExchangeCommission: exchangeFee,
	}
	if stamp := item.GetTradeDatetime(); stamp != nil {
		trade.At = stamp.AsTime()
	}
	return trade
}

// ApplyMarketPrices дописывает биржевые цены и не заменяет цену брокера.
// haveLast и haveClose говорят, что ответ биржи получен: тогда отсутствие цены — предупреждение.
func ApplyMarketPrices(account *Account, last, close []MarketQuote, haveLast, haveClose bool) {
	lastBy := indexQuotes(last)
	closeBy := indexQuotes(close)
	for i := range account.Positions {
		position := &account.Positions[i]
		if position.Cash {
			continue
		}
		if priceID(*position) == "" {
			continue
		}
		if haveLast {
			if value, ok := lookupQuote(lastBy, *position); ok {
				position.LastPrice = Quote{Value: value, Known: true}
			} else {
				account.Warnings = append(account.Warnings, fmt.Sprintf("%s: нет последней цены", positionLabel(*position)))
			}
		}
		if haveClose {
			if value, ok := lookupQuote(closeBy, *position); ok {
				position.ClosePrice = Quote{Value: value, Known: true}
			} else {
				account.Warnings = append(account.Warnings, fmt.Sprintf("%s: нет цены закрытия", positionLabel(*position)))
			}
		}
	}
}

func priceIDs(account Account) []string {
	seen := map[string]struct{}{}
	var ids []string
	for _, position := range account.Positions {
		if position.Cash {
			continue
		}
		id := priceID(position)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	return ids
}

func priceID(position Position) string {
	if position.InstrumentUID != "" {
		return position.InstrumentUID
	}
	return position.Figi
}

func indexQuotes(quotes []MarketQuote) map[string]decimal.Decimal {
	out := make(map[string]decimal.Decimal, len(quotes)*2)
	for _, quote := range quotes {
		if quote.InstrumentUID != "" {
			out[quote.InstrumentUID] = quote.Value
		}
		if quote.Figi != "" {
			out[quote.Figi] = quote.Value
		}
	}
	return out
}

func lookupQuote(index map[string]decimal.Decimal, position Position) (decimal.Decimal, bool) {
	if position.InstrumentUID != "" {
		if value, ok := index[position.InstrumentUID]; ok {
			return value, true
		}
	}
	if position.Figi != "" {
		if value, ok := index[position.Figi]; ok {
			return value, true
		}
	}
	return decimal.Zero, false
}
