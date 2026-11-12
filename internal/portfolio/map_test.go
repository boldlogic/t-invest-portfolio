package portfolio

import (
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"
	pb "opensource.tbank.ru/invest/invest-go/proto"
)

func TestUnitsNano(t *testing.T) {
	tests := []struct {
		name  string
		units int64
		nano  int32
		want  string
	}{
		{"копейки", 247, 848333000, "247.848333"},
		{"точность_девять_знаков", 1, 1, "1.000000001"},
		{"отрицательная цена", -2, -250000000, "-2.25"},
		{"ноль", 0, 0, "0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := unitsNano(tt.units, tt.nano)
			if got.String() != tt.want {
				t.Fatalf("unitsNano() = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestMapPortfolio(t *testing.T) {
	expire := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	meta := AccountMeta{ID: "acc", Name: "Основной", Type: "ACCOUNT_TYPE_TINKOFF"}

	account, err := MapPortfolio(meta, &pb.PortfolioResponse{
		TotalAmountShares:     rub(700),
		TotalAmountCurrencies: rub(300),
		TotalAmountPortfolio:  rub(1000),
		ExpectedYield:         &pb.Quotation{Units: 1, Nano: 200000000},
		Positions: []*pb.PortfolioPosition{
			{
				Figi:           CashRUBFigi,
				InstrumentType: "currency",
				Quantity:       &pb.Quotation{Units: 300},
				CurrentPrice:   rub(1),
			},
			{
				Figi:                 "BBG004730N88",
				Ticker:               "SBER",
				InstrumentType:       "share",
				InstrumentUid:        "share-uid",
				Quantity:             &pb.Quotation{Units: 2},
				CurrentPrice:         rub(100),
				AveragePositionPrice: &pb.MoneyValue{Currency: "rub", Units: 90},
				ExpectedYield:        &pb.Quotation{Units: 20},
				Blocked:              true,
				BlockedLots:          &pb.Quotation{Units: 1},
			},
			{
				Figi:           "BOND",
				InstrumentType: "bond",
				Quantity:       &pb.Quotation{Units: 2},
				CurrentPrice:   rub(100),
				CurrentNkd:     &pb.MoneyValue{Currency: "rub", Units: 3, Nano: 500000000},
			},
			{
				Figi:           "USD",
				InstrumentType: "share",
				Quantity:       &pb.Quotation{Units: 1},
				CurrentPrice:   &pb.MoneyValue{Currency: "usd", Units: 10},
			},
		},
		VirtualPositions: []*pb.VirtualPortfolioPosition{
			{
				Figi:         "VIRT",
				Ticker:       "GIFT",
				Quantity:     &pb.Quotation{Units: 5},
				CurrentPrice: rub(1),
				ExpireDate:   timestamppb.New(expire),
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	if !account.Positions[0].Cash || account.Positions[0].Ticker != "RUB" {
		t.Fatalf("кэш: %+v", account.Positions[0])
	}
	if account.Positions[0].MarketValue.Amount.String() != "300" {
		t.Fatalf("кэш оценка = %s", account.Positions[0].MarketValue.Amount)
	}

	share := account.Positions[1]
	if !share.Blocked || share.BlockedLots.String() != "1" {
		t.Fatalf("блокировка: %+v", share)
	}
	if share.MarketValue.Amount.String() != "200" {
		t.Fatalf("акция оценка = %s", share.MarketValue.Amount)
	}

	bond := account.Positions[2]
	if bond.MarketValue.Amount.String() != "207" {
		t.Fatalf("облигация с НКД = %s, want 207", bond.MarketValue.Amount)
	}

	if !account.ValueCheckPartial {
		t.Fatal("позиция в USD должна делать сверку неполной")
	}
	// 300 + 200 + 207 = 707, USD не входит. Итог брокера 1000.
	if account.PositionsValue.Amount.String() != "707" {
		t.Fatalf("сумма сверки = %s", account.PositionsValue.Amount)
	}
	if account.TotalDelta.Amount.String() != "-293" {
		t.Fatalf("дельта = %s", account.TotalDelta.Amount)
	}
	if !account.Totals.ExpectedYield.Known || account.Totals.ExpectedYield.Value.String() != "1.2" {
		t.Fatalf("доходность = %+v", account.Totals.ExpectedYield)
	}

	if len(account.VirtualPositions) != 1 || !account.VirtualPositions[0].HasExpireDate {
		t.Fatalf("виртуальная позиция: %+v", account.VirtualPositions)
	}
	if !account.VirtualPositions[0].ExpireDate.Equal(expire) {
		t.Fatalf("expire = %s", account.VirtualPositions[0].ExpireDate)
	}
	if len(account.Positions) != 4 {
		t.Fatalf("виртуальная позиция попала в обычные: %d", len(account.Positions))
	}
}

func TestMapPortfolioForeignTotal(t *testing.T) {
	_, err := MapPortfolio(AccountMeta{ID: "acc"}, &pb.PortfolioResponse{
		TotalAmountPortfolio: &pb.MoneyValue{Currency: "usd", Units: 1},
	})
	if err == nil {
		t.Fatal("ожидалась ошибка чужой валюты")
	}
	if !strings.Contains(err.Error(), "RUB") || !strings.Contains(err.Error(), "USD") {
		t.Fatalf("ошибка = %v", err)
	}
}

func TestMapPortfolioNKDCurrencyMismatch(t *testing.T) {
	account, err := MapPortfolio(AccountMeta{ID: "acc"}, &pb.PortfolioResponse{
		TotalAmountPortfolio: rub(200),
		Positions: []*pb.PortfolioPosition{{
			Ticker:         "OFZ",
			InstrumentType: "bond",
			Quantity:       &pb.Quotation{Units: 2},
			CurrentPrice:   rub(100),
			CurrentNkd:     &pb.MoneyValue{Currency: "usd", Units: 1},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if account.Positions[0].MarketValue.Amount.String() != "200" {
		t.Fatalf("оценка = %s, НКД в другой валюте не должен прибавляться", account.Positions[0].MarketValue.Amount)
	}
	if len(account.Warnings) != 1 {
		t.Fatalf("warnings = %v", account.Warnings)
	}
}

func TestMapPortfolioAbsentYield(t *testing.T) {
	account, err := MapPortfolio(AccountMeta{ID: "acc", Type: "ACCOUNT_TYPE_TINKOFF_IIS"}, &pb.PortfolioResponse{
		TotalAmountPortfolio: rub(0),
	})
	if err != nil {
		t.Fatal(err)
	}
	if account.Totals.ExpectedYield.Known {
		t.Fatal("пустая доходность не должна становиться нулём с Known=true")
	}
	if account.TypeLabel != "ИИС" {
		t.Fatalf("label = %s", account.TypeLabel)
	}
	if account.TotalDelta.Amount.String() != "0" {
		t.Fatalf("дельта = %s", account.TotalDelta.Amount)
	}
}

func rub(units int64) *pb.MoneyValue {
	return &pb.MoneyValue{Currency: "rub", Units: units}
}
