package portfolio

import (
	"time"

	"github.com/shopspring/decimal"
)

// Profile — карточка пользователя. Лимиты лежат отдельно: их несколько.
type Profile struct {
	UserID         string      `json:"user_id,omitempty"`
	Premium        bool        `json:"premium"`
	Qualified      bool        `json:"qualified"`
	Tariff         string      `json:"tariff,omitempty"`
	Qualifications []string    `json:"qualifications,omitempty"`
	Limits         []RateLimit `json:"limits,omitempty"`
}

// RateLimit — одна группа лимитов unary или stream.
type RateLimit struct {
	Kind      string   `json:"kind"`
	PerMinute int32    `json:"per_minute,omitempty"`
	PerSecond int32    `json:"per_second,omitempty"`
	Limit     int32    `json:"limit,omitempty"`
	Open      int32    `json:"open,omitempty"`
	Methods   []string `json:"methods,omitempty"`
}

// Coupon — выплата на одну облигацию.
type Coupon struct {
	InstrumentUID string    `json:"instrument_uid,omitempty"`
	PayDate       time.Time `json:"pay_date,omitempty"`
	Amount        Money     `json:"amount"`
	Type          string    `json:"type,omitempty"`
}

// BondEvent — оферта, погашение или конвертация. Купон сюда не кладётся.
type BondEvent struct {
	InstrumentUID string    `json:"instrument_uid,omitempty"`
	Type          string    `json:"type"`
	At            time.Time `json:"at,omitempty"`
	Pay           Money     `json:"pay"`
}

// Dividend — выплата на одну бумагу из календаря, не из операций.
type Dividend struct {
	InstrumentUID string    `json:"instrument_uid,omitempty"`
	RecordDate    time.Time `json:"record_date,omitempty"`
	PaymentDate   time.Time `json:"payment_date,omitempty"`
	Type          string    `json:"type,omitempty"`
	Amount        Money     `json:"amount"`
}

// TradingState — статус торгов бумаги на момент запроса.
type TradingState struct {
	InstrumentUID string
	Figi          string
	Status        string
}

// Order — активная заявка на момент снимка.
type Order struct {
	ID            string    `json:"id,omitempty"`
	InstrumentUID string    `json:"instrument_uid,omitempty"`
	Figi          string    `json:"figi,omitempty"`
	Ticker        string    `json:"ticker,omitempty"`
	ISIN          string    `json:"isin,omitempty"`
	Direction     string    `json:"direction,omitempty"`
	LotsRequested int64     `json:"lots_requested"`
	LotsExecuted  int64     `json:"lots_executed"`
	Price         Money     `json:"price"`
	Status        string    `json:"status,omitempty"`
	At            time.Time `json:"at,omitempty"`
}

// StopOrder — стоп-заявка на момент снимка.
type StopOrder struct {
	ID            string    `json:"id,omitempty"`
	InstrumentUID string    `json:"instrument_uid,omitempty"`
	Figi          string    `json:"figi,omitempty"`
	Ticker        string    `json:"ticker,omitempty"`
	ISIN          string    `json:"isin,omitempty"`
	Direction     string    `json:"direction,omitempty"`
	LotsRequested int64     `json:"lots_requested"`
	Price         Money     `json:"price"`
	StopPrice     Money     `json:"stop_price"`
	Status        string    `json:"status,omitempty"`
	At            time.Time `json:"at,omitempty"`
}

// SharePassport — паспорт акции. Цена позиции сюда не входит.
type SharePassport struct {
	InstrumentUID string `json:"instrument_uid,omitempty"`
	Lot           int32  `json:"lot,omitempty"`
	Currency      string `json:"currency,omitempty"`
	ClassCode     string `json:"class_code,omitempty"`
	Exchange      string `json:"exchange,omitempty"`
	ISIN          string `json:"isin,omitempty"`
	Sector        string `json:"sector,omitempty"`
	ShareType     string `json:"share_type,omitempty"`
	DividendYield bool   `json:"dividend_yield,omitempty"`
	Nominal       Money  `json:"nominal"`
	NominalKnown  bool   `json:"nominal_known,omitempty"`
	ForQual       bool   `json:"for_qual,omitempty"`
	AssetUID      string `json:"asset_uid,omitempty"`
	Known         bool   `json:"known,omitempty"`
}

// EtfPassport — паспорт фонда. Комиссия хранится decimal, не float.
type EtfPassport struct {
	InstrumentUID   string          `json:"instrument_uid,omitempty"`
	Lot             int32           `json:"lot,omitempty"`
	Currency        string          `json:"currency,omitempty"`
	ClassCode       string          `json:"class_code,omitempty"`
	Exchange        string          `json:"exchange,omitempty"`
	ISIN            string          `json:"isin,omitempty"`
	Sector          string          `json:"sector,omitempty"`
	FocusType       string          `json:"focus_type,omitempty"`
	Commission      decimal.Decimal `json:"commission"`
	CommissionKnown bool            `json:"commission_known,omitempty"`
	Rebalancing     string          `json:"rebalancing,omitempty"`
	AssetUID        string          `json:"asset_uid,omitempty"`
	Known           bool            `json:"known,omitempty"`
}

// AccruedInterest — точка графика НКД. Текущий НКД позиции сюда не входит.
type AccruedInterest struct {
	InstrumentUID string          `json:"instrument_uid,omitempty"`
	At            time.Time       `json:"at,omitempty"`
	Amount        Money           `json:"amount"`
	AmountKnown   bool            `json:"amount_known,omitempty"`
	Percent       decimal.Decimal `json:"percent"`
	PercentKnown  bool            `json:"percent_known,omitempty"`
	Nominal       Money           `json:"nominal"`
	NominalKnown  bool            `json:"nominal_known,omitempty"`
}

// Fundamental — одно поле фундаментала. Ноль proto3 хранится как ноль.
type Fundamental struct {
	AssetUID string `json:"asset_uid,omitempty"`
	Metric   string `json:"metric"`
	Value    string `json:"value"`
}

// InsiderDeal — сделка инсайдера. Нулевой trade_id ключом не считается.
type InsiderDeal struct {
	TradeID          int64     `json:"trade_id,omitempty"`
	InstrumentUID    string    `json:"instrument_uid,omitempty"`
	Direction        string    `json:"direction,omitempty"`
	Currency         string    `json:"currency,omitempty"`
	At               time.Time `json:"at,omitempty"`
	Quantity         int64     `json:"quantity"`
	Price            Money     `json:"price"`
	Ticker           string    `json:"ticker,omitempty"`
	InvestorName     string    `json:"investor_name,omitempty"`
	InvestorPosition string    `json:"investor_position,omitempty"`
	Percentage       string    `json:"percentage,omitempty"`
	OptionExecution  bool      `json:"option_execution,omitempty"`
	Disclosure       time.Time `json:"disclosure,omitempty"`
}

// NewsItem — новость, в которой есть бумага портфеля.
type NewsItem struct {
	ID          int64       `json:"id,omitempty"`
	Source      string      `json:"source,omitempty"`
	Title       string      `json:"title,omitempty"`
	Content     string      `json:"content,omitempty"`
	Summary     string      `json:"summary,omitempty"`
	Priority    bool        `json:"priority,omitempty"`
	At          time.Time   `json:"at,omitempty"`
	Instruments []NewsPaper `json:"instruments,omitempty"`
}

// NewsPaper — бумага портфеля, упомянутая в новости.
type NewsPaper struct {
	InstrumentUID string `json:"instrument_uid,omitempty"`
	Ticker        string `json:"ticker,omitempty"`
	ClassCode     string `json:"class_code,omitempty"`
}

// InstrumentHit — одна строка FindInstrument.
type InstrumentHit struct {
	Query          string    `json:"query,omitempty"`
	ISIN           string    `json:"isin,omitempty"`
	Figi           string    `json:"figi,omitempty"`
	Ticker         string    `json:"ticker,omitempty"`
	ClassCode      string    `json:"class_code,omitempty"`
	InstrumentType string    `json:"instrument_type,omitempty"`
	Name           string    `json:"name,omitempty"`
	UID            string    `json:"uid,omitempty"`
	PositionUID    string    `json:"position_uid,omitempty"`
	Kind           string    `json:"kind,omitempty"`
	APITrade       bool      `json:"api_trade,omitempty"`
	ForIIS         bool      `json:"for_iis,omitempty"`
	ForQual        bool      `json:"for_qual,omitempty"`
	Weekend        bool      `json:"weekend,omitempty"`
	BlockedTCA     bool      `json:"blocked_tca,omitempty"`
	Lot            int32     `json:"lot,omitempty"`
	First1Min      time.Time `json:"first_1min,omitempty"`
	First1Day      time.Time `json:"first_1day,omitempty"`
}

// Country — строка справочника стран.
type Country struct {
	AlfaTwo   string `json:"alfa_two,omitempty"`
	AlfaThree string `json:"alfa_three,omitempty"`
	Name      string `json:"name,omitempty"`
	NameBrief string `json:"name_brief,omitempty"`
}

// Brand — строка справочника брендов.
type Brand struct {
	UID               string `json:"uid,omitempty"`
	Name              string `json:"name,omitempty"`
	Description       string `json:"description,omitempty"`
	Info              string `json:"info,omitempty"`
	Company           string `json:"company,omitempty"`
	Sector            string `json:"sector,omitempty"`
	CountryOfRisk     string `json:"country_of_risk,omitempty"`
	CountryOfRiskName string `json:"country_of_risk_name,omitempty"`
}

// RiskRate — одна ставка риска. Роль отделяет ставку пользователя от доступного уровня.
type RiskRate struct {
	InstrumentUID string          `json:"instrument_uid,omitempty"`
	Side          string          `json:"side"`
	Role          string          `json:"role"`
	LevelCode     string          `json:"level_code,omitempty"`
	Value         decimal.Decimal `json:"value"`
}

// RiskProblem — ошибка ставки одной бумаги. Запуск из-за неё не останавливается.
type RiskProblem struct {
	InstrumentUID string
	Message       string
}

// AssetCard — текущая карточка актива. Поля, которых нет в ответе для этого типа бумаги, остаются пустыми.
type AssetCard struct {
	AssetUID          string               `json:"asset_uid,omitempty"`
	Description       string               `json:"description,omitempty"`
	Sector            string               `json:"sector,omitempty"`
	CountryOfRisk     string               `json:"country_of_risk,omitempty"`
	CountryOfRiskName string               `json:"country_of_risk_name,omitempty"`
	BrandDescription  string               `json:"brand_description,omitempty"`
	Company           string               `json:"company,omitempty"`
	Instruments       []AssetInstrumentRef `json:"instruments,omitempty"`
	Bond              *AssetBondTerms      `json:"bond,omitempty"`
	Fund              *AssetFundTerms      `json:"fund,omitempty"`
}

// AssetInstrumentRef — инструмент актива и его связи.
type AssetInstrumentRef struct {
	UID            string         `json:"uid"`
	Figi           string         `json:"figi,omitempty"`
	Ticker         string         `json:"ticker,omitempty"`
	ClassCode      string         `json:"class_code,omitempty"`
	InstrumentType string         `json:"instrument_type,omitempty"`
	Links          []AssetLinkRef `json:"links,omitempty"`
}

// AssetLinkRef — связь с другим инструментом. Пустой uid связи ключом не является.
type AssetLinkRef struct {
	Type          string `json:"type,omitempty"`
	InstrumentUID string `json:"instrument_uid"`
}

// AssetBondTerms — номиналы и флаги облигации из карточки актива.
type AssetBondTerms struct {
	CurrentNominal  *decimal.Decimal `json:"current_nominal,omitempty"`
	Nominal         *decimal.Decimal `json:"nominal,omitempty"`
	NominalCurrency string           `json:"nominal_currency,omitempty"`
	IndexedNominal  bool             `json:"indexed_nominal"`
	Amortization    bool             `json:"amortization"`
	FloatingCoupon  bool             `json:"floating_coupon"`
}

// AssetFundTerms — индекс, описание и комиссии фонда.
type AssetFundTerms struct {
	Index             string           `json:"index,omitempty"`
	Description       string           `json:"description,omitempty"`
	IndexDescription  string           `json:"index_description,omitempty"`
	TotalExpense      *decimal.Decimal `json:"total_expense,omitempty"`
	HurdleRate        *decimal.Decimal `json:"hurdle_rate,omitempty"`
	PerformanceFee    *decimal.Decimal `json:"performance_fee,omitempty"`
	FixedCommission   *decimal.Decimal `json:"fixed_commission,omitempty"`
	ExpenseCommission *decimal.Decimal `json:"expense_commission,omitempty"`
	BuyPremium        *decimal.Decimal `json:"buy_premium,omitempty"`
	SellDiscount      *decimal.Decimal `json:"sell_discount,omitempty"`
	Watermark         bool             `json:"watermark"`
}

// TradingDay — один день расписания биржи.
type TradingDay struct {
	Exchange string    `json:"exchange"`
	Date     time.Time `json:"date"`
	Trading  bool      `json:"trading"`
	Start    time.Time `json:"start,omitempty"`
	End      time.Time `json:"end,omitempty"`
}

// Fill — одна сделка. Номер пустым ключом не считается.
type Fill struct {
	TradeNum      string    `json:"trade_num,omitempty"`
	AccountID     string    `json:"account_id,omitempty"`
	At            time.Time `json:"at,omitempty"`
	InstrumentUID string    `json:"instrument_uid,omitempty"`
	Figi          string    `json:"figi,omitempty"`
	Ticker        string    `json:"ticker,omitempty"`
	ISIN          string    `json:"isin,omitempty"`
	Direction     string    `json:"direction,omitempty"`
	Price         Money     `json:"price"`
	Quantity      int64     `json:"quantity"`
}
