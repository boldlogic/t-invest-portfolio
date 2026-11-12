package portfolio

import (
	"time"

	"github.com/shopspring/decimal"
)

// AccountTypeInvestBox — счёт, по которому GetPortfolio не работает.
const AccountTypeInvestBox = "ACCOUNT_TYPE_INVEST_BOX"

// CashRUBFigi — рублёвая валютная позиция, это кэш, не бумага.
const CashRUBFigi = "RUB000UTSTOM"

// AccountMeta — счёт из UsersService до запроса портфеля.
type AccountMeta struct {
	ID   string
	Name string
	Type string
}

// InstrumentMeta — название, которого нет в ответе портфеля.
type InstrumentMeta struct {
	Ticker   string
	Name     string
	AssetUID string
}

// Account — портфель одного счёта или причина, почему он пропущен.
type Account struct {
	ID                string            `json:"id"`
	Name              string            `json:"name,omitempty"`
	Type              string            `json:"type"`
	TypeLabel         string            `json:"type_label"`
	Skipped           bool              `json:"skipped"`
	SkipReason        string            `json:"skip_reason,omitempty"`
	Totals            Totals            `json:"totals"`
	Positions         []Position        `json:"positions,omitempty"`
	VirtualPositions  []VirtualPosition `json:"virtual_positions,omitempty"`
	Holdings          []Holding         `json:"holdings,omitempty"`
	PositionMoney     []Money           `json:"position_money,omitempty"`
	PositionBlocked   []Money           `json:"position_blocked,omitempty"`
	LimitsLoading     bool              `json:"limits_loading"`
	Withdraw          Withdraw          `json:"withdraw"`
	Margin            Margin            `json:"margin"`
	Operations        []Operation       `json:"operations,omitempty"`
	BrokerReport      []BrokerTrade     `json:"broker_report,omitempty"`
	Orders            []Order           `json:"orders,omitempty"`
	StopOrders        []StopOrder       `json:"stop_orders,omitempty"`
	PositionsValue    Money             `json:"positions_value"`
	TotalDelta        Money             `json:"total_delta"`
	ValueCheckPartial bool              `json:"value_check_partial"`
	Warnings          []string          `json:"warnings,omitempty"`
}

// Totals — суммы, которые посчитал брокер. Запрос всегда в RUB.
type Totals struct {
	Shares            Money   `json:"shares"`
	Bonds             Money   `json:"bonds"`
	ETF               Money   `json:"etf"`
	Currencies        Money   `json:"currencies"`
	Futures           Money   `json:"futures"`
	Options           Money   `json:"options"`
	StructuredNotes   Money   `json:"structured_notes"`
	DFA               Money   `json:"dfa"`
	Portfolio         Money   `json:"portfolio"`
	VarMargin         Money   `json:"var_margin"`
	ExpectedYield     Percent `json:"expected_yield"`
	DailyYield        Money   `json:"daily_yield"`
	DailyYieldPercent Percent `json:"daily_yield_percent"`
}

// Position — строка портфеля.
type Position struct {
	Figi             string            `json:"figi,omitempty"`
	Ticker           string            `json:"ticker,omitempty"`
	Name             string            `json:"name,omitempty"`
	ClassCode        string            `json:"class_code,omitempty"`
	InstrumentType   string            `json:"instrument_type,omitempty"`
	InstrumentUID    string            `json:"instrument_uid,omitempty"`
	Quantity         decimal.Decimal   `json:"quantity"`
	AveragePrice     Money             `json:"average_price"`
	AveragePriceFIFO Money             `json:"average_price_fifo"`
	CurrentPrice     Money             `json:"current_price"`
	CurrentNKD       Money             `json:"current_nkd"`
	MarketValue      Money             `json:"market_value"`
	MarketValueKnown bool              `json:"market_value_known"`
	ExpectedYield    Percent           `json:"expected_yield"`
	Blocked          bool              `json:"blocked"`
	BlockedLots      decimal.Decimal   `json:"blocked_lots"`
	VarMargin        Money             `json:"var_margin"`
	Cash             bool              `json:"cash"`
	LastPrice        Quote             `json:"last_price"`
	ClosePrice       Quote             `json:"close_price"`
	Nominal          Money             `json:"nominal"`
	NominalKnown     bool              `json:"nominal_known"`
	Maturity         time.Time         `json:"maturity,omitempty"`
	CouponsPerYear   int32             `json:"coupons_per_year,omitempty"`
	AssetUID         string            `json:"asset_uid,omitempty"`
	Coupons          []Coupon          `json:"coupons,omitempty"`
	BondEvents       []BondEvent       `json:"bond_events,omitempty"`
	Accrued          []AccruedInterest `json:"accrued,omitempty"`
	Dividends        []Dividend        `json:"dividends,omitempty"`
	InsiderDeals     []InsiderDeal     `json:"insider_deals,omitempty"`
	TradingStatus    string            `json:"trading_status,omitempty"`
	Share            SharePassport     `json:"share"`
	Etf              EtfPassport       `json:"etf"`
	RiskRates        []RiskRate        `json:"risk_rates,omitempty"`
}

// Quote — цена биржи без валюты: в ответе котировок её нет.
type Quote struct {
	Value decimal.Decimal `json:"value"`
	Known bool            `json:"known"`
}

// MarketQuote — цена из GetLastPrices или GetClosePrices.
type MarketQuote struct {
	InstrumentUID string
	Figi          string
	Value         decimal.Decimal
}

// Holding — штуки из GetPositions, без оценки брокера.
type Holding struct {
	Kind            string `json:"kind"`
	Figi            string `json:"figi,omitempty"`
	Ticker          string `json:"ticker,omitempty"`
	ClassCode       string `json:"class_code,omitempty"`
	InstrumentUID   string `json:"instrument_uid,omitempty"`
	InstrumentType  string `json:"instrument_type,omitempty"`
	Balance         int64  `json:"balance"`
	Blocked         int64  `json:"blocked"`
	ExchangeBlocked bool   `json:"exchange_blocked,omitempty"`
}

// Withdraw — деньги к выводу и блокировки.
type Withdraw struct {
	Money     []Money `json:"money,omitempty"`
	Blocked   []Money `json:"blocked,omitempty"`
	Guarantee []Money `json:"guarantee,omitempty"`
}

// Margin — показатели маржи. Known=false, если метод не ответил.
type Margin struct {
	Known           bool    `json:"known"`
	LiquidPortfolio Money   `json:"liquid_portfolio"`
	StartingMargin  Money   `json:"starting_margin"`
	MinimalMargin   Money   `json:"minimal_margin"`
	MissingFunds    Money   `json:"missing_funds"`
	Sufficiency     Percent `json:"sufficiency"`
}

// Operation — исполненная операция. Идентификатор API не хранится: он меняется.
type Operation struct {
	At            time.Time `json:"at,omitempty"`
	Type          string    `json:"type,omitempty"`
	Name          string    `json:"name,omitempty"`
	State         string    `json:"state,omitempty"`
	Figi          string    `json:"figi,omitempty"`
	Ticker        string    `json:"ticker,omitempty"`
	InstrumentUID string    `json:"instrument_uid,omitempty"`
	Payment       Money     `json:"payment"`
	Quantity      int64     `json:"quantity"`
	Fills         []Fill    `json:"fills,omitempty"`
}

// BrokerTrade — строка брокерского отчёта.
type BrokerTrade struct {
	TradeID            string    `json:"trade_id,omitempty"`
	At                 time.Time `json:"at,omitempty"`
	Ticker             string    `json:"ticker,omitempty"`
	Name               string    `json:"name,omitempty"`
	Direction          string    `json:"direction,omitempty"`
	Quantity           int64     `json:"quantity"`
	Amount             Money     `json:"amount"`
	BrokerCommission   Money     `json:"broker_commission"`
	ExchangeCommission Money     `json:"exchange_commission"`
}

// VirtualPosition — временные бумаги. В обычный остаток они не входят.
type VirtualPosition struct {
	Figi           string          `json:"figi,omitempty"`
	Ticker         string          `json:"ticker,omitempty"`
	Name           string          `json:"name,omitempty"`
	InstrumentType string          `json:"instrument_type,omitempty"`
	InstrumentUID  string          `json:"instrument_uid,omitempty"`
	Quantity       decimal.Decimal `json:"quantity"`
	AveragePrice   Money           `json:"average_price"`
	CurrentPrice   Money           `json:"current_price"`
	ExpectedYield  Percent         `json:"expected_yield"`
	ExpireDate     time.Time       `json:"expire_date,omitempty"`
	HasExpireDate  bool            `json:"has_expire_date"`
}
