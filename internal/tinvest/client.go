// Package tinvest — тонкая обёртка над invest-go.
// Торговые методы не вызываются.
package tinvest

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"
	"opensource.tbank.ru/invest/invest-go/investgo"
	pb "opensource.tbank.ru/invest/invest-go/proto"

	"t-invest-portfolio/internal/config"
	"t-invest-portfolio/internal/portfolio"
)

// sdkAccountPlaceholder не пустой специально.
// В investgo.NewClient пустой AccountId запускает GetSandboxAccounts
// и при отсутствии счетов вызывает OpenSandboxAccount.
// Чтение списка счетов идёт отдельно, через UsersService.
const sdkAccountPlaceholder = "portfolio-readonly"

// Client ходит в Users, Operations и Instruments.
type Client struct {
	raw         *investgo.Client
	users       *investgo.UsersServiceClient
	operations  *investgo.OperationsServiceClient
	instruments *investgo.InstrumentsServiceClient
	market      *investgo.MarketDataServiceClient
	orders      *investgo.OrdersServiceClient
	stops       *investgo.StopOrdersServiceClient
	dump        *Dump
}

// New поднимает gRPC-клиент. Отмена ctx останавливает вызовы SDK:
// сервисные методы используют контекст, переданный в NewClient.
func New(ctx context.Context, cfg config.Config) (*Client, error) {
	accountID := cfg.AccountID
	if accountID == "" {
		accountID = sdkAccountPlaceholder
	}

	raw, err := investgo.NewClient(ctx, investgo.Config{
		EndPoint:      cfg.Endpoint,
		Token:         cfg.Token,
		AppName:       cfg.AppName,
		AccountId:     accountID,
		TLSCACertFile: cfg.CAFile,
	}, stderrLogger{})
	if err != nil {
		return nil, fmt.Errorf("подключение к %s: %w", cfg.Endpoint, err)
	}

	return &Client{
		raw:         raw,
		users:       raw.NewUsersServiceClient(),
		operations:  raw.NewOperationsServiceClient(),
		instruments: raw.NewInstrumentsServiceClient(),
		market:      raw.NewMarketDataServiceClient(),
		orders:      raw.NewOrdersServiceClient(),
		stops:       raw.NewStopOrdersServiceClient(),
	}, nil
}

// Close закрывает соединение.
func (c *Client) Close() error {
	if c == nil || c.raw == nil {
		return nil
	}
	return c.raw.Stop()
}

// Accounts возвращает открытые счета, доступные токену.
func (c *Client) Accounts(ctx context.Context) ([]portfolio.AccountMeta, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	status := pb.AccountStatus_ACCOUNT_STATUS_OPEN
	started := time.Now()
	resp, err := c.users.GetAccounts(&status)
	var body proto.Message
	if resp != nil && resp.GetAccountsResponse != nil {
		body = resp.GetAccountsResponse
	}
	c.note("GetAccounts", started, map[string]any{"status": status.String()}, body, err)
	if err != nil {
		return nil, fmt.Errorf("список счетов: %w", err)
	}

	raw := resp.GetAccounts()
	out := make([]portfolio.AccountMeta, 0, len(raw))
	for _, account := range raw {
		out = append(out, portfolio.AccountMeta{
			ID:   account.GetId(),
			Name: account.GetName(),
			Type: account.GetType().String(),
		})
	}
	return out, nil
}

// Portfolio запрашивает оценку счёта в рублях.
func (c *Client) Portfolio(ctx context.Context, accountID string) (*pb.PortfolioResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if accountID == "" || accountID == sdkAccountPlaceholder {
		return nil, fmt.Errorf("пустой идентификатор счёта")
	}

	started := time.Now()
	resp, err := c.operations.GetPortfolio(accountID, pb.PortfolioRequest_RUB)
	var body proto.Message
	if resp != nil && resp.PortfolioResponse != nil {
		body = resp.PortfolioResponse
	}
	c.note("GetPortfolio", started, map[string]any{"account_id": accountID, "currency": "RUB"}, body, err)
	if err != nil {
		return nil, fmt.Errorf("портфель счёта %s: %w", accountID, err)
	}
	if resp == nil || resp.PortfolioResponse == nil {
		return nil, fmt.Errorf("портфель счёта %s: пустой ответ", accountID)
	}
	return resp.PortfolioResponse, nil
}

// Instrument возвращает тикер и название по uid.
// Карточка актива по-прежнему резолвит asset_uid этим вызовом.
func (c *Client) Instrument(ctx context.Context, uid string) (portfolio.InstrumentMeta, error) {
	if err := ctx.Err(); err != nil {
		return portfolio.InstrumentMeta{}, err
	}

	started := time.Now()
	resp, err := c.instruments.InstrumentByUid(uid)
	var body proto.Message
	if resp != nil && resp.InstrumentResponse != nil {
		body = resp.InstrumentResponse
	}
	c.note("InstrumentByUid", started, map[string]any{"uid": uid}, body, err)
	if err != nil {
		return portfolio.InstrumentMeta{}, fmt.Errorf("инструмент %s: %w", uid, err)
	}
	instrument := resp.GetInstrument()
	if instrument == nil {
		return portfolio.InstrumentMeta{}, fmt.Errorf("инструмент %s: пустой ответ", uid)
	}
	return instrumentMeta(instrument), nil
}

// InstrumentByTicker возвращает карточку инструмента по тикеру и class_code площадки T-Invest.
func (c *Client) InstrumentByTicker(ctx context.Context, ticker, classCode string) (portfolio.InstrumentMeta, error) {
	ticker = strings.TrimSpace(ticker)
	classCode = strings.TrimSpace(classCode)
	started := time.Now()
	request := map[string]any{"ticker": ticker, "class_code": classCode}
	if err := ctx.Err(); err != nil {
		c.note("InstrumentByTicker", started, request, nil, err)
		return portfolio.InstrumentMeta{}, err
	}
	if ticker == "" || classCode == "" {
		err := fmt.Errorf("для инструмента нужны тикер и class_code")
		c.note("InstrumentByTicker", started, request, nil, err)
		return portfolio.InstrumentMeta{}, err
	}
	resp, err := c.instruments.InstrumentByTicker(ticker, classCode)
	var body proto.Message
	if resp != nil && resp.InstrumentResponse != nil {
		body = resp.InstrumentResponse
	}
	c.note("InstrumentByTicker", started, request, body, err)
	if err != nil {
		return portfolio.InstrumentMeta{}, fmt.Errorf("инструмент %s %s: %w", ticker, classCode, err)
	}
	instrument := resp.GetInstrument()
	if instrument == nil {
		return portfolio.InstrumentMeta{}, fmt.Errorf("инструмент %s %s: пустой ответ", ticker, classCode)
	}
	return instrumentMeta(instrument), nil
}

func instrumentMeta(instrument *pb.Instrument) portfolio.InstrumentMeta {
	return portfolio.InstrumentMeta{
		Ticker:   instrument.GetTicker(),
		Name:     instrument.GetName(),
		AssetUID: instrument.GetAssetUid(),
	}
}

type stderrLogger struct{}

func (stderrLogger) Infof(template string, args ...any) {
	log.Printf(template, args...)
}

func (stderrLogger) Errorf(template string, args ...any) {
	log.Printf(template, args...)
}

func (stderrLogger) Fatalf(template string, args ...any) {
	log.New(os.Stderr, "", log.LstdFlags).Fatalf(template, args...)
}
