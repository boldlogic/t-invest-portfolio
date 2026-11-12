package brokerreport

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func TestDocsReportsNormalize(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("..", "..", "docs", "broker-report-*.xlsx"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) < 6 {
		t.Fatalf("found %d sample reports, want at least 6", len(files))
	}
	for _, path := range files {
		t.Run(filepath.Base(path), func(t *testing.T) {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			book, err := ReadWorkbook(data)
			if err != nil {
				t.Fatal(err)
			}
			digest := sha256.Sum256(data)
			records, err := Normalize(book, hex.EncodeToString(digest[:]))
			if err != nil {
				t.Fatal(err)
			}
			if records.ReportAccount == "" || records.PeriodEnd.IsZero() {
				t.Fatalf("missing report identity/period")
			}
		})
	}
}

func TestRepoLegsAreIndependentTradeKeys(t *testing.T) {
	if TradeKey(Trade{Number: "deal-7", RepoPart: "1"}) == TradeKey(Trade{Number: "deal-7", RepoPart: "2"}) {
		t.Fatal("repo legs share a key")
	}
}

func TestAssetCodeSeparatesISINFromTicker(t *testing.T) {
	ticker, isin := normalizeAssetCode(" us69269l1044 ")
	if ticker != "" || isin != "US69269L1044" {
		t.Fatalf("normalized ISIN as ticker=%q isin=%q", ticker, isin)
	}
	ticker, isin = normalizeAssetCode("SBMX")
	if ticker != "SBMX" || isin != "" {
		t.Fatalf("normalized ticker=%q isin=%q", ticker, isin)
	}
}
