package portfolio

// Subscription — бумага, которая уже была в портфеле или в сделке.
// Строка не удаляется, когда позиция закрылась.
type Subscription struct {
	InstrumentUID string
	Ticker        string
	ClassCode     string
	Figi          string
	AssetUID      string
}

// SubscriptionFromPosition берёт бумагу из позиции портфеля.
// Денежный остаток и строка без uid и тикера в подписку не входят.
func SubscriptionFromPosition(position Position) (Subscription, bool) {
	if position.Cash || isCash(position.Figi, position.InstrumentType) {
		return Subscription{}, false
	}
	item := Subscription{
		InstrumentUID: position.InstrumentUID,
		Ticker:        position.Ticker,
		ClassCode:     position.ClassCode,
		Figi:          position.Figi,
		AssetUID:      position.AssetUID,
	}
	if item.InstrumentUID == "" && item.Ticker == "" {
		return Subscription{}, false
	}
	return item, true
}
