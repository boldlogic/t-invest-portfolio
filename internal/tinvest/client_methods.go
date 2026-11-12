package tinvest

import (
	"context"
	"fmt"
	"time"

	"github.com/shopspring/decimal"
	"google.golang.org/protobuf/proto"
	"opensource.tbank.ru/invest/invest-go/investgo"
	pb "opensource.tbank.ru/invest/invest-go/proto"

	"t-invest-portfolio/internal/portfolio"
)

const (
	operationPageLimit = 1000
	reportWait         = 30 * time.Second
	reportPoll         = time.Second
)

func (c *Client) Positions(ctx context.Context, accountID string) (*pb.PositionsResponse, error) {
	started := time.Now()
	request := map[string]any{"account_id": accountID}
	if err := c.ready(ctx, accountID); err != nil {
		c.note("GetPositions", started, request, nil, err)
		return nil, err
	}
	resp, err := c.operations.GetPositions(accountID)
	body := positionsBody(resp)
	c.note("GetPositions", started, request, body, err)
	if err != nil {
		return nil, fmt.Errorf("остатки счёта %s: %w", accountID, err)
	}
	if body == nil {
		err = fmt.Errorf("остатки счёта %s: пустой ответ", accountID)
		c.note("GetPositions", started, request, nil, err)
		return nil, err
	}
	return resp.PositionsResponse, nil
}

func (c *Client) WithdrawLimits(ctx context.Context, accountID string) (*pb.WithdrawLimitsResponse, error) {
	started := time.Now()
	request := map[string]any{"account_id": accountID}
	if err := c.ready(ctx, accountID); err != nil {
		c.note("GetWithdrawLimits", started, request, nil, err)
		return nil, err
	}
	resp, err := c.operations.GetWithdrawLimits(accountID)
	var body proto.Message
	if resp != nil && resp.WithdrawLimitsResponse != nil {
		body = resp.WithdrawLimitsResponse
	}
	c.note("GetWithdrawLimits", started, request, body, err)
	if err != nil {
		return nil, fmt.Errorf("лимиты вывода счёта %s: %w", accountID, err)
	}
	if body == nil {
		err = fmt.Errorf("лимиты вывода счёта %s: пустой ответ", accountID)
		c.note("GetWithdrawLimits", started, request, nil, err)
		return nil, err
	}
	return resp.WithdrawLimitsResponse, nil
}

func (c *Client) Margin(ctx context.Context, accountID string) (*pb.GetMarginAttributesResponse, error) {
	started := time.Now()
	request := map[string]any{"account_id": accountID}
	if err := c.ready(ctx, accountID); err != nil {
		c.note("GetMarginAttributes", started, request, nil, err)
		return nil, err
	}
	resp, err := c.users.GetMarginAttributes(accountID)
	var body proto.Message
	if resp != nil && resp.GetMarginAttributesResponse != nil {
		body = resp.GetMarginAttributesResponse
	}
	c.note("GetMarginAttributes", started, request, body, err)
	if err != nil {
		return nil, err
	}
	if body == nil {
		err = fmt.Errorf("пустой ответ")
		c.note("GetMarginAttributes", started, request, nil, err)
		return nil, err
	}
	return resp.GetMarginAttributesResponse, nil
}

func (c *Client) OperationPage(ctx context.Context, accountID string, from, to time.Time, cursor string) (*pb.GetOperationsByCursorResponse, error) {
	started := time.Now()
	request := map[string]any{
		"account_id": accountID,
		"from":       from.UTC().Format(time.RFC3339),
		"to":         to.UTC().Format(time.RFC3339),
		"cursor":     cursor,
		"state":      pb.OperationState_OPERATION_STATE_EXECUTED.String(),
	}
	if err := c.ready(ctx, accountID); err != nil {
		c.note("GetOperationsByCursor", started, request, nil, err)
		return nil, err
	}
	resp, err := c.operations.GetOperationsByCursor(&investgo.GetOperationsByCursorRequest{
		AccountId: accountID,
		From:      from,
		To:        to,
		Cursor:    cursor,
		Limit:     operationPageLimit,
		State:     pb.OperationState_OPERATION_STATE_EXECUTED,
	})
	var body proto.Message
	if resp != nil && resp.GetOperationsByCursorResponse != nil {
		body = resp.GetOperationsByCursorResponse
	}
	c.note("GetOperationsByCursor", started, request, body, err)
	if err != nil {
		return nil, fmt.Errorf("операции счёта %s: %w", accountID, err)
	}
	if body == nil {
		return &pb.GetOperationsByCursorResponse{}, nil
	}
	return resp.GetOperationsByCursorResponse, nil
}

func (c *Client) BrokerReport(ctx context.Context, accountID string, from, to time.Time) ([]*pb.BrokerReport, error) {
	started := time.Now()
	request := map[string]any{
		"account_id": accountID,
		"from":       from.UTC().Format(time.RFC3339),
		"to":         to.UTC().Format(time.RFC3339),
	}
	if err := c.ready(ctx, accountID); err != nil {
		c.note("GenerateBrokerReport", started, request, nil, err)
		return nil, err
	}
	generated, err := c.operations.GenerateBrokerReport(accountID, from, to)
	var generatedBody proto.Message
	if generated != nil && generated.GenerateBrokerReportResponse != nil {
		generatedBody = generated.GenerateBrokerReportResponse
	}
	c.note("GenerateBrokerReport", started, request, generatedBody, err)
	if err != nil {
		return nil, err
	}
	if generated == nil || generated.GetTaskId() == "" {
		err = fmt.Errorf("пустой идентификатор задачи")
		c.note("GenerateBrokerReport", started, request, generatedBody, err)
		return nil, err
	}

	deadline := time.Now().Add(reportWait)
	taskID := generated.GetTaskId()
	reportRequest := map[string]any{"account_id": accountID, "task_id": taskID}
	for {
		pageStarted := time.Now()
		rows, ready, err := c.reportPages(taskID)
		if err != nil {
			c.note("GetBrokerReport", pageStarted, reportRequest, nil, err)
			return nil, err
		}
		if ready {
			c.note("GetBrokerReport", pageStarted, reportRequest, brokerMessages(rows), nil)
			return rows, nil
		}
		if !time.Now().Before(deadline) {
			c.note("GetBrokerReport", pageStarted, reportRequest, map[string]any{"timeout": true, "wait": reportWait.String()}, nil)
			return nil, nil
		}
		timer := time.NewTimer(reportPoll)
		select {
		case <-ctx.Done():
			timer.Stop()
			c.note("GetBrokerReport", pageStarted, reportRequest, nil, ctx.Err())
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func (c *Client) reportPages(taskID string) ([]*pb.BrokerReport, bool, error) {
	first, err := c.operations.GetBrokerReport(taskID, 0)
	if err != nil {
		return nil, false, err
	}
	if first == nil || first.GetBrokerReportResponse == nil {
		return nil, false, nil
	}
	rows := append([]*pb.BrokerReport{}, first.GetBrokerReport()...)
	pages := first.GetPagesCount()
	if len(rows) == 0 && pages == 0 {
		return nil, false, nil
	}
	for page := int32(1); page < pages; page++ {
		next, err := c.operations.GetBrokerReport(taskID, page)
		if err != nil {
			return nil, false, err
		}
		if next != nil {
			rows = append(rows, next.GetBrokerReport()...)
		}
	}
	return rows, true, nil
}

func (c *Client) LastPrices(ctx context.Context, instrumentIDs []string) ([]portfolio.MarketQuote, error) {
	started := time.Now()
	request := map[string]any{"instrument_ids": instrumentIDs}
	if err := ctx.Err(); err != nil {
		c.note("GetLastPrices", started, request, nil, err)
		return nil, err
	}
	if len(instrumentIDs) == 0 {
		c.note("GetLastPrices", started, request, map[string]any{"skipped": "нет бумаг"}, nil)
		return nil, nil
	}
	resp, err := c.market.GetLastPrices(instrumentIDs)
	var body proto.Message
	if resp != nil && resp.GetLastPricesResponse != nil {
		body = resp.GetLastPricesResponse
	}
	c.note("GetLastPrices", started, request, body, err)
	if err != nil {
		return nil, err
	}
	if body == nil {
		return nil, nil
	}
	return quotesFromLast(resp.GetLastPrices()), nil
}

func (c *Client) ClosePrices(ctx context.Context, instrumentIDs []string) ([]portfolio.MarketQuote, error) {
	started := time.Now()
	request := map[string]any{"instrument_ids": instrumentIDs}
	if err := ctx.Err(); err != nil {
		c.note("GetClosePrices", started, request, nil, err)
		return nil, err
	}
	if len(instrumentIDs) == 0 {
		c.note("GetClosePrices", started, request, map[string]any{"skipped": "нет бумаг"}, nil)
		return nil, nil
	}
	resp, err := c.market.GetClosePrices(instrumentIDs)
	var body proto.Message
	if resp != nil && resp.GetClosePricesResponse != nil {
		body = resp.GetClosePricesResponse
	}
	c.note("GetClosePrices", started, request, body, err)
	if err != nil {
		return nil, err
	}
	if body == nil {
		return nil, nil
	}
	return quotesFromClose(resp.GetClosePrices()), nil
}

func positionsBody(resp *investgo.PositionsResponse) proto.Message {
	if resp == nil || resp.PositionsResponse == nil {
		return nil
	}
	return resp.PositionsResponse
}

func brokerMessages(rows []*pb.BrokerReport) []proto.Message {
	out := make([]proto.Message, 0, len(rows))
	for _, row := range rows {
		if row != nil {
			out = append(out, row)
		}
	}
	return out
}

func (c *Client) ready(ctx context.Context, accountID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if accountID == "" || accountID == sdkAccountPlaceholder {
		return fmt.Errorf("пустой идентификатор счёта")
	}
	return nil
}

func quotesFromLast(items []*pb.LastPrice) []portfolio.MarketQuote {
	out := make([]portfolio.MarketQuote, 0, len(items))
	for _, item := range items {
		if item == nil || item.GetPrice() == nil {
			continue
		}
		out = append(out, portfolio.MarketQuote{
			InstrumentUID: item.GetInstrumentUid(),
			Figi:          item.GetFigi(),
			Value:         quotationAmount(item.GetPrice()),
		})
	}
	return out
}

func quotesFromClose(items []*pb.InstrumentClosePriceResponse) []portfolio.MarketQuote {
	out := make([]portfolio.MarketQuote, 0, len(items))
	for _, item := range items {
		if item == nil || item.GetPrice() == nil {
			continue
		}
		out = append(out, portfolio.MarketQuote{
			InstrumentUID: item.GetInstrumentUid(),
			Figi:          item.GetFigi(),
			Value:         quotationAmount(item.GetPrice()),
		})
	}
	return out
}

func quotationAmount(value *pb.Quotation) decimal.Decimal {
	return decimal.NewFromInt(value.GetUnits()).Add(decimal.New(int64(value.GetNano()), -9))
}
