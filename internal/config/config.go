// Package config читает параметры запуска из окружения и из .env в текущем каталоге.
// Файл .env в git не входит.
package config

import (
	"fmt"
	"os"
	"strings"
	"time"
)

const (
	// ProdEndpoint — боевой gRPC-контур, тот же хост, что у tinkoff-connector при TINKOFF_SANDBOX=false.
	ProdEndpoint = "invest-public-api.tinkoff.ru:443"
	// SandboxEndpoint — песочница, тот же хост, что у tinkoff-connector при TINKOFF_SANDBOX=true.
	SandboxEndpoint = "sandbox-invest-public-api.tinkoff.ru:443"

	appName = "t-invest-portfolio"
)

// Config — параметры клиента. Token не логировать и не печатать.
type Config struct {
	Token         string
	Endpoint      string
	AccountID     string
	AppName       string
	CAFile        string
	SQLDSN        string
	PollIntervals map[string]time.Duration
}

var defaultPollIntervals = map[string]time.Duration{
	"accounts":            5 * time.Minute,
	"profile":             24 * time.Hour,
	"portfolio":           time.Minute,
	"positions":           time.Minute,
	"withdraw_limits":     time.Minute,
	"margin":              time.Minute,
	"operations":          time.Minute,
	"orders":              time.Minute,
	"stop_orders":         time.Minute,
	"last_prices":         time.Minute,
	"close_prices":        time.Minute,
	"trading_statuses":    time.Minute,
	"instrument_metadata": 24 * time.Hour,
	"share_passport":      24 * time.Hour,
	"etf_passport":        24 * time.Hour,
	"bond_passport":       24 * time.Hour,
	"bond_coupons":        24 * time.Hour,
	"bond_events":         24 * time.Hour,
	"dividends":           24 * time.Hour,
	"accrued_interests":   24 * time.Hour,
	"risk_rates":          24 * time.Hour,
	"trading_schedule":    24 * time.Hour,
	"fundamentals":        24 * time.Hour,
	"insider_deals":       24 * time.Hour,
	"news":                24 * time.Hour,
	"instrument_search":   24 * time.Hour,
	"countries":           7 * 24 * time.Hour,
	"brands":              7 * 24 * time.Hour,
	"asset_card":          24 * time.Hour,
}

// FromEnv читает токен, адрес и счёт.
// Переменная процесса важнее той же переменной в .env.
// Токен берётся из TINVEST_TOKEN, а если её нет — из TINKOFF_TOKEN.
// Без токена возвращает ошибку. Адрес по умолчанию — боевой контур.
func FromEnv() (Config, error) {
	file, err := loadDotEnv()
	if err != nil {
		return Config{}, err
	}

	token := firstValue(file, "TINVEST_TOKEN", "TINKOFF_TOKEN")
	if token == "" {
		return Config{}, fmt.Errorf("задайте TINVEST_TOKEN или TINKOFF_TOKEN в окружении или в .env (токен только для чтения)")
	}

	sqlDSN := firstValue(file, "TINVEST_SQL_DSN")
	if sqlDSN == "" {
		return Config{}, fmt.Errorf("задайте TINVEST_SQL_DSN в окружении или в .env")
	}

	endpoint, err := resolveEndpoint(file)
	if err != nil {
		return Config{}, err
	}

	intervals, err := pollIntervals(file)
	if err != nil {
		return Config{}, err
	}

	return Config{
		Token:         token,
		Endpoint:      endpoint,
		AccountID:     firstValue(file, "TINVEST_ACCOUNT_ID"),
		AppName:       appName,
		CAFile:        resolveCAFile(file),
		SQLDSN:        sqlDSN,
		PollIntervals: intervals,
	}, nil
}

// SQLDSNFromEnv reads only the shared SQL Server DSN for the report API.
// It deliberately does not require a T-Invest API token.
func SQLDSNFromEnv() (string, error) {
	file, err := loadDotEnv()
	if err != nil {
		return "", err
	}
	dsn := firstValue(file, "TINVEST_SQL_DSN")
	if dsn == "" {
		return "", fmt.Errorf("задайте TINVEST_SQL_DSN в окружении или в .env")
	}
	return dsn, nil
}

// ReportAPIAddrFromEnv reads the local HTTP bind address; it never exposes it
// to the T-Invest gRPC client.
func ReportAPIAddrFromEnv() (string, error) {
	file, err := loadDotEnv()
	if err != nil {
		return "", err
	}
	addr := firstValue(file, "TINVEST_REPORT_API_ADDR")
	if addr == "" {
		addr = "127.0.0.1:8082"
	}
	return addr, nil
}

func pollIntervals(file map[string]string) (map[string]time.Duration, error) {
	intervals := make(map[string]time.Duration, len(defaultPollIntervals))
	for task, fallback := range defaultPollIntervals {
		key := "TINVEST_POLL_" + strings.ToUpper(task)
		value := firstValue(file, key)
		if value == "" {
			intervals[task] = fallback
			continue
		}
		duration, err := time.ParseDuration(value)
		if err != nil || duration <= 0 {
			return nil, fmt.Errorf("%s: ожидалась положительная длительность, например 60s или 5m", key)
		}
		intervals[task] = duration
	}
	return intervals, nil
}

// DefaultCAFile — официальный Russian Trusted Root CA, PEM с Госуслуг.
// Нужен, пока API отдаёт сертификат Минцифры, а в хранилище Windows этого корня нет.
const DefaultCAFile = "certs/russian_trusted_root_ca.pem"

func resolveCAFile(file map[string]string) string {
	if path := firstValue(file, "TINVEST_CA_FILE"); path != "" {
		return path
	}
	if _, err := os.Stat(DefaultCAFile); err == nil {
		return DefaultCAFile
	}
	return ""
}

// resolveEndpoint повторяет выбор хоста в tinkoff-connector.
// Явный TINVEST_ENDPOINT перекрывает флаг. Иначе TINKOFF_SANDBOX=true
// даёт песочницу, false и пустое значение — бой. В docker-compose у коннектора
// запасное значение флага true; здесь пустой флаг означает бой.
func resolveEndpoint(file map[string]string) (string, error) {
	if endpoint := firstValue(file, "TINVEST_ENDPOINT"); endpoint != "" {
		return endpoint, nil
	}
	sandbox, err := parseSandbox(firstValue(file, "TINKOFF_SANDBOX"))
	if err != nil {
		return "", err
	}
	if sandbox {
		return SandboxEndpoint, nil
	}
	return ProdEndpoint, nil
}

func parseSandbox(value string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "false", "0", "no":
		return false, nil
	case "true", "1", "yes":
		return true, nil
	default:
		return false, fmt.Errorf("TINKOFF_SANDBOX: ожидалось true или false, получено %q", value)
	}
}

func firstValue(file map[string]string, keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
		if value := strings.TrimSpace(file[key]); value != "" {
			return value
		}
	}
	return ""
}

func loadDotEnv() (map[string]string, error) {
	data, err := os.ReadFile(".env")
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]string{}, nil
		}
		return nil, fmt.Errorf("чтение .env: %w", err)
	}

	values := map[string]string{}
	for number, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(strings.TrimRight(line, "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		if !ok || key == "" {
			return nil, fmt.Errorf(".env:%d: ожидалась пара КЛЮЧ=значение", number+1)
		}
		values[key] = unquote(strings.TrimSpace(value))
	}
	return values, nil
}

func unquote(value string) string {
	if len(value) < 2 {
		return value
	}
	if value[0] == '"' && value[len(value)-1] == '"' {
		return value[1 : len(value)-1]
	}
	if value[0] == '\'' && value[len(value)-1] == '\'' {
		return value[1 : len(value)-1]
	}
	return value
}
