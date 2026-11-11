package main

import (
	"math"
	"testing"
)

// Requirement: one statistics function for every Lab comparison (fleet-lab.md, Statistics): paired difference per
// example, a 95% interval clustered by example, n always, "too few examples" without an interval; typed answers report
// coverage, selective accuracy and escalation share per tag. Risk: a comparison that averages repeats as independent
// examples (an interval too narrow), or a decision drawn from one example.
func TestStatisticsOnSyntheticLabelledSet(t *testing.T) {
	near := func(got, want float64) bool { return math.Abs(got-want) < 1e-6 }
	// The t quantile against tables: df 1 -> 12.706, df 4 -> 2.776, df 30 -> 2.042.
	for df, want := range map[float64]float64{1: 12.7062, 4: 2.7764, 30: 2.0423} {
		if q := tQuantile(0.975, df); math.Abs(q-want) > 1e-3 {
			t.Fatalf("t(0.975, %v) = %v, want %v", df, q, want)
		}
	}
	// Five examples; the variant's repeats of e1 are averaged (0.5), never counted as two examples.
	base := map[string][]float64{"e1": {0}, "e2": {0}, "e3": {1}, "e4": {0}, "e5": {1}, "only-base": {1}}
	variant := map[string][]float64{"e1": {1, 0}, "e2": {1}, "e3": {1}, "e4": {1}, "e5": {1}}
	c := compare(base, variant)
	if c.N != 5 || c.TooFew || !near(*c.Mean, 0.5) {
		t.Fatalf("compare = %+v", c)
	}
	// deltas 0.5 1 0 1 0: sd = 0.5, half width = 2.7764 * 0.5 / sqrt 5.
	half := 2.7764451 * 0.5 / math.Sqrt(5)
	if math.Abs(*c.Low-(0.5-half)) > 1e-4 || math.Abs(*c.High-(0.5+half)) > 1e-4 {
		t.Fatalf("interval %v..%v, want 0.5±%v", *c.Low, *c.High, half)
	}
	if one := compare(map[string][]float64{"e1": {0}}, map[string][]float64{"e1": {1}}); !one.TooFew || one.Low != nil || one.N != 1 {
		t.Fatalf("one example: %+v", one)
	}
	// Bands 0.8 / 0.2 over a labelled set in two languages.
	as := []typedAnswer{}
	for _, r := range []struct {
		tag, label string
		p          float64
	}{{"ru", "yes", 0.95}, {"ru", "yes", 0.5}, {"ru", "no", 0.1}, {"en", "no", 0.9}, {"en", "no", 0.05}} {
		as = append(as, typedAnswer{r.tag, bandAnswer(r.p, 0.8, 0.2), r.label})
	}
	got := map[string]typedStat{}
	for _, s := range typedStats(as) {
		got[s.Tag] = s
	}
	ru, en, all := got["ru"], got["en"], got["all"]
	if ru.N != 3 || !near(ru.Coverage, 2.0/3) || !near(*ru.SelectiveAccuracy, 1) || !near(ru.Escalation, 1.0/3) {
		t.Fatalf("ru: %+v", ru)
	}
	if en.N != 2 || !near(en.Coverage, 1) || !near(*en.SelectiveAccuracy, 0.5) || all.N != 5 || !near(*all.SelectiveAccuracy, 0.75) {
		t.Fatalf("en %+v all %+v", en, all)
	}
}
