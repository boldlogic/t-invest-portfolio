package worker

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	pb "opensource.tbank.ru/invest/invest-go/proto"
	"t-invest-portfolio/internal/config"
	"t-invest-portfolio/internal/portfolio"
	"t-invest-portfolio/internal/store"
)

type Source interface {
	Accounts(context.Context) ([]portfolio.AccountMeta, error)
	Profile(context.Context) (portfolio.Profile, error)
	Portfolio(context.Context, string) (*pb.PortfolioResponse, error)
	Positions(context.Context, string) (*pb.PositionsResponse, error)
	WithdrawLimits(context.Context, string) (*pb.WithdrawLimitsResponse, error)
	Margin(context.Context, string) (*pb.GetMarginAttributesResponse, error)
	OperationPage(context.Context, string, time.Time, time.Time, string) (*pb.GetOperationsByCursorResponse, error)
	Orders(context.Context, string) ([]portfolio.Order, error)
	StopOrders(context.Context, string) ([]portfolio.StopOrder, error)
	LastPrices(context.Context, []string) ([]portfolio.MarketQuote, error)
	ClosePrices(context.Context, []string) ([]portfolio.MarketQuote, error)
	TradingStatuses(context.Context, []string) ([]portfolio.TradingState, error)
	Instrument(context.Context, string) (portfolio.InstrumentMeta, error)
	InstrumentByTicker(context.Context, string, string) (portfolio.InstrumentMeta, error)
	Share(context.Context, string) (portfolio.SharePassport, error)
	Etf(context.Context, string) (portfolio.EtfPassport, error)
	Bond(context.Context, string) (portfolio.Position, error)
	BondCoupons(context.Context, string, time.Time, time.Time) ([]portfolio.Coupon, error)
	BondEvents(context.Context, string, string, time.Time, time.Time) ([]portfolio.BondEvent, error)
	Dividends(context.Context, string, time.Time, time.Time) ([]portfolio.Dividend, error)
	AccruedInterests(context.Context, string, time.Time, time.Time) ([]portfolio.AccruedInterest, error)
	RiskRates(context.Context, []string) ([]portfolio.RiskRate, []portfolio.RiskProblem, error)
	TradingSchedule(context.Context, string, time.Time) ([]portfolio.TradingDay, error)
	AssetFundamentals(context.Context, []string) ([]portfolio.Fundamental, error)
	AssetBy(context.Context, string) (portfolio.AssetCard, error)
	InsiderDeals(context.Context, string) ([]portfolio.InsiderDeal, error)
	News(context.Context) ([]portfolio.NewsItem, error)
	FindInstrument(context.Context, string) ([]portfolio.InstrumentHit, error)
	Countries(context.Context) ([]portfolio.Country, error)
	Brands(context.Context) ([]portfolio.Brand, error)
}

type Sink interface {
	EnsureSubscription(context.Context, portfolio.Subscription) error
	SetSubscriptionAsset(context.Context, string, string) error
	ListSubscriptions(context.Context) ([]portfolio.Subscription, error)
	BackfillSubscriptions(context.Context) error
	ReplaceCurrent(context.Context, string, string, []store.Record) error
	UpsertHistory(context.Context, string, string, string, time.Time, any) error
	SavePortfolioPositions(context.Context, string, []portfolio.Position) error
	GetCurrent(context.Context, string, string, string) (json.RawMessage, error)
}

type Engine struct {
	Source Source
	Store  Sink
	Config config.Config
	Logger *log.Logger

	gate            *QuotaGate
	mu              sync.RWMutex
	accounts        []portfolio.AccountMeta
	positions       map[string][]portfolio.Position
	assetUIDs       map[string]string
	exchanges       map[string]bool
	profile         portfolio.Profile
	portfolioLoaded bool
}

var ErrAccountNotFound = fmt.Errorf("указанный счёт не найден")

func New(src Source, sink Sink, cfg config.Config, logger *log.Logger) *Engine {
	return &Engine{Source: src, Store: sink, Config: cfg, Logger: logger, positions: make(map[string][]portfolio.Position), assetUIDs: make(map[string]string), exchanges: make(map[string]bool)}
}

func (e *Engine) Run(ctx context.Context) error {
	if e.Source == nil || e.Store == nil {
		return fmt.Errorf("source and store are required")
	}
	var profile portfolio.Profile
	if err := retryStartup(ctx, e.Logger, "получение лимитов GetUserTariff", func() error {
		var err error
		profile, err = e.Source.Profile(ctx)
		return err
	}); err != nil {
		return err
	}
	e.profile = profile
	e.gate = NewQuotaGate(profile)
	if err := e.saveOne(ctx, "profile", "user", "current", time.Now().UTC(), profile); err != nil {
		return fmt.Errorf("сохранение профиля и лимитов: %w", err)
	}
	if err := retryStartup(ctx, e.Logger, "первое чтение счетов", func() error { return e.refreshAccounts(ctx) }); err != nil {
		return err
	}
	if err := e.pollPortfolios(ctx); err != nil && e.Logger != nil {
		e.Logger.Printf("первое чтение портфелей: %v", err)
	}
	tasks := e.tasks()
	return (&Scheduler{Tasks: tasks, Logger: e.Logger, OnResult: e.recordTaskResult}).Run(ctx)
}

func retryStartup(ctx context.Context, logger *log.Logger, name string, run func() error) error {
	delay := time.Second
	for {
		if err := run(); err == nil {
			return nil
		} else if errors.Is(err, ErrAccountNotFound) || ctx.Err() != nil {
			return err
		} else if logger != nil {
			logger.Printf("%s: %v; повтор через %s", name, err, delay)
		}
		if !wait(ctx, delay) {
			return ctx.Err()
		}
		delay = min(delay*2, 5*time.Minute)
	}
}

type pollStatus struct {
	Name          string    `json:"name"`
	Method        string    `json:"method"`
	LastAttemptAt time.Time `json:"last_attempt_at"`
	LastSuccessAt time.Time `json:"last_success_at,omitempty"`
	LastError     string    `json:"last_error,omitempty"`
}

func (e *Engine) recordTaskResult(ctx context.Context, task Task, result error) {
	status := pollStatus{Name: task.Name, Method: task.Method, LastAttemptAt: time.Now().UTC()}
	if result != nil {
		if payload, err := e.Store.GetCurrent(ctx, "poll_status", "worker", task.Name); err == nil {
			var previous pollStatus
			if json.Unmarshal(payload, &previous) == nil {
				status.LastSuccessAt = previous.LastSuccessAt
			}
		} else if err != sql.ErrNoRows && e.Logger != nil {
			e.Logger.Printf("статус задачи %s: чтение предыдущего результата: %v", task.Name, err)
		}
		status.LastError = result.Error()
	} else {
		status.LastSuccessAt = status.LastAttemptAt
	}
	if err := e.Store.ReplaceCurrent(ctx, "poll_status", "worker", []store.Record{{Key: task.Name, At: status.LastAttemptAt, Payload: status}}); err != nil && e.Logger != nil {
		e.Logger.Printf("статус задачи %s: запись результата: %v", task.Name, err)
	}
}

func (e *Engine) tasks() []Task {
	interval := func(key string, fallback time.Duration) time.Duration {
		if value := e.Config.PollIntervals[key]; value > 0 {
			return value
		}
		return fallback
	}
	return []Task{
		{Name: "счета", Method: "GetAccounts", Interval: interval("accounts", 5*time.Minute), Run: e.refreshAccounts},
		{Name: "профиль и квоты", Method: "GetUserTariff", Interval: interval("profile", 24*time.Hour), InitialDelay: interval("profile", 24*time.Hour), Run: e.refreshProfile},
		{Name: "портфель", Method: "GetPortfolio", Interval: interval("portfolio", time.Minute), Run: e.pollPortfolios},
		{Name: "остатки", Method: "GetPositions", Interval: interval("positions", time.Minute), Run: e.pollPositions},
		{Name: "лимиты вывода", Method: "GetWithdrawLimits", Interval: interval("withdraw_limits", time.Minute), Run: e.pollWithdraw},
		{Name: "маржа", Method: "GetMarginAttributes", Interval: interval("margin", time.Minute), Run: e.pollMargin},
		{Name: "операции", Method: "GetOperationsByCursor", Interval: interval("operations", time.Minute), Run: e.pollOperations},
		{Name: "заявки", Method: "GetOrders", Interval: interval("orders", time.Minute), Run: e.pollOrders},
		{Name: "стоп-заявки", Method: "GetStopOrders", Interval: interval("stop_orders", time.Minute), Run: e.pollStopOrders},
		{Name: "последние цены", Method: "GetLastPrices", Interval: interval("last_prices", time.Minute), Run: e.pollLastPrices},
		{Name: "цены закрытия", Method: "GetClosePrices", Interval: interval("close_prices", time.Minute), Run: e.pollClosePrices},
		{Name: "статусы торгов", Method: "GetTradingStatuses", Interval: interval("trading_statuses", time.Minute), Run: e.pollTradingStatuses},
		{Name: "справочные данные инструментов", Method: "InstrumentByTicker", Interval: interval("instrument_metadata", 24*time.Hour), Run: e.pollMetadata},
		{Name: "паспорта акций", Method: "ShareBy", Interval: interval("share_passport", 24*time.Hour), Run: e.pollSharePassports},
		{Name: "паспорта фондов", Method: "EtfBy", Interval: interval("etf_passport", 24*time.Hour), Run: e.pollEtfPassports},
		{Name: "паспорта облигаций", Method: "BondBy", Interval: interval("bond_passport", 24*time.Hour), Run: e.pollBondPassports},
		{Name: "купоны облигаций", Method: "GetBondCoupons", Interval: interval("bond_coupons", 24*time.Hour), Run: e.pollCoupons},
		{Name: "события облигаций", Method: "GetBondEvents", Interval: interval("bond_events", 24*time.Hour), Run: e.pollBondEvents},
		{Name: "дивиденды", Method: "GetDividends", Interval: interval("dividends", 24*time.Hour), Run: e.pollDividends},
		{Name: "график НКД", Method: "GetAccruedInterests", Interval: interval("accrued_interests", 24*time.Hour), Run: e.pollAccrued},
		{Name: "ставки риска", Method: "GetRiskRates", Interval: interval("risk_rates", 24*time.Hour), Run: e.pollRiskRates},
		{Name: "расписание торгов", Method: "TradingSchedules", Interval: interval("trading_schedule", 24*time.Hour), Run: e.pollTradingSchedule},
		{Name: "фундаментальные показатели", Method: "GetAssetFundamentals", Interval: interval("fundamentals", 24*time.Hour), Run: e.pollFundamentals},
		{Name: "сделки инсайдеров", Method: "GetInsiderDeals", Interval: interval("insider_deals", 24*time.Hour), Run: e.pollInsiders},
		{Name: "новости", Method: "GetNews", Interval: interval("news", 24*time.Hour), Run: e.pollNews},
		{Name: "поиск инструментов", Method: "FindInstrument", Interval: interval("instrument_search", 24*time.Hour), Run: e.pollInstrumentSearch},
		{Name: "страны", Method: "GetCountries", Interval: interval("countries", 7*24*time.Hour), Run: e.pollCountries},
		{Name: "бренды", Method: "GetBrands", Interval: interval("brands", 7*24*time.Hour), Run: e.pollBrands},
		{Name: "карточка актива", Method: "GetAssetBy", Interval: interval("asset_card", 24*time.Hour), Run: e.pollAssetCards},
	}
}

func (e *Engine) refreshProfile(ctx context.Context) error {
	if err := e.wait(ctx, "GetInfo"); err != nil {
		return err
	}
	if err := e.wait(ctx, "GetUserTariff"); err != nil {
		return err
	}
	profile, err := e.Source.Profile(ctx)
	if err != nil {
		return err
	}
	e.mu.Lock()
	e.profile = profile
	e.mu.Unlock()
	e.gate.Update(profile)
	return e.saveOne(ctx, "profile", "user", "current", time.Now().UTC(), profile)
}

func (e *Engine) refreshAccounts(ctx context.Context) error {
	if err := e.wait(ctx, "GetAccounts"); err != nil {
		return err
	}
	accounts, err := e.Source.Accounts(ctx)
	if err != nil {
		return err
	}
	sort.Slice(accounts, func(i, j int) bool { return accounts[i].ID < accounts[j].ID })
	selected := make([]portfolio.AccountMeta, 0, len(accounts))
	rows := make([]store.Record, 0, len(accounts))
	for _, account := range accounts {
		if e.Config.AccountID != "" && account.ID != e.Config.AccountID {
			continue
		}
		selected = append(selected, account)
		rows = append(rows, store.Record{Key: account.ID, At: time.Now().UTC(), Payload: account})
	}
	if e.Config.AccountID != "" && len(rows) == 0 {
		return fmt.Errorf("%w: %s", ErrAccountNotFound, e.Config.AccountID)
	}
	if err := e.Store.ReplaceCurrent(ctx, "account", "open", rows); err != nil {
		return err
	}
	e.mu.Lock()
	for _, account := range selected {
		if _, ok := e.positions[account.ID]; !ok {
			e.portfolioLoaded = false
			break
		}
	}
	active := make(map[string]bool, len(selected))
	for _, account := range selected {
		active[account.ID] = true
	}
	for id := range e.positions {
		if !active[id] {
			delete(e.positions, id)
		}
	}
	e.accounts = selected
	e.mu.Unlock()
	return nil
}

func (e *Engine) pollPortfolios(ctx context.Context) error {
	var failures []string
	for _, account := range e.accountList() {
		if account.Type == portfolio.AccountTypeInvestBox {
			continue
		}
		if err := e.wait(ctx, "GetPortfolio"); err != nil {
			return err
		}
		response, err := e.Source.Portfolio(ctx, account.ID)
		if err != nil {
			failures = append(failures, account.ID+": "+err.Error())
			continue
		}
		mapped, err := portfolio.MapPortfolio(account, response)
		if err != nil {
			failures = append(failures, account.ID+": "+err.Error())
			continue
		}
		at := time.Now().UTC()
		if err := e.saveOne(ctx, "portfolio", account.ID, "totals", at, mapped.Totals); err != nil {
			failures = append(failures, account.ID+": "+err.Error())
			continue
		}
		positionRows := make([]store.Record, 0, len(mapped.Positions))
		for _, position := range mapped.Positions {
			key := position.InstrumentUID
			if key == "" {
				key = position.Figi
			}
			if key != "" {
				positionRows = append(positionRows, store.Record{Key: key, At: at, Payload: position})
			}
		}
		if err := e.Store.ReplaceCurrent(ctx, "position", account.ID, positionRows); err != nil {
			failures = append(failures, account.ID+": "+err.Error())
			continue
		}
		virtualRows := make([]store.Record, 0, len(mapped.VirtualPositions))
		for _, position := range mapped.VirtualPositions {
			key := position.InstrumentUID
			if key == "" {
				key = position.Figi
			}
			if key != "" {
				virtualRows = append(virtualRows, store.Record{Key: key, At: at, Payload: position})
			}
		}
		if err := e.Store.ReplaceCurrent(ctx, "virtual_position", account.ID, virtualRows); err != nil {
			failures = append(failures, account.ID+": "+err.Error())
			continue
		}
		e.mu.Lock()
		e.positions[account.ID] = mapped.Positions
		e.mu.Unlock()
		if err := e.Store.SavePortfolioPositions(ctx, account.ID, mapped.Positions); err != nil {
			failures = append(failures, account.ID+": shared positions: "+err.Error())
		}
		for _, position := range mapped.Positions {
			item, ok := portfolio.SubscriptionFromPosition(position)
			if !ok {
				continue
			}
			if err := e.Store.EnsureSubscription(ctx, item); err != nil {
				failures = append(failures, account.ID+": подписка: "+err.Error())
			}
		}
	}
	if len(failures) == 0 {
		e.mu.Lock()
		e.portfolioLoaded = true
		e.mu.Unlock()
	}
	return errorsJoin(failures)
}

func (e *Engine) pollPositions(ctx context.Context) error {
	return e.forAccounts(ctx, "GetPositions", "holdings", func(ctx context.Context, account portfolio.AccountMeta) ([]store.Record, error) {
		response, err := e.Source.Positions(ctx, account.ID)
		if err != nil {
			return nil, err
		}
		mapped := portfolio.Account{ID: account.ID}
		portfolio.ApplyPositions(&mapped, response)
		rows := make([]store.Record, 0, len(mapped.Holdings)+len(mapped.PositionMoney)+len(mapped.PositionBlocked))
		for _, holding := range mapped.Holdings {
			rows = append(rows, store.Record{Key: holding.Kind + ":" + holding.InstrumentUID, At: time.Now().UTC(), Payload: holding})
		}
		for i, value := range mapped.PositionMoney {
			rows = append(rows, store.Record{Key: fmt.Sprintf("money:%d", i), At: time.Now().UTC(), Payload: value})
		}
		for i, value := range mapped.PositionBlocked {
			rows = append(rows, store.Record{Key: fmt.Sprintf("blocked:%d", i), At: time.Now().UTC(), Payload: value})
		}
		return rows, nil
	})
}

func (e *Engine) pollWithdraw(ctx context.Context) error {
	return e.forAccounts(ctx, "GetWithdrawLimits", "withdraw_limits", func(ctx context.Context, account portfolio.AccountMeta) ([]store.Record, error) {
		value, err := e.Source.WithdrawLimits(ctx, account.ID)
		if err != nil {
			return nil, err
		}
		return []store.Record{{Key: "current", At: time.Now().UTC(), Payload: value}}, nil
	})
}

func (e *Engine) pollMargin(ctx context.Context) error {
	return e.forAccounts(ctx, "GetMarginAttributes", "margin", func(ctx context.Context, account portfolio.AccountMeta) ([]store.Record, error) {
		value, err := e.Source.Margin(ctx, account.ID)
		if err != nil {
			return nil, err
		}
		return []store.Record{{Key: "current", At: time.Now().UTC(), Payload: value}}, nil
	})
}

func (e *Engine) pollOrders(ctx context.Context) error {
	return e.forAccounts(ctx, "GetOrders", "orders", func(ctx context.Context, account portfolio.AccountMeta) ([]store.Record, error) {
		values, err := e.Source.Orders(ctx, account.ID)
		if err != nil {
			return nil, err
		}
		rows := make([]store.Record, 0, len(values))
		for _, value := range values {
			if value.ID != "" {
				rows = append(rows, store.Record{Key: value.ID, At: time.Now().UTC(), Payload: value})
			}
		}
		return rows, nil
	})
}

func (e *Engine) pollStopOrders(ctx context.Context) error {
	return e.forAccounts(ctx, "GetStopOrders", "stop_orders", func(ctx context.Context, account portfolio.AccountMeta) ([]store.Record, error) {
		values, err := e.Source.StopOrders(ctx, account.ID)
		if err != nil {
			return nil, err
		}
		rows := make([]store.Record, 0, len(values))
		for _, value := range values {
			if value.ID != "" {
				rows = append(rows, store.Record{Key: value.ID, At: time.Now().UTC(), Payload: value})
			}
		}
		return rows, nil
	})
}

func (e *Engine) pollOperations(ctx context.Context) error {
	var failures []string
	to := time.Now().UTC()
	for _, account := range e.accountList() {
		if account.Type == portfolio.AccountTypeInvestBox {
			continue
		}
		var watermark []byte
		if payload, err := e.Store.GetCurrent(ctx, "operation_watermark", account.ID, "complete"); err == nil {
			watermark = payload
		} else if err != sql.ErrNoRows {
			failures = append(failures, account.ID+": чтение курсора: "+err.Error())
			continue
		}
		from, err := operationFrom(watermark)
		if err != nil {
			failures = append(failures, account.ID+": некорректный watermark операций")
			continue
		}
		cursor := ""
		complete := true
		for {
			if err := e.wait(ctx, "GetOperationsByCursor"); err != nil {
				return err
			}
			response, err := e.Source.OperationPage(ctx, account.ID, from, to, cursor)
			if err != nil {
				failures = append(failures, account.ID+": "+err.Error())
				complete = false
				break
			}
			if response == nil {
				break
			}
			for _, item := range response.GetItems() {
				operation := portfolio.MapOperation(item)
				key := operationKey(account.ID, operation)
				if key == "" {
					continue
				}
				if err := e.Store.UpsertHistory(ctx, "operation", account.ID, key, operation.At, operation); err != nil {
					failures = append(failures, account.ID+": "+err.Error())
					complete = false
				}
			}
			next, continuePaging, valid := operationNextCursor(response.GetHasNext(), response.GetNextCursor(), cursor)
			if !valid {
				complete = false
				break
			}
			if !continuePaging {
				break
			}
			cursor = next
		}
		if complete {
			if err := e.saveOne(ctx, "operation_watermark", account.ID, "complete", to, to); err != nil {
				failures = append(failures, account.ID+": сохранение курсора: "+err.Error())
			}
		}
	}
	return errorsJoin(failures)
}

func operationFrom(watermark []byte) (time.Time, error) {
	if len(watermark) == 0 {
		return time.Unix(0, 0).UTC(), nil
	}
	var last time.Time
	if err := json.Unmarshal(watermark, &last); err != nil || last.IsZero() {
		return time.Time{}, fmt.Errorf("invalid operation watermark")
	}
	return last.Add(-24 * time.Hour), nil
}

func operationNextCursor(hasNext bool, next, current string) (string, bool, bool) {
	if !hasNext {
		return "", false, true
	}
	if next == "" || next == current {
		return "", false, false
	}
	return next, true, true
}

func (e *Engine) pollLastPrices(ctx context.Context) error {
	return e.pollPrices(ctx, "GetLastPrices", "last_price", e.Source.LastPrices)
}
func (e *Engine) pollClosePrices(ctx context.Context) error {
	return e.pollPrices(ctx, "GetClosePrices", "close_price", e.Source.ClosePrices)
}

func (e *Engine) pollPrices(ctx context.Context, method, entity string, call func(context.Context, []string) ([]portfolio.MarketQuote, error)) error {
	subscriptions, err := e.Store.ListSubscriptions(ctx)
	if err != nil {
		return err
	}
	ids := quoteSubscriptionIDs(subscriptions)
	if len(ids) == 0 {
		return nil
	}
	if err := e.wait(ctx, method); err != nil {
		return err
	}
	quotes, err := call(ctx, ids)
	if err != nil {
		return err
	}
	rows := make([]store.Record, 0, len(quotes))
	for _, quote := range quotes {
		key := quote.InstrumentUID
		if key == "" {
			key = quote.Figi
		}
		if key != "" {
			rows = append(rows, store.Record{Key: key, At: time.Now().UTC(), Payload: quote})
		}
	}
	return e.Store.ReplaceCurrent(ctx, entity, "portfolio", rows)
}

func quoteSubscriptionIDs(items []portfolio.Subscription) []string {
	seen := map[string]bool{}
	for _, item := range items {
		if !item.Resolved {
			continue
		}
		id := item.InstrumentUID
		if id == "" {
			id = item.Figi
		}
		if id != "" {
			seen[id] = true
		}
	}
	out := make([]string, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

func (e *Engine) pollTradingStatuses(ctx context.Context) error {
	ids := e.instrumentIDs()
	if len(ids) == 0 {
		return nil
	}
	if err := e.wait(ctx, "GetTradingStatuses"); err != nil {
		return err
	}
	states, err := e.Source.TradingStatuses(ctx, ids)
	if err != nil {
		return err
	}
	rows := make([]store.Record, 0, len(states))
	for _, state := range states {
		key := state.InstrumentUID
		if key == "" {
			key = state.Figi
		}
		if key != "" {
			rows = append(rows, store.Record{Key: key, At: time.Now().UTC(), Payload: state})
		}
	}
	return e.Store.ReplaceCurrent(ctx, "trading_status", "portfolio", rows)
}

func (e *Engine) pollMetadata(ctx context.Context) error {
	if !e.isPortfolioLoaded() {
		return ErrDependencyNotReady
	}
	var failures []string
	for _, position := range e.allPositions() {
		ticker := strings.TrimSpace(position.Ticker)
		classCode := strings.TrimSpace(position.ClassCode)
		if ticker == "" || classCode == "" {
			continue
		}
		if err := e.wait(ctx, "InstrumentByTicker"); err != nil {
			return err
		}
		meta, err := e.Source.InstrumentByTicker(ctx, ticker, classCode)
		if err != nil {
			failures = append(failures, ticker+" "+classCode+": "+err.Error())
			continue
		}
		key := position.InstrumentUID
		if key == "" {
			key = position.Figi
		}
		if key == "" {
			continue
		}
		if err := e.saveOne(ctx, "instrument_metadata", "instrument", key, time.Now().UTC(), meta); err != nil {
			failures = append(failures, ticker+" "+classCode+": "+err.Error())
		}
	}
	return errorsJoin(failures)
}

func (e *Engine) pollSharePassports(ctx context.Context) error { return e.pollPassports(ctx, "share") }
func (e *Engine) pollEtfPassports(ctx context.Context) error   { return e.pollPassports(ctx, "etf") }
func (e *Engine) pollBondPassports(ctx context.Context) error  { return e.pollPassports(ctx, "bond") }

func (e *Engine) pollPassports(ctx context.Context, only string) error {
	if !e.isPortfolioLoaded() {
		return ErrDependencyNotReady
	}
	var failures []string
	for _, position := range e.allPositions() {
		if position.InstrumentType != only {
			continue
		}
		uid := position.InstrumentUID
		if uid == "" {
			uid = position.Figi
		}
		if uid == "" {
			continue
		}
		switch only {
		case "share":
			if err := e.wait(ctx, "ShareBy"); err != nil {
				return err
			}
			value, err := e.Source.Share(ctx, uid)
			if err != nil {
				failures = append(failures, uid+": "+err.Error())
				continue
			}
			if err := e.saveOne(ctx, "share_passport", "instrument", uid, time.Now().UTC(), value); err != nil {
				failures = append(failures, uid+": "+err.Error())
			}
			e.mu.Lock()
			if value.AssetUID != "" {
				e.assetUIDs[uid] = value.AssetUID
			}
			if value.Exchange != "" {
				e.exchanges[value.Exchange] = true
			}
			e.mu.Unlock()
			if value.AssetUID != "" {
				if err := e.Store.SetSubscriptionAsset(ctx, uid, value.AssetUID); err != nil {
					failures = append(failures, uid+": "+err.Error())
				}
			}
		case "etf":
			if err := e.wait(ctx, "EtfBy"); err != nil {
				return err
			}
			value, err := e.Source.Etf(ctx, uid)
			if err != nil {
				failures = append(failures, uid+": "+err.Error())
				continue
			}
			if err := e.saveOne(ctx, "etf_passport", "instrument", uid, time.Now().UTC(), value); err != nil {
				failures = append(failures, uid+": "+err.Error())
			}
			e.mu.Lock()
			if value.AssetUID != "" {
				e.assetUIDs[uid] = value.AssetUID
			}
			if value.Exchange != "" {
				e.exchanges[value.Exchange] = true
			}
			e.mu.Unlock()
			if value.AssetUID != "" {
				if err := e.Store.SetSubscriptionAsset(ctx, uid, value.AssetUID); err != nil {
					failures = append(failures, uid+": "+err.Error())
				}
			}
		case "bond":
			if err := e.wait(ctx, "BondBy"); err != nil {
				return err
			}
			value, err := e.Source.Bond(ctx, uid)
			if err != nil {
				failures = append(failures, uid+": "+err.Error())
				continue
			}
			if err := e.saveOne(ctx, "bond_passport", "instrument", uid, time.Now().UTC(), value); err != nil {
				failures = append(failures, uid+": "+err.Error())
			}
			e.mu.Lock()
			if value.AssetUID != "" {
				e.assetUIDs[uid] = value.AssetUID
			}
			e.mu.Unlock()
			if value.AssetUID != "" {
				if err := e.Store.SetSubscriptionAsset(ctx, uid, value.AssetUID); err != nil {
					failures = append(failures, uid+": "+err.Error())
				}
			}
		}
	}
	return errorsJoin(failures)
}

func (e *Engine) pollCoupons(ctx context.Context) error {
	if !e.isPortfolioLoaded() {
		return ErrDependencyNotReady
	}
	var failures []string
	from, to := time.Now().UTC(), time.Now().UTC().Add(365*24*time.Hour)
	for _, p := range e.positionsOfType("bond") {
		uid := positionUID(p)
		if uid == "" {
			continue
		}
		if err := e.wait(ctx, "GetBondCoupons"); err != nil {
			return err
		}
		values, err := e.Source.BondCoupons(ctx, uid, from, to)
		if err != nil {
			failures = append(failures, uid+": "+err.Error())
			continue
		}
		for _, value := range values {
			key := uid + "|" + value.PayDate.UTC().Format(time.RFC3339Nano) + "|" + value.Type
			if err := e.Store.UpsertHistory(ctx, "bond_coupon", uid, key, value.PayDate, value); err != nil {
				failures = append(failures, uid+": "+err.Error())
			}
		}
	}
	return errorsJoin(failures)
}

func (e *Engine) pollBondEvents(ctx context.Context) error {
	if !e.isPortfolioLoaded() {
		return ErrDependencyNotReady
	}
	var failures []string
	from, to := time.Now().UTC(), time.Now().UTC().Add(365*24*time.Hour)
	for _, p := range e.positionsOfType("bond") {
		uid := positionUID(p)
		if uid == "" {
			continue
		}
		for _, kind := range []string{"call", "maturity", "conversion"} {
			if err := e.wait(ctx, "GetBondEvents"); err != nil {
				return err
			}
			values, err := e.Source.BondEvents(ctx, uid, kind, from, to)
			if err != nil {
				failures = append(failures, uid+" "+kind+": "+err.Error())
				continue
			}
			for _, value := range values {
				key := uid + "|" + kind + "|" + value.At.UTC().Format(time.RFC3339Nano)
				if err := e.Store.UpsertHistory(ctx, "bond_event", uid, key, value.At, value); err != nil {
					failures = append(failures, uid+": "+err.Error())
				}
			}
		}
	}
	return errorsJoin(failures)
}

func (e *Engine) pollDividends(ctx context.Context) error {
	if !e.isPortfolioLoaded() {
		return ErrDependencyNotReady
	}
	var failures []string
	from, to := time.Now().UTC(), time.Now().UTC().Add(365*24*time.Hour)
	for _, p := range e.positionsOfType("share", "etf") {
		uid := positionUID(p)
		if uid == "" {
			continue
		}
		if err := e.wait(ctx, "GetDividends"); err != nil {
			return err
		}
		values, err := e.Source.Dividends(ctx, uid, from, to)
		if err != nil {
			failures = append(failures, uid+": "+err.Error())
			continue
		}
		for _, value := range values {
			key := uid + "|" + value.RecordDate.UTC().Format("2006-01-02") + "|" + value.Type
			if err := e.Store.UpsertHistory(ctx, "dividend", uid, key, value.RecordDate, value); err != nil {
				failures = append(failures, uid+": "+err.Error())
			}
		}
	}
	return errorsJoin(failures)
}

func (e *Engine) pollAccrued(ctx context.Context) error {
	if !e.isPortfolioLoaded() {
		return ErrDependencyNotReady
	}
	var failures []string
	from, to := time.Now().UTC(), time.Now().UTC().Add(365*24*time.Hour)
	for _, p := range e.positionsOfType("bond") {
		uid := positionUID(p)
		if uid == "" {
			continue
		}
		if err := e.wait(ctx, "GetAccruedInterests"); err != nil {
			return err
		}
		values, err := e.Source.AccruedInterests(ctx, uid, from, to)
		if err != nil {
			failures = append(failures, uid+": "+err.Error())
			continue
		}
		for _, value := range values {
			key := uid + "|" + value.At.UTC().Format(time.RFC3339Nano)
			if err := e.Store.UpsertHistory(ctx, "accrued_interest", uid, key, value.At, value); err != nil {
				failures = append(failures, uid+": "+err.Error())
			}
		}
	}
	return errorsJoin(failures)
}

func (e *Engine) pollRiskRates(ctx context.Context) error {
	if !e.isPortfolioLoaded() {
		return ErrDependencyNotReady
	}
	ids := e.instrumentIDs()
	if len(ids) == 0 {
		return nil
	}
	if err := e.wait(ctx, "GetRiskRates"); err != nil {
		return err
	}
	rates, problems, err := e.Source.RiskRates(ctx, ids)
	if err != nil {
		return err
	}
	rows := make([]store.Record, 0, len(rates))
	for _, rate := range rates {
		key := rate.InstrumentUID + "|" + rate.Side + "|" + rate.Role + "|" + rate.LevelCode
		rows = append(rows, store.Record{Key: key, At: time.Now().UTC(), Payload: rate})
	}
	if err := e.Store.ReplaceCurrent(ctx, "risk_rate", "user", rows); err != nil {
		return err
	}
	if len(problems) > 0 {
		messages := make([]string, 0, len(problems))
		for _, p := range problems {
			messages = append(messages, p.InstrumentUID+": "+p.Message)
		}
		return errorsJoin(messages)
	}
	return nil
}

func (e *Engine) pollTradingSchedule(ctx context.Context) error {
	if !e.isPortfolioLoaded() {
		return ErrDependencyNotReady
	}
	e.mu.RLock()
	venues := make([]string, 0, len(e.exchanges))
	for venue := range e.exchanges {
		venues = append(venues, venue)
	}
	e.mu.RUnlock()
	sort.Strings(venues)
	var failures []string
	day := time.Now().UTC()
	for _, venue := range venues {
		if err := e.wait(ctx, "TradingSchedules"); err != nil {
			return err
		}
		values, err := e.Source.TradingSchedule(ctx, venue, day)
		if err != nil {
			failures = append(failures, venue+": "+err.Error())
			continue
		}
		for _, value := range values {
			key := value.Exchange + "|" + value.Date.UTC().Format("2006-01-02")
			if err := e.saveOne(ctx, "trading_day", venue, key, value.Date, value); err != nil {
				failures = append(failures, venue+": "+err.Error())
			}
		}
	}
	return errorsJoin(failures)
}

func (e *Engine) pollFundamentals(ctx context.Context) error {
	if !e.isPortfolioLoaded() {
		return ErrDependencyNotReady
	}
	e.mu.RLock()
	assets := make([]string, 0, len(e.assetUIDs))
	seen := map[string]bool{}
	for _, asset := range e.assetUIDs {
		if asset != "" && !seen[asset] {
			seen[asset] = true
			assets = append(assets, asset)
		}
	}
	e.mu.RUnlock()
	sort.Strings(assets)
	if len(assets) == 0 {
		return nil
	}
	if err := e.wait(ctx, "GetAssetFundamentals"); err != nil {
		return err
	}
	values, err := e.Source.AssetFundamentals(ctx, assets)
	if err != nil {
		return err
	}
	rows := make([]store.Record, 0, len(values))
	for _, value := range values {
		if value.AssetUID != "" && value.Metric != "" {
			rows = append(rows, store.Record{Key: value.AssetUID + "|" + value.Metric, At: time.Now().UTC(), Payload: value})
		}
	}
	return e.Store.ReplaceCurrent(ctx, "fundamental", "asset", rows)
}

func (e *Engine) pollInsiders(ctx context.Context) error {
	if !e.isPortfolioLoaded() {
		return ErrDependencyNotReady
	}
	var failures []string
	for _, p := range e.allPositions() {
		uid := positionUID(p)
		if uid == "" {
			continue
		}
		if err := e.wait(ctx, "GetInsiderDeals"); err != nil {
			return err
		}
		deals, err := e.Source.InsiderDeals(ctx, uid)
		if err != nil {
			failures = append(failures, uid+": "+err.Error())
			continue
		}
		for _, deal := range deals {
			key := fmt.Sprint(deal.TradeID)
			if deal.TradeID == 0 {
				key = hashedKey(uid + "|" + deal.At.UTC().Format(time.RFC3339Nano) + "|" + deal.InvestorName + "|" + deal.Direction)
			}
			if err := e.Store.UpsertHistory(ctx, "insider_deal", uid, key, deal.At, deal); err != nil {
				failures = append(failures, uid+": "+err.Error())
			}
		}
	}
	return errorsJoin(failures)
}

func (e *Engine) pollNews(ctx context.Context) error {
	if !e.isPortfolioLoaded() {
		return ErrDependencyNotReady
	}
	if err := e.wait(ctx, "GetNews"); err != nil {
		return err
	}
	items, err := e.Source.News(ctx)
	if err != nil {
		return err
	}
	active := map[string]bool{}
	for _, p := range e.allPositions() {
		active[positionUID(p)] = true
	}
	var failures []string
	for _, item := range items {
		matched := false
		for _, instrument := range item.Instruments {
			if active[instrument.InstrumentUID] || instrument.Ticker != "" && e.hasTicker(instrument.Ticker) {
				matched = true
				break
			}
		}
		if !matched {
			continue
		}
		key := fmt.Sprint(item.ID)
		if item.ID == 0 {
			key = hashedKey(item.Source + "|" + item.At.UTC().Format(time.RFC3339Nano) + "|" + item.Title)
		}
		if err := e.Store.UpsertHistory(ctx, "news", "portfolio", key, item.At, item); err != nil {
			failures = append(failures, err.Error())
		}
	}
	return errorsJoin(failures)
}

func (e *Engine) pollInstrumentSearch(ctx context.Context) error {
	if !e.isPortfolioLoaded() {
		return ErrDependencyNotReady
	}
	seen := map[string]bool{}
	var failures []string
	for _, p := range e.allPositions() {
		ticker := strings.TrimSpace(p.Ticker)
		if ticker == "" || seen[ticker] {
			continue
		}
		seen[ticker] = true
		if err := e.wait(ctx, "FindInstrument"); err != nil {
			return err
		}
		hits, err := e.Source.FindInstrument(ctx, ticker)
		if err != nil {
			failures = append(failures, ticker+": "+err.Error())
			continue
		}
		for _, hit := range hits {
			if hit.UID == "" {
				continue
			}
			key := ticker + "|" + hit.UID
			if err := e.saveOne(ctx, "instrument_search", ticker, key, time.Now().UTC(), hit); err != nil {
				failures = append(failures, ticker+": "+err.Error())
			}
		}
	}
	return errorsJoin(failures)
}

func (e *Engine) pollCountries(ctx context.Context) error {
	if err := e.wait(ctx, "GetCountries"); err != nil {
		return err
	}
	values, err := e.Source.Countries(ctx)
	if err != nil {
		return err
	}
	rows := make([]store.Record, 0, len(values))
	for _, value := range values {
		key := value.AlfaTwo
		if key == "" {
			key = value.AlfaThree
		}
		if key != "" {
			rows = append(rows, store.Record{Key: key, At: time.Now().UTC(), Payload: value})
		}
	}
	return e.Store.ReplaceCurrent(ctx, "country", "all", rows)
}

func (e *Engine) pollBrands(ctx context.Context) error {
	if err := e.wait(ctx, "GetBrands"); err != nil {
		return err
	}
	values, err := e.Source.Brands(ctx)
	if err != nil {
		return err
	}
	rows := make([]store.Record, 0, len(values))
	for _, value := range values {
		if value.UID != "" {
			rows = append(rows, store.Record{Key: value.UID, At: time.Now().UTC(), Payload: value})
		}
	}
	return e.Store.ReplaceCurrent(ctx, "brand", "all", rows)
}

func (e *Engine) positionsOfType(types ...string) []portfolio.Position {
	allowed := map[string]bool{}
	for _, kind := range types {
		allowed[kind] = true
	}
	var out []portfolio.Position
	for _, p := range e.allPositions() {
		if allowed[p.InstrumentType] {
			out = append(out, p)
		}
	}
	return out
}

func positionUID(position portfolio.Position) string {
	if position.InstrumentUID != "" {
		return position.InstrumentUID
	}
	return position.Figi
}

func (e *Engine) hasTicker(ticker string) bool {
	for _, p := range e.allPositions() {
		if strings.EqualFold(p.Ticker, ticker) {
			return true
		}
	}
	return false
}

func (e *Engine) forAccounts(ctx context.Context, method, entity string, poll func(context.Context, portfolio.AccountMeta) ([]store.Record, error)) error {
	var failures []string
	for _, account := range e.accountList() {
		if account.Type == portfolio.AccountTypeInvestBox {
			continue
		}
		if err := e.wait(ctx, method); err != nil {
			return err
		}
		rows, err := poll(ctx, account)
		if err != nil {
			failures = append(failures, account.ID+": "+err.Error())
			continue
		}
		if err := e.Store.ReplaceCurrent(ctx, entity, account.ID, rows); err != nil {
			failures = append(failures, account.ID+": "+err.Error())
		}
	}
	return errorsJoin(failures)
}

func (e *Engine) wait(ctx context.Context, method string) error { return e.gate.Wait(ctx, method) }
func (e *Engine) accountList() []portfolio.AccountMeta {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return append([]portfolio.AccountMeta(nil), e.accounts...)
}
func (e *Engine) allPositions() []portfolio.Position {
	e.mu.RLock()
	defer e.mu.RUnlock()
	var out []portfolio.Position
	for _, rows := range e.positions {
		out = append(out, rows...)
	}
	return out
}
func (e *Engine) instrumentIDs() []string {
	seen := map[string]bool{}
	for _, p := range e.allPositions() {
		if p.Cash {
			continue
		}
		id := p.InstrumentUID
		if id == "" {
			id = p.Figi
		}
		if id != "" {
			seen[id] = true
		}
	}
	out := make([]string, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

func (e *Engine) isPortfolioLoaded() bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.portfolioLoaded
}
func (e *Engine) pollAssetCards(ctx context.Context) error {
	if err := e.Store.BackfillSubscriptions(ctx); err != nil {
		return err
	}
	items, err := e.Store.ListSubscriptions(ctx)
	if err != nil {
		return err
	}
	if len(items) == 0 {
		if !e.isPortfolioLoaded() {
			return ErrDependencyNotReady
		}
		return nil
	}
	var failures []string
	assets := map[string]bool{}
	for _, item := range items {
		if item.AssetUID != "" {
			assets[item.AssetUID] = true
			continue
		}
		if item.InstrumentUID == "" {
			continue
		}
		if err := e.wait(ctx, "InstrumentByUid"); err != nil {
			return err
		}
		meta, err := e.Source.Instrument(ctx, item.InstrumentUID)
		if err != nil {
			failures = append(failures, item.InstrumentUID+": "+err.Error())
			continue
		}
		if meta.AssetUID == "" {
			continue
		}
		if err := e.Store.SetSubscriptionAsset(ctx, item.InstrumentUID, meta.AssetUID); err != nil {
			failures = append(failures, item.InstrumentUID+": "+err.Error())
			continue
		}
		assets[meta.AssetUID] = true
	}
	ids := make([]string, 0, len(assets))
	for id := range assets {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, assetUID := range ids {
		if err := e.wait(ctx, "GetAssetBy"); err != nil {
			return err
		}
		card, err := e.Source.AssetBy(ctx, assetUID)
		if err != nil {
			failures = append(failures, assetUID+": "+err.Error())
			continue
		}
		if card.AssetUID == "" {
			continue
		}
		if err := e.saveOne(ctx, "asset_card", assetUID, "card", time.Now().UTC(), card); err != nil {
			failures = append(failures, assetUID+": "+err.Error())
		}
	}
	return errorsJoin(failures)
}

func (e *Engine) saveOne(ctx context.Context, entity, scope, key string, at time.Time, value any) error {
	return e.Store.ReplaceCurrent(ctx, entity, scope, []store.Record{{Key: key, At: at, Payload: value}})
}

func operationKey(accountID string, op portfolio.Operation) string {
	if op.At.IsZero() {
		return ""
	}
	key := strings.Join([]string{accountID, op.At.UTC().Format(time.RFC3339Nano), op.Type, op.InstrumentUID, op.Payment.Amount.String(), op.Payment.Currency, fmt.Sprint(op.Quantity)}, "|")
	return hashedKey(key)
}

func hashedKey(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func errorsJoin(values []string) error {
	if len(values) == 0 {
		return nil
	}
	return fmt.Errorf("%s", strings.Join(values, "; "))
}
