package store

import (
	"context"
	"encoding/json"
	"testing"

	"t-invest-portfolio/internal/portfolio"
)

func TestSubscriptionKeepsOneRowAndFillsUID(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	if err := db.EnsureSubscription(ctx, portfolio.Subscription{}); err != nil {
		t.Fatal(err)
	}
	if err := db.EnsureSubscription(ctx, portfolio.Subscription{Ticker: "FIVE", ClassCode: "TQBR"}); err != nil {
		t.Fatal(err)
	}
	if err := db.EnsureSubscription(ctx, portfolio.Subscription{InstrumentUID: "uid-five", Ticker: "FIVE", ClassCode: "TQBR", Figi: "BBG"}); err != nil {
		t.Fatal(err)
	}
	if err := db.EnsureSubscription(ctx, portfolio.Subscription{InstrumentUID: "uid-five", Ticker: "FIVE", ClassCode: "TQBR"}); err != nil {
		t.Fatal(err)
	}
	if err := db.EnsureSubscription(ctx, portfolio.Subscription{InstrumentUID: "uid-gold", Ticker: "GOLD", ClassCode: "TQTF"}); err != nil {
		t.Fatal(err)
	}
	items, err := db.ListSubscriptions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("rows = %d, want 2", len(items))
	}
	var five portfolio.Subscription
	for _, item := range items {
		if item.Ticker == "FIVE" {
			five = item
		}
	}
	if five.InstrumentUID != "uid-five" || five.Figi != "BBG" {
		t.Fatalf("five = %+v", five)
	}
}

func TestBackfillSubscriptionsSkipsCash(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	if _, err := db.sql.Exec(`INSERT INTO trade_record(account_id,trade_number,repo_part,executed_at,instrument_uid,ticker) VALUES('acc','n1',0,'2026-01-01T00:00:00Z','uid-trade','SBER')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.Exec(`INSERT INTO operation_record(operation_key,account_id,executed_at,type,payment_amount,quantity) VALUES('op','acc','2026-01-02T00:00:00Z','buy','1',1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.Exec(`INSERT INTO operation_fill(operation_key,fill_index,instrument_uid,ticker,price_amount,quantity) VALUES('op',0,'uid-fill','GAZP',1,1)`); err != nil {
		t.Fatal(err)
	}
	cash, _ := json.Marshal(portfolio.Position{Figi: portfolio.CashRUBFigi, Ticker: "RUB", InstrumentType: "currency", Cash: true})
	share, _ := json.Marshal(portfolio.Position{InstrumentUID: "uid-share", Ticker: "LKOH", ClassCode: "TQBR", InstrumentType: "share"})
	if err := db.ReplaceCurrent(ctx, "position", "acc", []Record{
		{Key: "cash", Payload: json.RawMessage(cash)},
		{Key: "uid-share", Payload: json.RawMessage(share)},
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.BackfillSubscriptions(ctx); err != nil {
		t.Fatal(err)
	}
	if err := db.BackfillSubscriptions(ctx); err != nil {
		t.Fatal(err)
	}
	items, err := db.ListSubscriptions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 {
		t.Fatalf("rows = %d, want 3: %+v", len(items), items)
	}
	for _, item := range items {
		if item.Ticker == "RUB" {
			t.Fatalf("cash subscribed: %+v", item)
		}
	}
}
