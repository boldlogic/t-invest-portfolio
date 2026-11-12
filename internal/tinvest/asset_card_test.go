package tinvest

import (
	"encoding/json"
	"strings"
	"testing"

	pb "opensource.tbank.ru/invest/invest-go/proto"
)

func TestAssetCardKeepsAgreedFields(t *testing.T) {
	share := assetCard(&pb.AssetFull{
		Uid:         "asset-share",
		Description: "актив",
		Name:        "не писать",
		Brand: &pb.Brand{
			Uid: "brand", Name: "не писать", Description: "бренд", Info: "справка",
			Company: "компания", Sector: "энергетика", CountryOfRisk: "RU", CountryOfRiskName: "Россия",
		},
		Instruments: []*pb.AssetInstrument{
			{Ticker: "SKIP"},
			{Uid: "uid-1", Ticker: "FIVE", Figi: "F", ClassCode: "TQBR", InstrumentType: "share", Links: []*pb.InstrumentLink{
				{Type: "base", InstrumentUid: ""},
			}},
		},
		Ext: &pb.AssetFull_Security{Security: &pb.AssetSecurity{
			Isin: "RU000",
			Ext:  &pb.AssetSecurity_Share{Share: &pb.AssetShare{Nominal: &pb.Quotation{Units: 1}}},
		}},
	})
	raw, err := json.Marshal(share)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, unwanted := range []string{"не писать", "справка", "RU000", "bond", "fund"} {
		if strings.Contains(text, unwanted) {
			t.Fatalf("card contains %q: %s", unwanted, text)
		}
	}
	if share.Company != "компания" || share.Description != "актив" || share.BrandDescription != "бренд" {
		t.Fatalf("texts = %+v", share)
	}
	if len(share.Instruments) != 1 || len(share.Instruments[0].Links) != 1 || share.Instruments[0].Links[0].InstrumentUID != "" {
		t.Fatalf("instruments = %+v", share.Instruments)
	}

	bond := assetCard(&pb.AssetFull{
		Uid: "asset-bond",
		Ext: &pb.AssetFull_Security{Security: &pb.AssetSecurity{Ext: &pb.AssetSecurity_Bond{Bond: &pb.AssetBond{
			CurrentNominal: &pb.Quotation{Units: 900}, Nominal: &pb.Quotation{Units: 1000}, NominalCurrency: "USD",
			IndexedNominalFlag: true, AmortizationFlag: true, FloatingCouponFlag: false, SubordinatedFlag: true,
		}}}},
	})
	raw, err = json.Marshal(bond)
	if err != nil {
		t.Fatal(err)
	}
	text = string(raw)
	if strings.Contains(text, "subordinat") {
		t.Fatalf("bond card kept a field outside the list: %s", text)
	}
	if bond.Bond == nil || !bond.Bond.IndexedNominal || !bond.Bond.Amortization || bond.Bond.FloatingCoupon || bond.Bond.NominalCurrency != "USD" {
		t.Fatalf("bond = %+v", bond.Bond)
	}

	fund := assetCard(&pb.AssetFull{
		Uid: "asset-fund",
		Ext: &pb.AssetFull_Security{Security: &pb.AssetSecurity{Ext: &pb.AssetSecurity_Etf{Etf: &pb.AssetEtf{
			PrimaryIndex: "IMOEX", Description: "фонд", PrimaryIndexDescription: "индекс",
			TotalExpense: &pb.Quotation{Units: 1}, WatermarkFlag: false, LeveragedFlag: true,
		}}}},
	})
	raw, err = json.Marshal(fund)
	if err != nil {
		t.Fatal(err)
	}
	text = string(raw)
	if strings.Contains(text, "leveraged") || fund.Fund == nil || fund.Fund.Index != "IMOEX" || fund.Fund.Description != "фонд" || fund.Fund.IndexDescription != "индекс" {
		t.Fatalf("fund = %s", text)
	}
	if fund.Fund.TotalExpense == nil || fund.Fund.Watermark {
		t.Fatalf("commissions = %+v", fund.Fund)
	}
}
