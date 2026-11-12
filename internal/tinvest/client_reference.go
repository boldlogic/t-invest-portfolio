package tinvest

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/shopspring/decimal"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
	pb "opensource.tbank.ru/invest/invest-go/proto"

	"t-invest-portfolio/internal/portfolio"
)

const insiderPageLimit = 100

// AccruedInterests возвращает график НКД облигации за период.
func (c *Client) AccruedInterests(ctx context.Context, uid string, from, to time.Time) ([]portfolio.AccruedInterest, error) {
	started := time.Now()
	request := map[string]any{"uid": uid, "from": from.UTC().Format(time.RFC3339), "to": to.UTC().Format(time.RFC3339)}
	if err := ctx.Err(); err != nil {
		c.note("GetAccruedInterests", started, request, nil, err)
		return nil, err
	}
	resp, err := c.instruments.GetAccruedInterests(uid, from, to)
	var body proto.Message
	if resp != nil && resp.GetAccruedInterestsResponse != nil {
		body = resp.GetAccruedInterestsResponse
	}
	c.note("GetAccruedInterests", started, request, body, err)
	if err != nil || resp == nil {
		return nil, err
	}
	out := make([]portfolio.AccruedInterest, 0)
	for _, item := range resp.GetAccruedInterests() {
		if item == nil {
			continue
		}
		amount, amountKnown := quoteDecimal(item.GetValue())
		percent, percentKnown := quoteDecimal(item.GetValuePercent())
		nominal, nominalKnown := quoteDecimal(item.GetNominal())
		out = append(out, portfolio.AccruedInterest{
			InstrumentUID: uid,
			At:            stampTime(item.GetDate()),
			Amount:        portfolio.Money{Amount: amount},
			AmountKnown:   amountKnown,
			Percent:       percent,
			PercentKnown:  percentKnown,
			Nominal:       portfolio.Money{Amount: nominal},
			NominalKnown:  nominalKnown,
		})
	}
	return out, nil
}

// AssetFundamentals читает показатели пачкой asset_uid.
func (c *Client) AssetFundamentals(ctx context.Context, assetUIDs []string) ([]portfolio.Fundamental, error) {
	started := time.Now()
	request := map[string]any{"assets": assetUIDs}
	if err := ctx.Err(); err != nil {
		c.note("GetAssetFundamentals", started, request, nil, err)
		return nil, err
	}
	if len(assetUIDs) == 0 {
		c.note("GetAssetFundamentals", started, request, map[string]any{"skipped": "нет активов"}, nil)
		return nil, nil
	}
	resp, err := c.instruments.GetAssetFundamentals(assetUIDs)
	var body proto.Message
	if resp != nil && resp.GetAssetFundamentalsResponse != nil {
		body = resp.GetAssetFundamentalsResponse
	}
	c.note("GetAssetFundamentals", started, request, body, err)
	if err != nil || resp == nil {
		return nil, err
	}
	out := make([]portfolio.Fundamental, 0)
	for _, item := range resp.GetFundamentals() {
		out = append(out, fundamentalRows(item)...)
	}
	return out, nil
}

// InsiderDeals листает сделки инсайдера, пока брокер отдаёт курсор.
func (c *Client) InsiderDeals(ctx context.Context, uid string) ([]portfolio.InsiderDeal, error) {
	var cursor *string
	seen := map[string]struct{}{}
	out := make([]portfolio.InsiderDeal, 0)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		started := time.Now()
		current := ""
		if cursor != nil {
			current = *cursor
		}
		request := map[string]any{"uid": uid, "cursor": current, "limit": insiderPageLimit}
		resp, err := c.instruments.GetInsiderDeals(uid, insiderPageLimit, cursor)
		var body proto.Message
		if resp != nil && resp.GetInsiderDealsResponse != nil {
			body = resp.GetInsiderDealsResponse
		}
		c.note("GetInsiderDeals", started, request, body, err)
		if err != nil || resp == nil {
			return nil, err
		}
		for _, item := range resp.GetInsiderDeals() {
			if item == nil {
				continue
			}
			price, _ := quoteDecimal(item.GetPrice())
			instrumentUID := item.GetInstrumentUid()
			if instrumentUID == "" {
				instrumentUID = uid
			}
			out = append(out, portfolio.InsiderDeal{
				TradeID:          item.GetTradeId(),
				InstrumentUID:    instrumentUID,
				Direction:        item.GetDirection().String(),
				Currency:         strings.ToUpper(item.GetCurrency()),
				At:               stampTime(item.GetDate()),
				Quantity:         item.GetQuantity(),
				Price:            portfolio.Money{Amount: price, Currency: strings.ToUpper(item.GetCurrency())},
				Ticker:           item.GetTicker(),
				InvestorName:     item.GetInvestorName(),
				InvestorPosition: item.GetInvestorPosition(),
				Percentage:       strconv.FormatFloat(float64(item.GetPercentage()), 'g', -1, 32),
				OptionExecution:  item.GetIsOptionExecution(),
				Disclosure:       stampTime(item.GetDisclosureDate()),
			})
		}
		next := resp.GetNextCursor()
		if next == "" {
			return out, nil
		}
		if _, ok := seen[next]; ok {
			return out, nil
		}
		seen[next] = struct{}{}
		cursor = &next
	}
}

// News возвращает одну страницу новостей. Отбор по бумагам портфеля делает сборка снимка.
func (c *Client) News(ctx context.Context) ([]portfolio.NewsItem, error) {
	started := time.Now()
	request := map[string]any{}
	if err := ctx.Err(); err != nil {
		c.note("News", started, request, nil, err)
		return nil, err
	}
	resp, err := c.instruments.News(&pb.NewsRequest{})
	var body proto.Message
	if resp != nil && resp.NewsResponse != nil {
		body = resp.NewsResponse
	}
	c.note("News", started, request, body, err)
	if err != nil || resp == nil {
		return nil, err
	}
	out := make([]portfolio.NewsItem, 0)
	for _, item := range resp.GetItems() {
		if item == nil || item.GetId() == 0 {
			continue
		}
		papers := make([]portfolio.NewsPaper, 0)
		for _, link := range item.GetInstrumentId() {
			if link == nil || link.GetInstrument() == nil {
				continue
			}
			info := link.GetInstrument()
			papers = append(papers, portfolio.NewsPaper{
				InstrumentUID: info.GetInstrumentUid(),
				Ticker:        info.GetTicker(),
				ClassCode:     info.GetClassCode(),
			})
		}
		out = append(out, portfolio.NewsItem{
			ID:          item.GetId(),
			Source:      item.GetSource(),
			Title:       item.GetTitle(),
			Content:     item.GetContent(),
			Summary:     item.GetSummary(),
			Priority:    item.GetPriority(),
			At:          stampTime(item.GetTs()),
			Instruments: papers,
		})
	}
	return out, nil
}

// FindInstrument ищет бумагу по строке, обычно по тикеру портфеля.
func (c *Client) FindInstrument(ctx context.Context, query string) ([]portfolio.InstrumentHit, error) {
	started := time.Now()
	request := map[string]any{"query": query}
	if err := ctx.Err(); err != nil {
		c.note("FindInstrument", started, request, nil, err)
		return nil, err
	}
	resp, err := c.instruments.FindInstrument(query)
	var body proto.Message
	if resp != nil && resp.FindInstrumentResponse != nil {
		body = resp.FindInstrumentResponse
	}
	c.note("FindInstrument", started, request, body, err)
	if err != nil || resp == nil {
		return nil, err
	}
	out := make([]portfolio.InstrumentHit, 0)
	for _, item := range resp.GetInstruments() {
		if item == nil || item.GetUid() == "" {
			continue
		}
		out = append(out, portfolio.InstrumentHit{
			Query:          query,
			ISIN:           item.GetIsin(),
			Figi:           item.GetFigi(),
			Ticker:         item.GetTicker(),
			ClassCode:      item.GetClassCode(),
			InstrumentType: item.GetInstrumentType(),
			Name:           item.GetName(),
			UID:            item.GetUid(),
			PositionUID:    item.GetPositionUid(),
			Kind:           item.GetInstrumentKind().String(),
			APITrade:       item.GetApiTradeAvailableFlag(),
			ForIIS:         item.GetForIisFlag(),
			ForQual:        item.GetForQualInvestorFlag(),
			Weekend:        item.GetWeekendFlag(),
			BlockedTCA:     item.GetBlockedTcaFlag(),
			Lot:            item.GetLot(),
			First1Min:      stampTime(item.GetFirst_1MinCandleDate()),
			First1Day:      stampTime(item.GetFirst_1DayCandleDate()),
		})
	}
	return out, nil
}

// Countries возвращает справочник стран.
func (c *Client) Countries(ctx context.Context) ([]portfolio.Country, error) {
	started := time.Now()
	request := map[string]any{}
	if err := ctx.Err(); err != nil {
		c.note("GetCountries", started, request, nil, err)
		return nil, err
	}
	resp, err := c.instruments.GetCountries()
	var body proto.Message
	if resp != nil && resp.GetCountriesResponse != nil {
		body = resp.GetCountriesResponse
	}
	c.note("GetCountries", started, request, body, err)
	if err != nil || resp == nil {
		return nil, err
	}
	out := make([]portfolio.Country, 0)
	for _, item := range resp.GetCountries() {
		if item == nil {
			continue
		}
		out = append(out, portfolio.Country{
			AlfaTwo:   item.GetAlfaTwo(),
			AlfaThree: item.GetAlfaThree(),
			Name:      item.GetName(),
			NameBrief: item.GetNameBrief(),
		})
	}
	return out, nil
}

// Brands возвращает одну страницу справочника брендов: обёртка SDK не принимает номер страницы.
func (c *Client) Brands(ctx context.Context) ([]portfolio.Brand, error) {
	started := time.Now()
	request := map[string]any{}
	if err := ctx.Err(); err != nil {
		c.note("GetBrands", started, request, nil, err)
		return nil, err
	}
	resp, err := c.instruments.GetBrands()
	var body proto.Message
	if resp != nil && resp.GetBrandsResponse != nil {
		body = resp.GetBrandsResponse
	}
	c.note("GetBrands", started, request, body, err)
	if err != nil || resp == nil {
		return nil, err
	}
	out := make([]portfolio.Brand, 0)
	for _, item := range resp.GetBrands() {
		if item == nil || item.GetUid() == "" {
			continue
		}
		out = append(out, portfolio.Brand{
			UID:               item.GetUid(),
			Name:              item.GetName(),
			Description:       item.GetDescription(),
			Info:              item.GetInfo(),
			Company:           item.GetCompany(),
			Sector:            item.GetSector(),
			CountryOfRisk:     item.GetCountryOfRisk(),
			CountryOfRiskName: item.GetCountryOfRiskName(),
		})
	}
	return out, nil
}

func fundamentalRows(item *pb.GetAssetFundamentalsResponse_StatisticResponse) []portfolio.Fundamental {
	if item == nil || item.GetAssetUid() == "" {
		return nil
	}
	uid := item.GetAssetUid()
	rows := []portfolio.Fundamental{
		textMetric(uid, "currency", item.GetCurrency()),
		floatMetric(uid, "market_capitalization", item.GetMarketCapitalization()),
		floatMetric(uid, "high_price_last_52_weeks", item.GetHighPriceLast_52Weeks()),
		floatMetric(uid, "low_price_last_52_weeks", item.GetLowPriceLast_52Weeks()),
		floatMetric(uid, "average_daily_volume_last_10_days", item.GetAverageDailyVolumeLast_10Days()),
		floatMetric(uid, "average_daily_volume_last_4_weeks", item.GetAverageDailyVolumeLast_4Weeks()),
		floatMetric(uid, "beta", item.GetBeta()),
		floatMetric(uid, "free_float", item.GetFreeFloat()),
		floatMetric(uid, "forward_annual_dividend_yield", item.GetForwardAnnualDividendYield()),
		floatMetric(uid, "shares_outstanding", item.GetSharesOutstanding()),
		floatMetric(uid, "revenue_ttm", item.GetRevenueTtm()),
		floatMetric(uid, "ebitda_ttm", item.GetEbitdaTtm()),
		floatMetric(uid, "net_income_ttm", item.GetNetIncomeTtm()),
		floatMetric(uid, "eps_ttm", item.GetEpsTtm()),
		floatMetric(uid, "diluted_eps_ttm", item.GetDilutedEpsTtm()),
		floatMetric(uid, "free_cash_flow_ttm", item.GetFreeCashFlowTtm()),
		floatMetric(uid, "five_year_annual_revenue_growth_rate", item.GetFiveYearAnnualRevenueGrowthRate()),
		floatMetric(uid, "three_year_annual_revenue_growth_rate", item.GetThreeYearAnnualRevenueGrowthRate()),
		floatMetric(uid, "pe_ratio_ttm", item.GetPeRatioTtm()),
		floatMetric(uid, "price_to_sales_ttm", item.GetPriceToSalesTtm()),
		floatMetric(uid, "price_to_book_ttm", item.GetPriceToBookTtm()),
		floatMetric(uid, "price_to_free_cash_flow_ttm", item.GetPriceToFreeCashFlowTtm()),
		floatMetric(uid, "total_enterprise_value_mrq", item.GetTotalEnterpriseValueMrq()),
		floatMetric(uid, "ev_to_ebitda_mrq", item.GetEvToEbitdaMrq()),
		floatMetric(uid, "net_margin_mrq", item.GetNetMarginMrq()),
		floatMetric(uid, "net_interest_margin_mrq", item.GetNetInterestMarginMrq()),
		floatMetric(uid, "roe", item.GetRoe()),
		floatMetric(uid, "roa", item.GetRoa()),
		floatMetric(uid, "roic", item.GetRoic()),
		floatMetric(uid, "total_debt_mrq", item.GetTotalDebtMrq()),
		floatMetric(uid, "total_debt_to_equity_mrq", item.GetTotalDebtToEquityMrq()),
		floatMetric(uid, "total_debt_to_ebitda_mrq", item.GetTotalDebtToEbitdaMrq()),
		floatMetric(uid, "free_cash_flow_to_price", item.GetFreeCashFlowToPrice()),
		floatMetric(uid, "net_debt_to_ebitda", item.GetNetDebtToEbitda()),
		floatMetric(uid, "current_ratio_mrq", item.GetCurrentRatioMrq()),
		floatMetric(uid, "fixed_charge_coverage_ratio_fy", item.GetFixedChargeCoverageRatioFy()),
		floatMetric(uid, "dividend_yield_daily_ttm", item.GetDividendYieldDailyTtm()),
		floatMetric(uid, "dividend_rate_ttm", item.GetDividendRateTtm()),
		floatMetric(uid, "dividends_per_share", item.GetDividendsPerShare()),
		floatMetric(uid, "five_years_average_dividend_yield", item.GetFiveYearsAverageDividendYield()),
		floatMetric(uid, "five_year_annual_dividend_growth_rate", item.GetFiveYearAnnualDividendGrowthRate()),
		floatMetric(uid, "dividend_payout_ratio_fy", item.GetDividendPayoutRatioFy()),
		floatMetric(uid, "buy_back_ttm", item.GetBuyBackTtm()),
		floatMetric(uid, "one_year_annual_revenue_growth_rate", item.GetOneYearAnnualRevenueGrowthRate()),
		textMetric(uid, "domicile_indicator_code", item.GetDomicileIndicatorCode()),
		floatMetric(uid, "adr_to_common_share_ratio", item.GetAdrToCommonShareRatio()),
		floatMetric(uid, "number_of_employees", item.GetNumberOfEmployees()),
		timeMetric(uid, "ex_dividend_date", item.GetExDividendDate()),
		timeMetric(uid, "fiscal_period_start_date", item.GetFiscalPeriodStartDate()),
		timeMetric(uid, "fiscal_period_end_date", item.GetFiscalPeriodEndDate()),
		floatMetric(uid, "revenue_change_five_years", item.GetRevenueChangeFiveYears()),
		floatMetric(uid, "eps_change_five_years", item.GetEpsChangeFiveYears()),
		floatMetric(uid, "ebitda_change_five_years", item.GetEbitdaChangeFiveYears()),
		floatMetric(uid, "total_debt_change_five_years", item.GetTotalDebtChangeFiveYears()),
		floatMetric(uid, "ev_to_sales", item.GetEvToSales()),
	}
	return rows
}

func floatMetric(uid, name string, value float64) portfolio.Fundamental {
	return portfolio.Fundamental{AssetUID: uid, Metric: name, Value: strconv.FormatFloat(value, 'g', -1, 64)}
}

func textMetric(uid, name, value string) portfolio.Fundamental {
	return portfolio.Fundamental{AssetUID: uid, Metric: name, Value: value}
}

func timeMetric(uid, name string, stamp *timestamppb.Timestamp) portfolio.Fundamental {
	value := ""
	if stamp != nil {
		value = stamp.AsTime().UTC().Format(time.RFC3339Nano)
	}
	return portfolio.Fundamental{AssetUID: uid, Metric: name, Value: value}
}

// AssetBy читает карточку актива и оставляет только согласованные поля.
func (c *Client) AssetBy(ctx context.Context, assetUID string) (portfolio.AssetCard, error) {
	started := time.Now()
	request := map[string]any{"id": assetUID}
	if err := ctx.Err(); err != nil {
		c.note("GetAssetBy", started, request, nil, err)
		return portfolio.AssetCard{}, err
	}
	resp, err := c.instruments.GetAssetBy(assetUID)
	var body proto.Message
	if resp != nil && resp.AssetResponse != nil {
		body = resp.AssetResponse
	}
	c.note("GetAssetBy", started, request, body, err)
	if err != nil || resp == nil || resp.GetAsset() == nil {
		return portfolio.AssetCard{}, err
	}
	return assetCard(resp.GetAsset()), nil
}

func assetCard(asset *pb.AssetFull) portfolio.AssetCard {
	card := portfolio.AssetCard{AssetUID: asset.GetUid(), Description: asset.GetDescription()}
	if brand := asset.GetBrand(); brand != nil {
		card.Sector = brand.GetSector()
		card.CountryOfRisk = brand.GetCountryOfRisk()
		card.CountryOfRiskName = brand.GetCountryOfRiskName()
		card.BrandDescription = brand.GetDescription()
		card.Company = brand.GetCompany()
	}
	for _, item := range asset.GetInstruments() {
		if item == nil || item.GetUid() == "" {
			continue
		}
		ref := portfolio.AssetInstrumentRef{
			UID:            item.GetUid(),
			Figi:           item.GetFigi(),
			Ticker:         item.GetTicker(),
			ClassCode:      item.GetClassCode(),
			InstrumentType: item.GetInstrumentType(),
		}
		for _, link := range item.GetLinks() {
			if link == nil {
				continue
			}
			ref.Links = append(ref.Links, portfolio.AssetLinkRef{Type: link.GetType(), InstrumentUID: link.GetInstrumentUid()})
		}
		card.Instruments = append(card.Instruments, ref)
	}
	security := asset.GetSecurity()
	if security == nil {
		return card
	}
	if bond := security.GetBond(); bond != nil {
		card.Bond = &portfolio.AssetBondTerms{
			CurrentNominal:  optionalQuotation(bond.GetCurrentNominal()),
			Nominal:         optionalQuotation(bond.GetNominal()),
			NominalCurrency: bond.GetNominalCurrency(),
			IndexedNominal:  bond.GetIndexedNominalFlag(),
			Amortization:    bond.GetAmortizationFlag(),
			FloatingCoupon:  bond.GetFloatingCouponFlag(),
		}
	}
	if fund := security.GetEtf(); fund != nil {
		card.Fund = &portfolio.AssetFundTerms{
			Index:             fund.GetPrimaryIndex(),
			Description:       fund.GetDescription(),
			IndexDescription:  fund.GetPrimaryIndexDescription(),
			TotalExpense:      optionalQuotation(fund.GetTotalExpense()),
			HurdleRate:        optionalQuotation(fund.GetHurdleRate()),
			PerformanceFee:    optionalQuotation(fund.GetPerformanceFee()),
			FixedCommission:   optionalQuotation(fund.GetFixedCommission()),
			ExpenseCommission: optionalQuotation(fund.GetExpenseCommission()),
			BuyPremium:        optionalQuotation(fund.GetBuyPremium()),
			SellDiscount:      optionalQuotation(fund.GetSellDiscount()),
			Watermark:         fund.GetWatermarkFlag(),
		}
	}
	return card
}

func optionalQuotation(value *pb.Quotation) *decimal.Decimal {
	amount, ok := quoteDecimal(value)
	if !ok {
		return nil
	}
	return &amount
}
