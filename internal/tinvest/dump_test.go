package tinvest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	pb "opensource.tbank.ru/invest/invest-go/proto"
)

func TestDumpScrubsSecretAndKeepsResponse(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "api-dump.jsonl")
	dump, err := OpenDump(path)
	if err != nil {
		t.Fatal(err)
	}
	client := &Client{dump: dump}
	client.NoteSession("invest-public-api.tinkoff.ru:443", "")
	client.note("GetAccounts", time.Now(), nil, &pb.PortfolioResponse{AccountId: "acc-1"},
		errString("rpc: Bearer t.abcdefghijklmnopqrstuvwxyz012345"))
	if err := dump.Close(); err != nil {
		t.Fatal(err)
	}

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if strings.Contains(text, "abcdefghijklmnopqrstuvwxyz") || strings.Contains(text, "Bearer t.") {
		t.Fatalf("секрет остался в логе: %s", text)
	}
	if !strings.Contains(text, "скрыто") || !strings.Contains(text, `"accountId":"acc-1"`) {
		t.Fatalf("лог = %s", text)
	}
	if !strings.Contains(text, "invest-public-api.tinkoff.ru:443") {
		t.Fatalf("сессия не записана: %s", text)
	}
}

type errString string

func (e errString) Error() string { return string(e) }
