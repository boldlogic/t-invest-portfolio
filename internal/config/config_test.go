package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFromEnv(t *testing.T) {
	t.Run("нет токена", func(t *testing.T) {
		t.Setenv("TINVEST_TOKEN", "  ")
		t.Setenv("TINKOFF_TOKEN", "")
		t.Setenv("TINVEST_ENDPOINT", ProdEndpoint)
		_, err := FromEnv()
		if err == nil {
			t.Fatal("ожидалась ошибка")
		}
	})

	t.Run("нет строки подключения", func(t *testing.T) {
		t.Chdir(t.TempDir())
		t.Setenv("TINVEST_TOKEN", "secret-token")
		t.Setenv("TINKOFF_TOKEN", "")
		t.Setenv("TINVEST_SQL_DSN", "")
		if _, err := FromEnv(); err == nil {
			t.Fatal("ожидалась ошибка")
		}
	})

	t.Run("прод по умолчанию", func(t *testing.T) {
		t.Setenv("TINVEST_TOKEN", "secret-token")
		t.Setenv("TINVEST_SQL_DSN", "server=localhost;database=portfolio")
		t.Setenv("TINVEST_ENDPOINT", "")
		t.Setenv("TINKOFF_SANDBOX", "")
		t.Setenv("TINVEST_CA_FILE", "")
		t.Setenv("TINVEST_ACCOUNT_ID", " acc-1 ")

		cfg, err := FromEnv()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Token != "secret-token" {
			t.Fatalf("token = %q", cfg.Token)
		}
		if cfg.Endpoint != ProdEndpoint {
			t.Fatalf("endpoint = %q", cfg.Endpoint)
		}
		if cfg.AccountID != "acc-1" {
			t.Fatalf("account = %q", cfg.AccountID)
		}
		if cfg.AppName != appName {
			t.Fatalf("app = %q", cfg.AppName)
		}
	})

	t.Run("явная песочница", func(t *testing.T) {
		t.Setenv("TINVEST_TOKEN", "secret-token")
		t.Setenv("TINVEST_SQL_DSN", "server=localhost;database=portfolio")
		t.Setenv("TINVEST_ENDPOINT", SandboxEndpoint)
		t.Setenv("TINVEST_ACCOUNT_ID", "")

		cfg, err := FromEnv()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Endpoint != SandboxEndpoint {
			t.Fatalf("endpoint = %q", cfg.Endpoint)
		}
		if cfg.AccountID != "" {
			t.Fatalf("account = %q", cfg.AccountID)
		}
	})
}

func TestFromEnvFile(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("TINKOFF_TOKEN=from-file\nTINVEST_SQL_DSN=server=localhost;database=portfolio\nTINKOFF_SANDBOX=true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TINVEST_TOKEN", "")
	t.Setenv("TINKOFF_TOKEN", "")
	t.Setenv("TINVEST_ENDPOINT", "")
	t.Setenv("TINKOFF_SANDBOX", "")
	t.Setenv("TINVEST_ACCOUNT_ID", "")

	cfg, err := FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Token != "from-file" {
		t.Fatalf("token = %q", cfg.Token)
	}
	if cfg.Endpoint != SandboxEndpoint {
		t.Fatalf("endpoint = %q", cfg.Endpoint)
	}
}

func TestProcessEnvOverridesFile(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("TINKOFF_TOKEN=from-file\nTINVEST_SQL_DSN=server=localhost;database=portfolio\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TINVEST_TOKEN", "from-process")
	t.Setenv("TINKOFF_TOKEN", "")
	t.Setenv("TINKOFF_SANDBOX", "false")
	t.Setenv("TINVEST_ENDPOINT", "")

	cfg, err := FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Token != "from-process" {
		t.Fatalf("token = %q", cfg.Token)
	}
	if cfg.Endpoint != ProdEndpoint {
		t.Fatalf("endpoint = %q", cfg.Endpoint)
	}
}

func TestSandboxFlag(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("TINKOFF_TOKEN=from-file\nTINVEST_SQL_DSN=server=localhost;database=portfolio\nTINKOFF_SANDBOX=true\nTINVEST_ENDPOINT=custom.example:443\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TINVEST_TOKEN", "secret-token")
	t.Setenv("TINKOFF_TOKEN", "")
	t.Setenv("TINKOFF_SANDBOX", "")
	t.Setenv("TINVEST_ENDPOINT", "")

	cfg, err := FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Endpoint != "custom.example:443" {
		t.Fatalf("явный адрес не перекрыл флаг: %q", cfg.Endpoint)
	}

	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("TINKOFF_TOKEN=from-file\nTINKOFF_SANDBOX=maybe\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TINKOFF_SANDBOX", "")
	t.Setenv("TINVEST_ENDPOINT", "")
	if _, err := FromEnv(); err == nil {
		t.Fatal("ожидалась ошибка флага")
	}
}

func TestReportAPIAddrFromEnv(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("TINVEST_REPORT_API_ADDR", "")
	addr, err := ReportAPIAddrFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if addr != "127.0.0.1:8082" {
		t.Fatalf("default report API address=%q", addr)
	}
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("TINVEST_REPORT_API_ADDR=127.0.0.1:9090\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	addr, err = ReportAPIAddrFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if addr != "127.0.0.1:9090" {
		t.Fatalf("file report API address=%q", addr)
	}
	t.Setenv("TINVEST_REPORT_API_ADDR", "127.0.0.1:8083")
	addr, err = ReportAPIAddrFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if addr != "127.0.0.1:8083" {
		t.Fatalf("process report API address=%q", addr)
	}
}

func TestPollIntervals(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("TINVEST_TOKEN", "test")
	t.Setenv("TINVEST_SQL_DSN", "server=localhost;database=portfolio")
	t.Setenv("TINVEST_POLL_PORTFOLIO", "90s")
	cfg, err := FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PollIntervals["portfolio"].String() != "1m30s" {
		t.Fatalf("portfolio interval = %v", cfg.PollIntervals["portfolio"])
	}
	if cfg.PollIntervals["operations"].String() != "1m0s" {
		t.Fatalf("operations default = %v", cfg.PollIntervals["operations"])
	}
	t.Setenv("TINVEST_POLL_MARGIN", "0s")
	if _, err := FromEnv(); err == nil {
		t.Fatal("zero interval should be rejected")
	}
}
