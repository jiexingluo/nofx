package market

import (
	"testing"
	"time"
)

func TestParseRSS(t *testing.T) {
	body := []byte(`<?xml version="1.0"?><rss version="2.0"><channel><title>Feed</title>
<item><title><![CDATA[Monero jumps 10% as privacy coins rally]]></title><link>https://x/1</link><pubDate>Thu, 05 Nov 2026 22:10:00 +0000</pubDate></item>
<item><title>Bitcoin &amp; Ether ETFs see outflows</title><link>https://x/2</link><pubDate>Thu, 5 Nov 2026 20:00:00 GMT</pubDate></item>
<item><title>No date here</title><link>https://x/3</link></item>
</channel></rss>`)
	items, err := parseRSS(body, "example.com")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("want 2 dated items, got %d: %+v", len(items), items)
	}
	if items[0].Title != "Monero jumps 10% as privacy coins rally" || items[0].Source != "example.com" {
		t.Fatalf("CDATA title/source not parsed: %+v", items[0])
	}
	if items[1].Title != "Bitcoin & Ether ETFs see outflows" {
		t.Fatalf("HTML entities not unescaped: %q", items[1].Title)
	}
	if want := time.Date(2026, 11, 5, 20, 0, 0, 0, time.UTC); !items[1].Published.Equal(want) {
		t.Fatalf("single-digit-day date parsed as %v, want %v", items[1].Published, want)
	}
}

func TestParseCryptoPanic(t *testing.T) {
	body := []byte(`{"results":[{"title":"Zcash upgrade goes live","published_at":"2026-11-05T21:00:00Z",
"original_url":"https://src/zec","source":{"title":"The Block","domain":"theblock.co"},
"instruments":[{"code":"zec"}],"votes":{"positive":12,"negative":3,"important":5}}]}`)
	items, err := parseCryptoPanic(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("want 1 item, got %d", len(items))
	}
	it := items[0]
	if it.Source != "The Block" || it.URL != "https://src/zec" || it.Positive != 12 || it.Important != 5 ||
		len(it.Currencies) != 1 || it.Currencies[0] != "ZEC" {
		t.Fatalf("unexpected item: %+v", it)
	}
	if !NewsMentions(it, "ZECUSDT") {
		t.Fatal("a CryptoPanic currency tag must count as a mention")
	}
}

func TestNewsMentions(t *testing.T) {
	cases := []struct {
		title, symbol string
		want          bool
	}{
		{"Monero jumps as privacy coins rally", "XMRUSDT", true},
		{"XMR breaks out", "XMRUSDT", true},
		{"Bitcoin price near $80K", "NEARUSDT", false}, // ordinary "near"
		{"NEAR Protocol upgrade", "NEARUSDT", true},
		{"Click the link below", "LINKUSDT", false},
		{"Chainlink partners with bank", "LINKUSDT", true},
		{"PEPE memecoin surges", "1000PEPEUSDT", true},
		{"ETHENA? no: ENAble the switch", "ENAUSDT", false}, // ticker inside a word
		{"Pump.fun buys back tokens", "PUMPUSDT", true},
	}
	for _, c := range cases {
		if got := NewsMentions(NewsItem{Title: c.title}, c.symbol); got != c.want {
			t.Errorf("NewsMentions(%q, %s) = %v, want %v", c.title, c.symbol, got, c.want)
		}
	}
}

func TestParseFearGreedHistoryIsOldestFirst(t *testing.T) {
	body := []byte(`{"name":"Fear and Greed Index","data":[
{"value":"28","value_classification":"Fear","timestamp":"1793836800"},
{"value":"34","value_classification":"Fear","timestamp":"1793750400"}]}`)
	got, err := parseFearGreedHistory(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Value != 34 || got[1].Value != 28 || got[1].Time != 1793836800000 {
		t.Fatalf("want oldest first with ms timestamps, got %+v", got)
	}
}

func TestIsCryptoHeadline(t *testing.T) {
	for title, want := range map[string]bool{
		"Core Scientific to quit Bitcoin mining by year-end":            true,
		"Asian crypto exchanges batten down the hatches as hacks surge": true,
		"Monero jumps as privacy coins rally":                           true,
		"Claude Gets Personal Finance—Pompliano Accuses Anthropic":      false,
		"DOJ Creates 'Super Intelligence' Task Force":                   false,
		"Spot ether ETFs see record outflows":                           true,
		"Whether stocks rise depends on Netflix earnings":               false, // not "ether", not "etf"
		"Definitely a minerals story":                                   false, // not "defi", not "miner"
		"Bitcoin miners sell as hashprice drops":                        true,
	} {
		if got := IsCryptoHeadline(NewsItem{Title: title}); got != want {
			t.Errorf("IsCryptoHeadline(%q) = %v, want %v", title, got, want)
		}
	}
}
