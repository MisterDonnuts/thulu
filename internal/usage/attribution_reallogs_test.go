package usage

import (
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/engine"
)

// Run logs contain private code; never commit them to the repo.
func TestAttribute_RealLogs(t *testing.T) {
	dir := os.Getenv("OCTOPUS_ATTRIBUTION_LOGS")
	if dir == "" {
		t.Skip("OCTOPUS_ATTRIBUTION_LOGS not set")
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.log"))
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		in := AttributionInput{Prices: func(m string) (Pricing, bool) {
			p, ok := engine.PriceFor(m)
			return fromEnginePrice(p), ok
		}}
		for ev := range engine.MustGet("").ParseStream(strings.NewReader(string(raw))) {
			in.Events = append(in.Events, ev)
			if ev.Kind == engine.EventInit {
				in.Model = ev.Model
			}
			if ev.Kind == engine.EventResult {
				in.CostUSD += ev.CostUSD
				if ev.Usage != nil {
					in.OutputTokens += ev.Usage.OutputTokens
				}
			}
		}
		a := Attribute(in)
		var sum float64
		for _, s := range a.Categories {
			sum += s.CostUSD
		}
		if math.Abs(sum-in.CostUSD) > 0.0001 {
			t.Errorf("%s: categories add up to %.4f, run cost %.4f", filepath.Base(f), sum, in.CostUSD)
		}
		var tools float64
		for _, tc := range a.Tools {
			if tc.ParentToolUseID == "" {
				tools += tc.CostUSD
			}
		}
		if got := a.Categories[CategoryTools].CostUSD + a.Categories[CategorySubagents].CostUSD; math.Abs(got-tools) > 0.0001 {
			t.Errorf("%s: tool rows %.4f vs tools+subagents %.4f", filepath.Base(f), tools, got)
		}
		if os.Getenv("OCTOPUS_ATTRIBUTION_VERBOSE") != "" {
			cats := make([]string, 0, len(a.Categories))
			for _, c := range Categories {
				if s, ok := a.Categories[c]; ok {
					cats = append(cats, c+"="+trimF(s.CostUSD/in.CostUSD*100)+"%")
				}
			}
			t.Logf("%s cost=$%.2f calls=%d base=%d compactions=%d %s", filepath.Base(f)[:30], in.CostUSD, len(a.Calls), a.BaseTokens, a.Compactions, strings.Join(cats, " "))
			top := make([]*ToolCost, 0, len(a.Tools))
			for _, tc := range a.Tools {
				top = append(top, tc)
			}
			sort.Slice(top, func(i, j int) bool { return top[i].CostUSD > top[j].CostUSD })
			for _, tc := range top[:min(3, len(top))] {
				t.Logf("   top %s result=%dtok carried=%d $%.3f", tc.ToolUseID[:12], tc.ResultTokens, tc.CarriedCalls, tc.CostUSD)
			}
		}
	}
}

func trimF(f float64) string { return strings.TrimRight(strings.TrimRight(strconvF(f), "0"), ".") }

func strconvF(f float64) string { return strconv.FormatFloat(f, 'f', 1, 64) }
