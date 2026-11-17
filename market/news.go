package market

import (
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// Crypto news headlines for the daily trend AI review. Free RSS feeds need no
// key; CryptoPanic is added when a token is configured (it tags each post
// with coins and community votes, but its free plan may no longer exist).

// NewsItem is one headline.
type NewsItem struct {
	Title      string
	Source     string
	URL        string
	Published  time.Time
	Currencies []string // CryptoPanic only
	Positive   int      // CryptoPanic votes
	Negative   int
	Important  int
}

// DefaultNewsFeeds are free crypto news RSS feeds; every one answered from
// the production server on 2026-11-17. CryptoPanic's free Developer plan was
// discontinued (~April 2026; paid plans start at $199/month), so breadth here
// is what gives the review per-coin coverage.
var DefaultNewsFeeds = []string{
	"https://cointelegraph.com/rss",
	"https://decrypt.co/feed",
	"https://www.coindesk.com/arc/outboundfeeds/rss/",
	"https://www.theblock.co/rss.xml",
	"https://cryptoslate.com/feed/",
	"https://beincrypto.com/feed/",
	"https://cryptobriefing.com/feed/",
	"https://www.newsbtc.com/feed/",
	"https://bitcoinmagazine.com/feed",
}

var newsHTTPClient = &http.Client{Timeout: 15 * time.Second}

// FetchRSSNews returns headlines published after since from every feed that
// answered, newest first, plus one error per feed that did not.
func FetchRSSNews(feeds []string, since time.Time) ([]NewsItem, []error) {
	var items []NewsItem
	var errs []error
	for _, feed := range feeds {
		req, err := http.NewRequest("GET", feed, nil)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		req.Header.Set("User-Agent", "Mozilla/5.0 (nofx news reader)")
		resp, err := newsHTTPClient.Do(req)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", feed, err))
			continue
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		resp.Body.Close()
		if err != nil || resp.StatusCode != http.StatusOK {
			errs = append(errs, fmt.Errorf("%s: HTTP %d %v", feed, resp.StatusCode, err))
			continue
		}
		host := feed
		if u, err := url.Parse(feed); err == nil {
			host = strings.TrimPrefix(u.Host, "www.")
		}
		parsed, err := parseRSS(body, host)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", feed, err))
			continue
		}
		for _, it := range parsed {
			if it.Published.After(since) {
				items = append(items, it)
			}
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Published.After(items[j].Published) })
	return items, errs
}

var rssDateLayouts = []string{
	time.RFC1123Z, time.RFC1123,
	"Mon, 2 Jan 2006 15:04:05 -0700", "Mon, 2 Jan 2006 15:04:05 MST",
	time.RFC3339,
}

func parseRSS(body []byte, source string) ([]NewsItem, error) {
	var doc struct {
		Channel struct {
			Items []struct {
				Title   string `xml:"title"`
				Link    string `xml:"link"`
				PubDate string `xml:"pubDate"`
			} `xml:"item"`
		} `xml:"channel"`
	}
	if err := xml.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("parse RSS: %w", err)
	}
	out := make([]NewsItem, 0, len(doc.Channel.Items))
	for _, it := range doc.Channel.Items {
		var published time.Time
		for _, layout := range rssDateLayouts {
			if t, err := time.Parse(layout, strings.TrimSpace(it.PubDate)); err == nil {
				published = t.UTC()
				break
			}
		}
		title := strings.TrimSpace(html.UnescapeString(it.Title))
		if title == "" || published.IsZero() {
			continue
		}
		out = append(out, NewsItem{Title: title, Source: source, URL: strings.TrimSpace(it.Link), Published: published})
	}
	return out, nil
}

// FetchCryptoPanicNews returns recent CryptoPanic news posts, optionally for
// the given coin codes. plan is the API plan path segment ("developer" by
// default). Errors never contain the token.
func FetchCryptoPanicNews(token, plan string, currencies []string) ([]NewsItem, error) {
	if token == "" {
		return nil, errors.New("CryptoPanic token not configured")
	}
	if plan == "" {
		plan = "developer"
	}
	q := url.Values{"auth_token": {token}, "kind": {"news"}, "public": {"true"}}
	if len(currencies) > 0 {
		q.Set("currencies", strings.Join(currencies, ","))
	}
	resp, err := newsHTTPClient.Get(fmt.Sprintf("https://cryptopanic.com/api/%s/v2/posts/?%s", url.PathEscape(plan), q.Encode()))
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err // drop the URL: it carries the token
		}
		return nil, fmt.Errorf("CryptoPanic request failed: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("CryptoPanic read failed: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("CryptoPanic HTTP %d", resp.StatusCode)
	}
	return parseCryptoPanic(body)
}

func parseCryptoPanic(body []byte) ([]NewsItem, error) {
	type code struct {
		Code string `json:"code"`
	}
	var resp struct {
		Results []struct {
			Title       string `json:"title"`
			PublishedAt string `json:"published_at"`
			URL         string `json:"url"`
			OriginalURL string `json:"original_url"`
			Source      struct {
				Title  string `json:"title"`
				Domain string `json:"domain"`
			} `json:"source"`
			Currencies  []code `json:"currencies"`
			Instruments []code `json:"instruments"`
			Votes       struct {
				Positive  int `json:"positive"`
				Negative  int `json:"negative"`
				Important int `json:"important"`
			} `json:"votes"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("parse CryptoPanic response: %w", err)
	}
	out := make([]NewsItem, 0, len(resp.Results))
	for _, r := range resp.Results {
		published, _ := time.Parse(time.RFC3339, r.PublishedAt)
		item := NewsItem{
			Title: strings.TrimSpace(r.Title), URL: r.OriginalURL, Published: published.UTC(),
			Source: r.Source.Title, Positive: r.Votes.Positive, Negative: r.Votes.Negative, Important: r.Votes.Important,
		}
		if item.URL == "" {
			item.URL = r.URL
		}
		if item.Source == "" {
			item.Source = r.Source.Domain
		}
		for _, c := range append(r.Currencies, r.Instruments...) {
			item.Currencies = append(item.Currencies, strings.ToUpper(c.Code))
		}
		if item.Title != "" {
			out = append(out, item)
		}
	}
	return out, nil
}

// coinNewsNames are the case-sensitive words a headline must contain to be
// about a coin. Tickers are matched in capitals only, so "Near" or "link" in
// ordinary English does not count as NEAR or LINK.
var coinNewsNames = map[string][]string{
	"BTC": {"Bitcoin", "BTC"}, "ETH": {"Ethereum", "Ether", "ETH"}, "SOL": {"Solana", "SOL"},
	"XRP": {"XRP", "Ripple"}, "BNB": {"BNB"}, "DOGE": {"Dogecoin", "DOGE"}, "ADA": {"Cardano", "ADA"},
	"AVAX": {"Avalanche", "AVAX"}, "LINK": {"Chainlink", "LINK"}, "SUI": {"Sui", "SUI"},
	"HYPE": {"Hyperliquid", "HYPE"}, "ZEC": {"Zcash", "ZEC"}, "NEAR": {"NEAR"}, "ENA": {"Ethena", "ENA", "USDe"},
	"XMR": {"Monero", "XMR"}, "WLD": {"Worldcoin", "WLD"}, "LIT": {"Lighter", "LIT"}, "UNI": {"Uniswap", "UNI"},
	"ZRO": {"LayerZero", "ZRO"}, "XPL": {"Plasma", "XPL"}, "VVV": {"Venice", "VVV"}, "PUMP": {"Pump.fun", "pump.fun", "PUMP"},
	"PEPE": {"Pepe", "PEPE"},
}

// Terms that mark a headline as crypto-market news, matched case-insensitively.
// Stems must start a word ("hack" matches "hacks", not "whack"); short terms
// must be the whole word ("etf" must not match "Netflix", nor "defi"
// "definitely"). General feeds such as decrypt.co also carry AI and politics
// stories that are noise for a trade review.
var (
	cryptoHeadlineStems = []string{"crypto", "bitcoin", "ether", "stablecoin", "blockchain", "token", "altcoin",
		"memecoin", "exchange", "hack", "exploit", "liquidat", "on-chain", "onchain", "web3", "mining"}
	cryptoHeadlineWords = []string{"defi", "etf", "etfs", "nft", "nfts", "dao", "miner", "miners"}
)

// IsCryptoHeadline reports whether a headline is about crypto markets: it
// names a tracked coin or uses a crypto-market term.
func IsCryptoHeadline(item NewsItem) bool {
	if len(item.Currencies) > 0 {
		return true
	}
	lower := strings.ToLower(item.Title)
	for _, stem := range cryptoHeadlineStems {
		if hasWordPrefix(lower, stem) {
			return true
		}
	}
	for _, word := range cryptoHeadlineWords {
		if containsWord(lower, word) {
			return true
		}
	}
	for base := range coinNewsNames {
		if NewsMentions(item, base+"USDT") {
			return true
		}
	}
	return false
}

// NewsBaseAsset maps an exchange symbol to the coin code news uses
// (BTCUSDT → BTC, 1000PEPEUSDT → PEPE).
func NewsBaseAsset(symbol string) string {
	return strings.TrimPrefix(strings.TrimSuffix(strings.ToUpper(symbol), "USDT"), "1000")
}

// NewsMentions reports whether a headline is about the coin traded as symbol.
func NewsMentions(item NewsItem, symbol string) bool {
	base := NewsBaseAsset(symbol)
	for _, c := range item.Currencies {
		if c == base {
			return true
		}
	}
	names := coinNewsNames[base]
	if len(names) == 0 {
		names = []string{base}
	}
	for _, name := range names {
		if containsWord(item.Title, name) {
			return true
		}
	}
	return false
}

func containsWord(text, word string) bool {
	for start := 0; ; {
		i := strings.Index(text[start:], word)
		if i < 0 {
			return false
		}
		i += start
		end := i + len(word)
		if (i == 0 || !isWordByte(text[i-1])) && (end == len(text) || !isWordByte(text[end])) {
			return true
		}
		start = i + 1
	}
}

// hasWordPrefix reports whether some word in text starts with prefix.
func hasWordPrefix(text, prefix string) bool {
	for start := 0; ; {
		i := strings.Index(text[start:], prefix)
		if i < 0 {
			return false
		}
		i += start
		if i == 0 || !isWordByte(text[i-1]) {
			return true
		}
		start = i + 1
	}
}

func isWordByte(b byte) bool {
	return b == '_' || b >= '0' && b <= '9' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z'
}
