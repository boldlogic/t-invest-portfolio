package tinvest

import (
	"context"
	"strings"
	"time"

	"github.com/shopspring/decimal"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
	pb "opensource.tbank.ru/invest/invest-go/proto"

	"t-invest-portfolio/internal/portfolio"
)

// Bond возвращает номинал и сроки облигации.
func (c *Client) Bond(ctx context.Context, uid string) (portfolio.Position, error) {
	started := time.Now()
	request := map[string]any{"uid": uid}
	if err := ctx.Err(); err != nil {
		c.note("BondBy", started, request, nil, err)
		return portfolio.Position{}, err
	}
	resp, err := c.instruments.BondByUid(uid)
	var body proto.Message
	if resp != nil && resp.BondResponse != nil {
		body = resp.BondResponse
	}
	c.note("BondBy", started, request, body, err)
	if err != nil || resp == nil {
		return portfolio.Position{}, err
	}
	instrument := resp.GetInstrument()
	if instrument == nil {
		return portfolio.Position{}, nil
	}
	nominal := moneyFrom(instrument.GetNominal())
	return portfolio.Position{
		InstrumentUID:  uid,
		Nominal:        nominal,
		NominalKnown:   instrument.GetNominal() != nil,
		Maturity:       stampTime(instrument.GetMaturityDate()),
		CouponsPerYear: instrument.GetCouponQuantityPerYear(),
		AssetUID:       instrument.GetAssetUid(),
	}, nil
}

// BondCoupons возвращает купоны за период.
func (c *Client) BondCoupons(ctx context.Context, uid string, from, to time.Time) ([]portfolio.Coupon, error) {
	started := time.Now()
	request := map[string]any{"uid": uid, "from": from.UTC().Format(time.RFC3339), "to": to.UTC().Format(time.RFC3339)}
	if err := ctx.Err(); err != nil {
		c.note("GetBondCoupons", started, request, nil, err)
		return nil, err
	}
	resp, err := c.instruments.GetBondCoupons(uid, from, to)
	var body proto.Message
	if resp != nil && resp.GetBondCouponsResponse != nil {
		body = resp.GetBondCouponsResponse
	}
	c.note("GetBondCoupons", started, request, body, err)
	if err != nil || resp == nil {
		return nil, err
	}
	out := make([]portfolio.Coupon, 0)
	for _, item := range resp.GetEvents() {
		if item == nil {
			continue
		}
		out = append(out, portfolio.Coupon{
			InstrumentUID: uid,
			PayDate:       stampTime(item.GetCouponDate()),
			Amount:        moneyFrom(item.GetPayOneBond()),
			Type:          item.GetCouponType().String(),
		})
	}
	return out, nil
}

// BondEvents возвращает события одного вида: call, maturity или conversion.
func (c *Client) BondEvents(ctx context.Context, uid, kind string, from, to time.Time) ([]portfolio.BondEvent, error) {
	started := time.Now()
	request := map[string]any{"uid": uid, "kind": kind}
	eventType, ok := bondEventType(kind)
	if !ok {
		err := errUnknownEvent(kind)
		c.note("GetBondEvents", started, request, nil, err)
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		c.note("GetBondEvents", started, request, nil, err)
		return nil, err
	}
	resp, err := c.instruments.GetBondEvents(uid, eventType, from, to)
	var body proto.Message
	if resp != nil && resp.GetBondEventsResponse != nil {
		body = resp.GetBondEventsResponse
	}
	c.note("GetBondEvents", started, request, body, err)
	if err != nil || resp == nil {
		return nil, err
	}
	out := make([]portfolio.BondEvent, 0)
	for _, item := range resp.GetEvents() {
		if item == nil {
			continue
		}
		out = append(out, portfolio.BondEvent{
			InstrumentUID: uid,
			Type:          kind,
			At:            stampTime(item.GetEventDate()),
			Pay:           moneyFrom(item.GetPayOneBond()),
		})
	}
	return out, nil
}

// Dividends возвращает календарь выплат за период.
func (c *Client) Dividends(ctx context.Context, uid string, from, to time.Time) ([]portfolio.Dividend, error) {
	started := time.Now()
	request := map[string]any{"uid": uid}
	if err := ctx.Err(); err != nil {
		c.note("GetDividends", started, request, nil, err)
		return nil, err
	}
	resp, err := c.instruments.GetDividents(uid, from, to)
	var body proto.Message
	if resp != nil && resp.GetDividendsResponse != nil {
		body = resp.GetDividendsResponse
	}
	c.note("GetDividends", started, request, body, err)
	if err != nil || resp == nil {
		return nil, err
	}
	out := make([]portfolio.Dividend, 0)
	for _, item := range resp.GetDividends() {
		if item == nil {
			continue
		}
		out = append(out, portfolio.Dividend{
			InstrumentUID: uid,
			RecordDate:    stampTime(item.GetRecordDate()),
			PaymentDate:   stampTime(item.GetPaymentDate()),
			Type:          item.GetDividendType(),
			Amount:        moneyFrom(item.GetDividendNet()),
		})
	}
	return out, nil
}

// TradingStatuses возвращает статусы торгов пачкой.
func (c *Client) TradingStatuses(ctx context.Context, instrumentIDs []string) ([]portfolio.TradingState, error) {
	started := time.Now()
	request := map[string]any{"instrument_ids": instrumentIDs}
	if err := ctx.Err(); err != nil {
		c.note("GetTradingStatuses", started, request, nil, err)
		return nil, err
	}
	if len(instrumentIDs) == 0 {
		return nil, nil
	}
	resp, err := c.market.GetTradingStatuses(instrumentIDs)
	var body proto.Message
	if resp != nil && resp.GetTradingStatusesResponse != nil {
		body = resp.GetTradingStatusesResponse
	}
	c.note("GetTradingStatuses", started, request, body, err)
	if err != nil || resp == nil {
		return nil, err
	}
	out := make([]portfolio.TradingState, 0)
	for _, item := range resp.GetTradingStatuses() {
		if item == nil {
			continue
		}
		out = append(out, portfolio.TradingState{
			InstrumentUID: item.GetInstrumentUid(),
			Figi:          item.GetFigi(),
			Status:        item.GetTradingStatus().String(),
		})
	}
	return out, nil
}

// Orders возвращает активные заявки счёта.
func (c *Client) Orders(ctx context.Context, accountID string) ([]portfolio.Order, error) {
	started := time.Now()
	request := map[string]any{"account_id": accountID}
	if err := c.ready(ctx, accountID); err != nil {
		c.note("GetOrders", started, request, nil, err)
		return nil, err
	}
	resp, err := c.orders.GetOrders(accountID, nil)
	var body proto.Message
	if resp != nil && resp.GetOrdersResponse != nil {
		body = resp.GetOrdersResponse
	}
	c.note("GetOrders", started, request, body, err)
	if err != nil || resp == nil {
		return nil, err
	}
	out := make([]portfolio.Order, 0)
	for _, item := range resp.GetOrders() {
		if item == nil {
			continue
		}
		out = append(out, portfolio.Order{
			ID:            item.GetOrderId(),
			InstrumentUID: item.GetInstrumentUid(),
			Figi:          item.GetFigi(),
			Ticker:        item.GetTicker(),
			Direction:     item.GetDirection().String(),
			LotsRequested: item.GetLotsRequested(),
			LotsExecuted:  item.GetLotsExecuted(),
			Price:         moneyFrom(item.GetInitialSecurityPrice()),
			Status:        item.GetExecutionReportStatus().String(),
			At:            stampTime(item.GetOrderDate()),
		})
	}
	return out, nil
}

// StopOrders возвращает стоп-заявки счёта.
func (c *Client) StopOrders(ctx context.Context, accountID string) ([]portfolio.StopOrder, error) {
	started := time.Now()
	request := map[string]any{"account_id": accountID}
	if err := c.ready(ctx, accountID); err != nil {
		c.note("GetStopOrders", started, request, nil, err)
		return nil, err
	}
	resp, err := c.stops.GetStopOrders(accountID)
	var body proto.Message
	if resp != nil && resp.GetStopOrdersResponse != nil {
		body = resp.GetStopOrdersResponse
	}
	c.note("GetStopOrders", started, request, body, err)
	if err != nil || resp == nil {
		return nil, err
	}
	out := make([]portfolio.StopOrder, 0)
	for _, item := range resp.GetStopOrders() {
		if item == nil {
			continue
		}
		out = append(out, portfolio.StopOrder{
			ID:            item.GetStopOrderId(),
			InstrumentUID: item.GetInstrumentUid(),
			Figi:          item.GetFigi(),
			Ticker:        item.GetTicker(),
			Direction:     item.GetDirection().String(),
			LotsRequested: item.GetLotsRequested(),
			Price:         moneyFrom(item.GetPrice()),
			StopPrice:     moneyFrom(item.GetStopPrice()),
			Status:        item.GetStatus().String(),
			At:            stampTime(item.GetCreateDate()),
		})
	}
	return out, nil
}

// Profile читает карточку пользователя и лимиты. Частичный ответ возможен вместе с ошибкой.
func (c *Client) Profile(ctx context.Context) (portfolio.Profile, error) {
	started := time.Now()
	if err := ctx.Err(); err != nil {
		c.note("GetInfo", started, nil, nil, err)
		return portfolio.Profile{}, err
	}
	info, infoErr := c.users.GetInfo()
	var infoBody proto.Message
	if info != nil && info.GetInfoResponse != nil {
		infoBody = info.GetInfoResponse
	}
	c.note("GetInfo", started, nil, infoBody, infoErr)

	tariffStarted := time.Now()
	tariff, tariffErr := c.users.GetUserTariff()
	var tariffBody proto.Message
	if tariff != nil && tariff.GetUserTariffResponse != nil {
		tariffBody = tariff.GetUserTariffResponse
	}
	c.note("GetUserTariff", tariffStarted, nil, tariffBody, tariffErr)

	var profile portfolio.Profile
	if info != nil {
		profile.UserID = info.GetUserId()
		profile.Premium = info.GetPremStatus()
		profile.Qualified = info.GetQualStatus()
		profile.Tariff = info.GetTariff()
		profile.Qualifications = append([]string{}, info.GetQualifiedForWorkWith()...)
	}
	if tariff != nil {
		for _, limit := range tariff.GetUnaryLimits() {
			if limit == nil {
				continue
			}
			profile.Limits = append(profile.Limits, portfolio.RateLimit{
				Kind:      "unary",
				PerMinute: limit.GetLimitPerMinute(),
				PerSecond: limit.GetLimitPerSecond(),
				Methods:   append([]string{}, limit.GetMethods()...),
			})
		}
		for _, limit := range tariff.GetStreamLimits() {
			if limit == nil {
				continue
			}
			profile.Limits = append(profile.Limits, portfolio.RateLimit{
				Kind:    "stream",
				Limit:   limit.GetLimit(),
				Open:    limit.GetOpen(),
				Methods: append([]string{}, limit.GetStreams()...),
			})
		}
	}
	if infoErr != nil {
		return profile, infoErr
	}
	return profile, tariffErr
}

func bondEventType(kind string) (pb.GetBondEventsRequest_EventType, bool) {
	switch kind {
	case "call":
		return pb.GetBondEventsRequest_EVENT_TYPE_CALL, true
	case "maturity":
		return pb.GetBondEventsRequest_EVENT_TYPE_MTY, true
	case "conversion":
		return pb.GetBondEventsRequest_EVENT_TYPE_CONV, true
	default:
		return 0, false
	}
}

type unknownEvent string

func errUnknownEvent(kind string) error { return unknownEvent(kind) }

func (e unknownEvent) Error() string {
	return "неизвестный тип события облигации: " + string(e)
}

func moneyFrom(value *pb.MoneyValue) portfolio.Money {
	if value == nil {
		return portfolio.Money{}
	}
	amount := decimal.NewFromInt(value.GetUnits()).Add(decimal.New(int64(value.GetNano()), -9))
	return portfolio.Money{Amount: amount, Currency: strings.ToUpper(value.GetCurrency())}
}

func stampTime(stamp *timestamppb.Timestamp) time.Time {
	if stamp == nil {
		return time.Time{}
	}
	return stamp.AsTime()
}
