package portfolio

import "testing"

func TestSubscriptionFromPositionSkipsCash(t *testing.T) {
	if _, ok := SubscriptionFromPosition(Position{Figi: CashRUBFigi, Ticker: "RUB", InstrumentType: "currency"}); ok {
		t.Fatal("cash position subscribed")
	}
	if _, ok := SubscriptionFromPosition(Position{InstrumentType: "share"}); ok {
		t.Fatal("position without uid and ticker subscribed")
	}
	item, ok := SubscriptionFromPosition(Position{InstrumentUID: "uid", Ticker: "FIVE", ClassCode: "TQBR", InstrumentType: "share"})
	if !ok || item.InstrumentUID != "uid" || item.ClassCode != "TQBR" {
		t.Fatalf("item = %+v, ok = %v", item, ok)
	}
}
