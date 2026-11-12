package tinvest

import (
	"context"
	"time"

	"github.com/shopspring/decimal"
	"google.golang.org/protobuf/proto"
	pb "opensource.tbank.ru/invest/invest-go/proto"

	"t-invest-portfolio/internal/portfolio"
)

// Share читает паспорт акции. Цена брокера здесь не считается.
func (c *Client) Share(ctx context.Context, uid string) (portfolio.SharePassport, error) {
	started := time.Now()
	request := map[string]any{"uid": uid}
	if err := ctx.Err(); err != nil {
		c.note("ShareBy", started, request, nil, err)
		return portfolio.SharePassport{}, err
	}
	resp, err := c.instruments.ShareByUid(uid)
	var body proto.Message
	if resp != nil && resp.ShareResponse != nil {
		body = resp.ShareResponse
	}
	c.note("ShareBy", started, request, body, err)
	if err != nil || resp == nil {
		return portfolio.SharePassport{}, err
	}
	instrument := resp.GetInstrument()
	if instrument == nil {
		return portfolio.SharePassport{}, nil
	}
	nominal := moneyFrom(instrument.GetNominal())
	return portfolio.SharePassport{
		InstrumentUID: uid,
		Lot:           instrument.GetLot(),
		Currency:      instrument.GetCurrency(),
		ClassCode:     instrument.GetClassCode(),
		Exchange:      calendarExchange(instrument.GetRealExchange()),
		ISIN:          instrument.GetIsin(),
		Sector:        instrument.GetSector(),
		ShareType:     instrument.GetShareType().String(),
		DividendYield: instrument.GetDivYieldFlag(),
		Nominal:       nominal,
		NominalKnown:  instrument.GetNominal() != nil,
		ForQual:       instrument.GetForQualInvestorFlag(),
		AssetUID:      instrument.GetAssetUid(),
		Known:         true,
	}, nil
}

// Etf читает паспорт фонда.
func (c *Client) Etf(ctx context.Context, uid string) (portfolio.EtfPassport, error) {
	started := time.Now()
	request := map[string]any{"uid": uid}
	if err := ctx.Err(); err != nil {
		c.note("EtfBy", started, request, nil, err)
		return portfolio.EtfPassport{}, err
	}
	resp, err := c.instruments.EtfByUid(uid)
	var body proto.Message
	if resp != nil && resp.EtfResponse != nil {
		body = resp.EtfResponse
	}
	c.note("EtfBy", started, request, body, err)
	if err != nil || resp == nil {
		return portfolio.EtfPassport{}, err
	}
	instrument := resp.GetInstrument()
	if instrument == nil {
		return portfolio.EtfPassport{}, nil
	}
	commission, known := quoteDecimal(instrument.GetFixedCommission())
	return portfolio.EtfPassport{
		InstrumentUID:   uid,
		Lot:             instrument.GetLot(),
		Currency:        instrument.GetCurrency(),
		ClassCode:       instrument.GetClassCode(),
		Exchange:        calendarExchange(instrument.GetRealExchange()),
		ISIN:            instrument.GetIsin(),
		Sector:          instrument.GetSector(),
		FocusType:       instrument.GetFocusType(),
		Commission:      commission,
		CommissionKnown: known,
		Rebalancing:     instrument.GetRebalancingFreq(),
		AssetUID:        instrument.GetAssetUid(),
		Known:           true,
	}, nil
}

// RiskRates читает ставки пачкой. Ошибка одной бумаги остаётся в Problems.
func (c *Client) RiskRates(ctx context.Context, instrumentIDs []string) ([]portfolio.RiskRate, []portfolio.RiskProblem, error) {
	started := time.Now()
	request := map[string]any{"instrument_ids": instrumentIDs}
	if err := ctx.Err(); err != nil {
		c.note("GetRiskRates", started, request, nil, err)
		return nil, nil, err
	}
	if len(instrumentIDs) == 0 {
		return nil, nil, nil
	}
	resp, err := c.instruments.GetRiskRates(instrumentIDs)
	var body proto.Message
	if resp != nil && resp.RiskRatesResponse != nil {
		body = resp.RiskRatesResponse
	}
	c.note("GetRiskRates", started, request, body, err)
	if err != nil || resp == nil {
		return nil, nil, err
	}
	var rates []portfolio.RiskRate
	var problems []portfolio.RiskProblem
	for _, item := range resp.GetInstrumentRiskRates() {
		if item == nil {
			continue
		}
		if item.GetError() != "" {
			problems = append(problems, portfolio.RiskProblem{
				InstrumentUID: item.GetInstrumentUid(),
				Message:       item.GetError(),
			})
		}
		rates = append(rates, riskSide(item.GetInstrumentUid(), "long", "user", item.GetLongRiskRate())...)
		rates = append(rates, riskSide(item.GetInstrumentUid(), "short", "user", item.GetShortRiskRate())...)
		for _, rate := range item.GetLongRiskRates() {
			rates = append(rates, riskSide(item.GetInstrumentUid(), "long", "available", rate)...)
		}
		for _, rate := range item.GetShortRiskRates() {
			rates = append(rates, riskSide(item.GetInstrumentUid(), "short", "available", rate)...)
		}
	}
	return rates, problems, nil
}

// TradingSchedule читает один календарный день одной биржи.
func (c *Client) TradingSchedule(ctx context.Context, exchange string, day time.Time) ([]portfolio.TradingDay, error) {
	started := time.Now()
	request := map[string]any{"exchange": exchange, "day": day.UTC().Format("2006-01-02")}
	if err := ctx.Err(); err != nil {
		c.note("TradingSchedules", started, request, nil, err)
		return nil, err
	}
	if exchange != "MOEX" && exchange != "SPB" {
		return nil, nil
	}
	from := time.Date(day.UTC().Year(), day.UTC().Month(), day.UTC().Day(), 0, 0, 0, 0, time.UTC)
	to := from.Add(24 * time.Hour)
	resp, err := c.instruments.TradingSchedules(exchange, from, to)
	var body proto.Message
	if resp != nil && resp.TradingSchedulesResponse != nil {
		body = resp.TradingSchedulesResponse
	}
	c.note("TradingSchedules", started, request, body, err)
	if err != nil || resp == nil {
		return nil, err
	}
	var out []portfolio.TradingDay
	for _, item := range resp.GetExchanges() {
		if item == nil {
			continue
		}
		name := item.GetExchange()
		if name == "" {
			name = exchange
		}
		for _, session := range item.GetDays() {
			if session == nil {
				continue
			}
			row := portfolio.TradingDay{Exchange: name, Trading: session.GetIsTradingDay()}
			if stamp := session.GetDate(); stamp != nil {
				row.Date = stamp.AsTime()
			}
			if stamp := session.GetStartTime(); stamp != nil {
				row.Start = stamp.AsTime()
			}
			if stamp := session.GetEndTime(); stamp != nil {
				row.End = stamp.AsTime()
			}
			out = append(out, row)
		}
	}
	return out, nil
}

func calendarExchange(value pb.RealExchange) string {
	switch value {
	case pb.RealExchange_REAL_EXCHANGE_MOEX:
		return "MOEX"
	case pb.RealExchange_REAL_EXCHANGE_RTS:
		return "SPB"
	default:
		return ""
	}
}

func riskSide(uid, side, role string, rate *pb.RiskRatesResponse_RiskRate) []portfolio.RiskRate {
	if rate == nil || rate.GetRiskLevelCode() == "" {
		return nil
	}
	value, ok := quoteDecimal(rate.GetValue())
	if !ok {
		return nil
	}
	return []portfolio.RiskRate{{
		InstrumentUID: uid,
		Side:          side,
		Role:          role,
		LevelCode:     rate.GetRiskLevelCode(),
		Value:         value,
	}}
}

func quoteDecimal(value *pb.Quotation) (decimal.Decimal, bool) {
	if value == nil {
		return decimal.Decimal{}, false
	}
	amount := decimal.NewFromInt(value.GetUnits()).Add(decimal.New(int64(value.GetNano()), -9))
	return amount, true
}
