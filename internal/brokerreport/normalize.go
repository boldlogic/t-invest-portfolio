package brokerreport

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"strings"
	"time"
)

var ErrInvalidReport = errors.New("неподдерживаемая структура отчёта")

type Trade struct {
	Number, RepoPart, Ticker, ISIN, Name, Side, Currency, Quantity, Price, Amount string
	At                                                                            time.Time
	Sheet                                                                         string
	Row                                                                           int
}
type Operation struct {
	Type, RawType, Currency, Amount, Note, Key string
	At                                         time.Time
	Sheet                                      string
	Row                                        int
}
type Records struct {
	ReportAccount          string
	PeriodStart, PeriodEnd time.Time
	Trades                 []Trade
	Operations             []Operation
}

var periodPattern = regexp.MustCompile("(\\d{2}\\.\\d{2}\\.\\d{4})\\s*[-–—]\\s*(\\d{2}\\.\\d{2}\\.\\d{4})")
var reportNumberPattern = regexp.MustCompile("(?:/|№)\\s*([[:alnum:]А-Яа-яЁё./-]+)\\s+от\\s+\\d{2}\\.\\d{2}\\.\\d{4}")
var isinPattern = regexp.MustCompile(`(?i)^[A-Z]{2}[A-Z0-9]{9}[0-9]$`)

func Normalize(book Workbook, fileHash string) (Records, error) {
	var out Records
	for _, sheet := range book.Sheets {
		for _, row := range sheet.Rows {
			for _, cell := range row {
				v := strings.TrimSpace(strings.ReplaceAll(cell, "\n", " "))
				if out.ReportAccount == "" && strings.Contains(strings.ToLower(v), "инвестор:") {
					if m := reportNumberPattern.FindStringSubmatch(v); len(m) == 2 {
						out.ReportAccount = strings.TrimSpace(m[1])
					}
				}
				if out.PeriodStart.IsZero() && strings.Contains(strings.ToLower(v), "отчет о сделках") {
					if m := periodPattern.FindStringSubmatch(v); len(m) == 3 {
						out.PeriodStart, _ = time.Parse("02.01.2006", m[1])
						out.PeriodEnd, _ = time.Parse("02.01.2006", m[2])
					}
				}
			}
		}
		trades, err := parseTrades(sheet)
		if err != nil {
			return Records{}, err
		}
		ops, err := parseOperations(sheet, fileHash)
		if err != nil {
			return Records{}, err
		}
		out.Trades = append(out.Trades, trades...)
		out.Operations = append(out.Operations, ops...)
	}
	if out.ReportAccount == "" {
		return Records{}, fmt.Errorf("не удалось определить номер счёта из строки «Инвестор»")
	}
	if out.PeriodStart.IsZero() || out.PeriodEnd.IsZero() || out.PeriodEnd.Before(out.PeriodStart) {
		return Records{}, fmt.Errorf("не удалось определить период отчёта")
	}
	if err := validateDuplicates(out.Trades); err != nil {
		return Records{}, err
	}
	return out, nil
}

func parseTrades(sheet Sheet) ([]Trade, error) {
	var out []Trade
	var h map[string]int
	inExecutedSection := false
	for ri, row := range sheet.Rows {
		for _, v := range row {
			section := strings.TrimSpace(strings.ToLower(v))
			switch {
			case strings.HasPrefix(section, "1.1 "):
				inExecutedSection = true
				h = nil
			case strings.HasPrefix(section, "1.2 "), strings.HasPrefix(section, "1.3 "), strings.HasPrefix(section, "2. "):
				inExecutedSection = false
				h = nil
			}
		}
		if !inExecutedSection {
			continue
		}
		if found := findTradeHeader(row); found != nil {
			h = found
			continue
		}
		if h == nil {
			continue
		}
		date, number := cell(row, h["дата заключения"]), strings.TrimSpace(cell(row, h["номер сделки"]))
		if number == "" || date == "" {
			continue
		}
		at, err := reportDateTime(date, cell(row, h["время"]))
		if err != nil {
			return nil, fmt.Errorf("лист %q строка %d: %w", sheet.Name, ri+1, err)
		}
		side := normalizeSide(cell(row, h["вид сделки"]))
		if side == "" {
			continue
		}
		ticker, isin := normalizeAssetCode(cell(row, h["код актива"]))
		qty, price := decimalText(cell(row, h["количество"])), decimalText(cell(row, h["цена за единицу"]))
		if (ticker == "" && isin == "") || qty == "" || price == "" {
			return nil, fmt.Errorf("лист %q строка %d: отсутствует код/количество/цена (%t/%t/%t), колонки %v", sheet.Name, ri+1, ticker != "", qty != "", price != "", h)
		}
		amount := ""
		if i, ok := h["сумма сделки"]; ok {
			amount = decimalText(cell(row, i))
		}
		if amount == "" {
			q, _ := new(big.Rat).SetString(qty)
			p, _ := new(big.Rat).SetString(price)
			amount = new(big.Rat).Mul(q, p).FloatString(8)
		}
		repoPart := ""
		for _, v := range row {
			low := strings.ToLower(strings.ReplaceAll(v, " ", ""))
			if strings.Contains(low, "репо") && (strings.Contains(low, "ч.1") || strings.Contains(low, "ч1")) {
				repoPart = "1"
			}
			if strings.Contains(low, "репо") && (strings.Contains(low, "ч.2") || strings.Contains(low, "ч2")) {
				repoPart = "2"
			}
		}
		out = append(out, Trade{Number: number, RepoPart: repoPart, Ticker: ticker, ISIN: isin, Name: strings.TrimSpace(cell(row, h["наименование актива"])), Side: side,
			Currency: strings.TrimSpace(cell(row, h["валюта цены"])), Quantity: qty, Price: price, Amount: amount, At: at, Sheet: sheet.Name, Row: ri + 1})
	}
	return out, nil
}

func normalizeAssetCode(value string) (ticker, isin string) {
	code := strings.ToUpper(strings.TrimSpace(value))
	if isinPattern.MatchString(code) {
		return "", code
	}
	return strings.TrimSpace(value), ""
}

func findTradeHeader(row []string) map[string]int {
	want := []string{"номер сделки", "дата заключения", "вид сделки", "код актива", "цена за единицу", "количество"}
	got := map[string]int{}
	for i, v := range row {
		n := header(v)
		for _, key := range want {
			if strings.Contains(n, key) {
				got[key] = i
			}
		}
		if strings.HasPrefix(n, "время") {
			got["время"] = i
		}
		if strings.HasPrefix(n, "наименование актива") {
			got["наименование актива"] = i
		}
		if strings.HasPrefix(n, "валюта цены") {
			got["валюта цены"] = i
		}
		if strings.HasPrefix(n, "сумма сделки") {
			got["сумма сделки"] = i
		}
	}
	for _, key := range want {
		if _, ok := got[key]; !ok {
			return nil
		}
	}
	return got
}

func parseOperations(sheet Sheet, hash string) ([]Operation, error) {
	var out []Operation
	var h map[string]int
	currency := ""
	for ri, row := range sheet.Rows {
		if found := findOperationHeader(row); found != nil {
			h = found
			continue
		}
		if h == nil {
			continue
		}
		if v := strings.TrimSpace(cell(row, 0)); isCurrency(v) {
			currency = strings.ToUpper(v)
		}
		rawType := strings.TrimSpace(cell(row, h["операция"]))
		if rawType == "" || strings.Contains(strings.ToLower(rawType), "итого") {
			continue
		}
		date := strings.TrimSpace(cell(row, h["дата"]))
		if date == "" {
			date = strings.TrimSpace(cell(row, h["дата исполнения"]))
		}
		if date == "" {
			continue
		}
		at, err := reportDateTime(date, cell(row, h["время совершения"]))
		if err != nil {
			return nil, fmt.Errorf("лист %q строка %d: %w", sheet.Name, ri+1, err)
		}
		credit, err := parseAmount(cell(row, h["сумма зачисления"]))
		if err != nil {
			return nil, fmt.Errorf("лист %q строка %d: зачисление: %w", sheet.Name, ri+1, err)
		}
		debit, err := parseAmount(cell(row, h["сумма списания"]))
		if err != nil {
			return nil, fmt.Errorf("лист %q строка %d: списание: %w", sheet.Name, ri+1, err)
		}
		if credit == "" && debit == "" {
			continue
		}
		cr, _ := new(big.Rat).SetString(defaultZero(credit))
		db, _ := new(big.Rat).SetString(defaultZero(debit))
		sum := new(big.Rat).Sub(cr, db).FloatString(8)
		keyData := fmt.Sprintf("%s\x00%s\x00%d", hash, sheet.Name, ri+1)
		digest := sha256.Sum256([]byte(keyData))
		out = append(out, Operation{Type: normalizeOperationType(rawType), RawType: rawType, Currency: currency, Amount: sum,
			Note: strings.TrimSpace(cell(row, h["примечание"])), Key: hex.EncodeToString(digest[:]), At: at, Sheet: sheet.Name, Row: ri + 1})
	}
	return out, nil
}

func findOperationHeader(row []string) map[string]int {
	want := []string{"дата", "время совершения", "операция", "сумма зачисления", "сумма списания"}
	got := map[string]int{}
	for i, v := range row {
		n := header(v)
		for _, key := range want {
			if strings.Contains(n, key) {
				got[key] = i
			}
		}
		if strings.HasPrefix(n, "дата исполнения") {
			got["дата исполнения"] = i
		}
		if strings.HasPrefix(n, "примечание") {
			got["примечание"] = i
		}
	}
	for _, key := range want {
		if _, ok := got[key]; !ok {
			return nil
		}
	}
	return got
}

func validateDuplicates(trades []Trade) error {
	seen := map[string]Trade{}
	for _, tr := range trades {
		key := TradeKey(tr)
		if previous, ok := seen[key]; ok && (previous.Ticker != tr.Ticker || previous.ISIN != tr.ISIN || !previous.At.Equal(tr.At) || previous.Side != tr.Side || previous.Quantity != tr.Quantity || previous.Price != tr.Price) {
			return fmt.Errorf("конфликт повторного номера сделки %q в отчёте", tr.Number)
		}
		seen[key] = tr
	}
	return nil
}

func normalizeOperationType(v string) string {
	n := strings.ToLower(strings.Join(strings.Fields(v), " "))
	switch {
	case strings.Contains(n, "пополнение"):
		return "input"
	case strings.Contains(n, "вывод средств"):
		return "output"
	case strings.Contains(n, "налог"):
		return "tax"
	case strings.Contains(n, "комисси"):
		return "commission"
	case strings.Contains(n, "покупка/продажа"):
		return "trade_settlement"
	case strings.Contains(n, "выплат") || strings.Contains(n, "дивиденд") || strings.Contains(n, "купон"):
		return "income"
	case strings.Contains(n, "перевод"):
		return "transfer"
	default:
		return n
	}
}
func normalizeSide(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "покупка":
		return "BUY"
	case "продажа":
		return "SELL"
	}
	return ""
}
func reportDateTime(d, t string) (time.Time, error) {
	date, err := time.Parse("02.01.2006", strings.TrimSpace(d))
	if err != nil {
		return time.Time{}, fmt.Errorf("неверная дата %q", d)
	}
	clock := strings.TrimSpace(t)
	if clock == "" {
		clock = "00:00:00"
	}
	if len(clock) == 5 {
		clock += ":00"
	}
	parsed, err := time.Parse("15:04:05", clock)
	if err != nil {
		return time.Time{}, fmt.Errorf("неверное время %q", t)
	}
	return time.Date(date.Year(), date.Month(), date.Day(), parsed.Hour(), parsed.Minute(), parsed.Second(), 0, time.FixedZone("MSK", 3*60*60)), nil
}
func parseAmount(v string) (string, error) {
	v = decimalText(v)
	if v == "" {
		return "", nil
	}
	if _, ok := new(big.Rat).SetString(v); !ok {
		return "", fmt.Errorf("неверная сумма")
	}
	return v, nil
}
func decimalText(v string) string {
	v = strings.NewReplacer("\u00a0", "", "\u202f", "", " ", "", ",", ".").Replace(strings.TrimSpace(v))
	if _, ok := new(big.Rat).SetString(v); !ok {
		return ""
	}
	return v
}
func defaultZero(v string) string {
	if v == "" {
		return "0"
	}
	return v
}
func cell(row []string, i int) string {
	if i < 0 || i >= len(row) {
		return ""
	}
	return strings.TrimSpace(row[i])
}
func header(v string) string {
	return strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(v)), " "))
}
func isCurrency(v string) bool {
	switch strings.ToUpper(v) {
	case "RUB", "USD", "EUR", "CNY", "KZT", "HKD", "GBP", "CHF", "JPY", "TRY", "AMD", "BYN", "GEL", "UZS":
		return true
	}
	return false
}
func TradeKey(t Trade) string {
	return strings.ToUpper(strings.TrimSpace(t.Number)) + "\x00" + t.RepoPart
}
