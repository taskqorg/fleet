package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"fleet/contract"
)

type priceSource struct {
	name, url, document string
	parse               func(string, time.Time) ([]contract.PriceRefreshChange, []string)
}

var providerPriceSources = []priceSource{
	{"OpenAI", "https://developers.openai.com/api/docs/pricing", "https://developers.openai.com/api/docs/pricing.md", openAIPrices},
	{"Anthropic", "https://platform.claude.com/docs/en/about-claude/pricing", "https://platform.claude.com/docs/en/about-claude/pricing.md", anthropicPrices},
	{"Google Gemini", "https://ai.google.dev/gemini-api/docs/pricing", "https://aistudio.google.com/docs/pricing.md", geminiPrices},
}

var providerPriceClient = &http.Client{
	Timeout:       10 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

func fetchProviderPrice(ctx context.Context, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := providerPriceClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	// Refuse oversized documents without interpreting a truncated table as a complete source.
	const maxDocument = 2 << 20
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxDocument+1))
	if err != nil {
		return "", err
	}
	if len(body) > maxDocument {
		return "", fmt.Errorf("pricing document exceeds %d bytes", maxDocument)
	}
	return string(body), nil
}

type markdownRows map[string][][]string

// Match the whole heading line and stop at the next heading at the same or higher level.
func priceSection(page, heading string) (string, bool) {
	lines := strings.Split(strings.ReplaceAll(page, "\r\n", "\n"), "\n")
	level := strings.IndexByte(heading, ' ')
	start := -1
	for i, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == heading {
			if start >= 0 {
				return "", false
			}
			start = i
		}
	}
	if start < 0 {
		return "", false
	}
	end := len(lines)
	for i := start + 1; i < end; i++ {
		line := strings.TrimSpace(lines[i])
		if n := strings.IndexByte(line, ' '); n > 0 && n <= level && line[:n] == strings.Repeat("#", n) {
			end = i
			break
		}
	}
	return strings.Join(lines[start+1:end], "\n"), true
}

var markdownSeparator = regexp.MustCompile(`^:?-{3,}:?$`)

func pricingRows(section string, headers []string) (markdownRows, bool) {
	rows := markdownRows{}
	table := false
	matched := 0
	lines := strings.Split(section, "\n")
	for i, line := range lines {
		cells := priceCells(line)
		if len(cells) == 0 {
			table = false
			continue
		}
		if strings.Join(cells, "|") == strings.Join(headers, "|") {
			if i+1 == len(lines) || !priceSeparator(priceCells(lines[i+1]), len(headers)) {
				return nil, false
			}
			matched++
			table = true
			continue
		}
		if table && !priceSeparator(cells, len(headers)) {
			rows[cells[0]] = append(rows[cells[0]], cells[1:])
		}
	}
	return rows, matched == 1
}

func priceCells(line string) []string {
	line = strings.TrimSpace(line)
	if len(line) < 2 || line[0] != '|' || line[len(line)-1] != '|' {
		return nil
	}
	cells := strings.Split(line[1:len(line)-1], "|")
	for i := range cells {
		cells[i] = strings.TrimSpace(cells[i])
	}
	return cells
}

func priceSeparator(cells []string, width int) bool {
	if len(cells) != width {
		return false
	}
	for _, cell := range cells {
		if !markdownSeparator.MatchString(cell) {
			return false
		}
	}
	return true
}

var usdMillion = regexp.MustCompile(`^\$([0-9]+(?:\.[0-9]+)?)$`)
var anthropicMillion = regexp.MustCompile(`^(\$[0-9]+(?:\.[0-9]+)?) / MTok(?:<sup>[0-9]+</sup>)?$`)

func tokenRate(subject, unit, amount string) (contract.PriceRefreshChange, bool) {
	match := usdMillion.FindStringSubmatch(amount)
	if match == nil {
		return contract.PriceRefreshChange{}, false
	}
	price, err := strconv.ParseFloat(match[1]+"e-6", 64)
	if err != nil || (price == 0 && strings.Trim(match[1], "0.") != "") {
		return contract.PriceRefreshChange{}, false
	}
	return contract.PriceRefreshChange{Subject: subject, Unit: unit, Price: price, Currency: "USD"}, true
}

func openAIPrices(page string, _ time.Time) ([]contract.PriceRefreshChange, []string) {
	section, found := priceSection(page, "### Standard pricing data")
	rows, ok := pricingRows(section, []string{"Model", "Short context input", "Short context output", "Short context cached input",
		"Short context 30m write", "Short context 12h write", "Long context input", "Long context output",
		"Long context cached input", "Long context 30m write", "Long context 12h write"})
	if !found || !ok || !strings.Contains(page, "Prices per 1M tokens.") {
		return nil, []string{"Standard USD per-million-token context table missing, ambiguous or changed"}
	}
	gaps := []string{"Long-context rates and 30m/12h cache-write rates require per-request context and retention data"}
	var rates []contract.PriceRefreshChange
	for _, model := range []string{"gpt-6.2-sol", "gpt-6.1-sol"} {
		modelRates, modelGaps := readTokenRow(rows[model], model, 10, []int{0, 1, 2}, usdMillion)
		rates, gaps = append(rates, modelRates...), append(gaps, modelGaps...)
	}
	return rates, gaps
}

func anthropicPrices(page string, _ time.Time) ([]contract.PriceRefreshChange, []string) {
	section, found := priceSection(page, "## Model pricing")
	rows, ok := pricingRows(section, []string{"Model", "Base input tokens", "5m cache writes", "1h cache writes", "Cache hits and refreshes", "Output tokens"})
	if !found || !ok || !strings.Contains(page, "All prices are in USD.") {
		return nil, []string{"Standard USD per-million-token model table missing, ambiguous or changed"}
	}
	gaps := []string{"5m/1h cache-write rates require retention data"}
	var rates []contract.PriceRefreshChange
	for _, model := range []struct{ id, label string }{{"claude-sonnet-5-5", "Claude Sonnet 5.5"}, {"claude-opus-5-5", "Claude Opus 5.5"}} {
		modelRates, modelGaps := readTokenRow(rows[model.label], model.id, 5, []int{0, 4, 3}, anthropicMillion)
		rates, gaps = append(rates, modelRates...), append(gaps, modelGaps...)
	}
	return rates, gaps
}

func readTokenRow(rows [][]string, model string, width int, columns []int, pattern *regexp.Regexp) ([]contract.PriceRefreshChange, []string) {
	if len(rows) != 1 || len(rows[0]) != width {
		return nil, []string{model + ": exact Standard row missing, ambiguous or changed"}
	}
	var rates []contract.PriceRefreshChange
	var gaps []string
	for i, column := range columns {
		cell := rows[0][column]
		if pattern == anthropicMillion {
			match := pattern.FindStringSubmatch(cell)
			if match != nil {
				cell = match[1]
			}
		}
		if rate, ok := tokenRate(model, priceUnits[i], cell); ok && pattern.MatchString(rows[0][column]) {
			rates = append(rates, rate)
		} else {
			gaps = append(gaps, model+" "+priceUnits[i]+": no uniform USD per-million-token rate")
		}
	}
	return rates, gaps
}

var geminiTransition = regexp.MustCompile(`^(\$[0-9]+(?:\.[0-9]+)?) through ([A-Z][a-z]+ [0-9]{1,2}, [0-9]{4})\.\n(\$[0-9]+(?:\.[0-9]+)?) starting ([A-Z][a-z]+ [0-9]{1,2}, [0-9]{4})\.$`)
var geminiStorage = regexp.MustCompile(`^\$[0-9]+(?:\.[0-9]+)? / 1,000,000 tokens per hour \(storage price\)(?: (?:through|starting) [A-Z][a-z]+ [0-9]{1,2}, [0-9]{4}\.)?$`)

func geminiPrices(page string, now time.Time) ([]contract.PriceRefreshChange, []string) {
	const model = "gemini-3.8-flash"
	section, found := priceSection(page, `## Gemini 3.8 Flash  {% id="gemini-3.8-flash" %}`)
	_, standard, tab := strings.Cut(section, `{% tab title="Standard" %}`)
	table, _, closed := strings.Cut(standard, "{% /tab %}")
	_, table, opened := strings.Cut(table, "{% table %}")
	table, _, ended := strings.Cut(table, "{% /table %}")
	blocks := strings.Split(strings.ReplaceAll(table, "\r\n", "\n"), "  ---")
	if !found || !strings.Contains(section, "[`"+model+"`](/docs/models/"+model+")") ||
		!tab || !closed || !opened || !ended || strings.Count(section, `{% tab title="Standard" %}`) != 1 ||
		strings.TrimSpace(blocks[0]) != "* Feature\n  * Free Tier\n  * Paid Tier, per 1M tokens in USD" {
		return nil, []string{model + ": Standard paid USD token table missing, ambiguous or changed"}
	}
	cells := map[string][]string{}
	for _, block := range blocks[1:] {
		parts := strings.Split("\n  "+strings.TrimSpace(block), "\n  * ")
		if len(parts) == 4 {
			cells[parts[1]] = append(cells[parts[1]], parts[3])
		}
	}
	gaps := []string{model + ": cache storage priced per token-hour has no Fleet usage unit"}
	var rates []contract.PriceRefreshChange
	for i, label := range []string{"Input price", "Output price (including thinking tokens)", "Context caching price"} {
		unit := priceUnits[i]
		var parsed []contract.PriceRefreshChange
		if cell := cells[label]; len(cell) == 1 {
			lines := []string{}
			for _, line := range strings.Split(strings.TrimSpace(cell[0]), "\n") {
				line = strings.TrimSpace(line)
				if unit != "cache_read_tokens" || !geminiStorage.MatchString(line) {
					lines = append(lines, line)
				}
			}
			parsed = geminiTokenRates(model, unit, strings.Join(lines, "\n"), now)
		}
		if parsed == nil {
			gaps = append(gaps, model+" "+unit+": no uniform Standard USD token rate or confirmed date transition")
		}
		rates = append(rates, parsed...)
	}
	return rates, gaps
}

func geminiTokenRates(model, unit, value string, now time.Time) []contract.PriceRefreshChange {
	if rate, ok := tokenRate(model, unit, value); ok {
		return []contract.PriceRefreshChange{rate}
	}
	match := geminiTransition.FindStringSubmatch(value)
	if match == nil {
		return nil
	}
	last, errLast := time.Parse("January 2, 2006", match[2])
	next, errNext := time.Parse("January 2, 2006", match[4])
	old, oldOK := tokenRate(model, unit, match[1])
	newRate, newOK := tokenRate(model, unit, match[3])
	if errLast != nil || errNext != nil || !last.AddDate(0, 0, 1).Equal(next) || !oldOK || !newOK {
		return nil
	}
	effective := next.UnixMilli()
	newRate.EffectiveFrom = &effective
	if now.Before(next) {
		return []contract.PriceRefreshChange{old, newRate}
	}
	return []contract.PriceRefreshChange{newRate}
}
