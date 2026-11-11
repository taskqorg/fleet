package main

// Lab statistics (docs/designs/fleet-lab.md, Statistics): the one function every comparison uses. Two variants on one
// suite and one evaluator version: the per-example paired difference, its mean, a 95% interval clustered by example
// (one difference per example, repeats averaged first), and n. Typed answers add coverage, selective accuracy and
// escalation share per example tag.

import (
	"math"
	"os"
	"sort"
	"strconv"
)

// minExamples: below it the interval means nothing and is not drawn ("too few examples"). A setting with a permissive
// default: 2 is the smallest n with a spread.
func minExamples() int {
	if n, err := strconv.Atoi(os.Getenv("FLEET_LAB_MIN_EXAMPLES")); err == nil && n >= 2 {
		return n
	}
	return 2
}

type exampleDelta struct {
	Example string  `json:"example"`
	Delta   float64 `json:"delta"`
}

type comparison struct {
	N        int            `json:"n"`
	Mean     *float64       `json:"mean"`
	Low      *float64       `json:"low"`
	High     *float64       `json:"high"`
	TooFew   bool           `json:"tooFew"`
	Examples []exampleDelta `json:"examples"`
}

func mean(xs []float64) float64 {
	s := 0.0
	for _, x := range xs {
		s += x
	}
	return s / float64(len(xs))
}

// compare: variant minus baseline, per example present on both sides. Repeats of one example are averaged, so a
// near-deterministic variant's identical repeats count as one value.
func compare(baseline, variant map[string][]float64) comparison {
	out := comparison{Examples: []exampleDelta{}}
	for ex, b := range baseline {
		if v := variant[ex]; len(v) > 0 && len(b) > 0 {
			out.Examples = append(out.Examples, exampleDelta{ex, mean(v) - mean(b)})
		}
	}
	sort.Slice(out.Examples, func(i, j int) bool { return out.Examples[i].Example < out.Examples[j].Example })
	out.N = len(out.Examples)
	if out.N == 0 {
		out.TooFew = true
		return out
	}
	ds := make([]float64, out.N)
	for i, e := range out.Examples {
		ds[i] = e.Delta
	}
	m := mean(ds)
	out.Mean = &m
	if out.N < minExamples() {
		out.TooFew = true
		return out
	}
	ss := 0.0
	for _, d := range ds {
		ss += (d - m) * (d - m)
	}
	half := tQuantile(0.975, float64(out.N-1)) * math.Sqrt(ss/float64(out.N-1)) / math.Sqrt(float64(out.N))
	lo, hi := m-half, m+half
	out.Low, out.High = &lo, &hi
	return out
}

// tQuantile: the Student t quantile for probability p and df degrees of freedom, by bisection on the CDF.
func tQuantile(p, df float64) float64 {
	lo, hi := -1e3, 1e3
	for i := 0; i < 200; i++ {
		mid := (lo + hi) / 2
		if tCDF(mid, df) < p {
			lo = mid
		} else {
			hi = mid
		}
	}
	return (lo + hi) / 2
}

func tCDF(t, df float64) float64 {
	x := df / (df + t*t)
	tail := 0.5 * betaInc(df/2, 0.5, x)
	if t >= 0 {
		return 1 - tail
	}
	return tail
}

// betaInc: the regularized incomplete beta function I_x(a, b) (Numerical Recipes continued fraction).
func betaInc(a, b, x float64) float64 {
	if x <= 0 {
		return 0
	}
	if x >= 1 {
		return 1
	}
	la, _ := math.Lgamma(a + b)
	lb, _ := math.Lgamma(a)
	lc, _ := math.Lgamma(b)
	front := math.Exp(la - lb - lc + a*math.Log(x) + b*math.Log(1-x))
	if x < (a+1)/(a+b+2) {
		return front * betaCF(a, b, x) / a
	}
	return 1 - front*betaCF(b, a, 1-x)/b
}

func betaCF(a, b, x float64) float64 {
	const tiny = 1e-300
	c, d := 1.0, 1-(a+b)*x/(a+1)
	if math.Abs(d) < tiny {
		d = tiny
	}
	d = 1 / d
	h := d
	for m := 1; m <= 300; m++ {
		fm := float64(m)
		for _, num := range []float64{fm * (b - fm) * x / ((a + 2*fm - 1) * (a + 2*fm)), -(a + fm) * (a + b + fm) * x / ((a + 2*fm) * (a + 2*fm + 1))} {
			d = 1 + num*d
			if math.Abs(d) < tiny {
				d = tiny
			}
			c = 1 + num/c
			if math.Abs(c) < tiny {
				c = tiny
			}
			d = 1 / d
			h *= d * c
		}
		if math.Abs(d*c-1) < 1e-14 {
			break
		}
	}
	return h
}

// typedAnswer: one trial of a typed decision: its answer after the bands (yes, no, or abstain in the middle band), the
// example's label and tag.
type typedAnswer struct {
	Tag, Answer, Label string
}

type typedStat struct {
	Tag               string   `json:"tag"`
	N                 int      `json:"n"`
	Confident         int      `json:"confident"`
	Correct           int      `json:"correct"`
	Coverage          float64  `json:"coverage"`
	SelectiveAccuracy *float64 `json:"selectiveAccuracy"`
	Escalation        float64  `json:"escalation"`
}

// typedStats: coverage (confident / all), selective accuracy (correct confident / confident; null with no confident
// answer) and escalation share (1 - coverage), per tag in tag order, then the aggregate (Tag "all", last). Used by
// lab_experiment_get's typed block (expTyped).
func typedStats(as []typedAnswer) []typedStat {
	by, total := map[string]*typedStat{}, &typedStat{Tag: "all"}
	add := func(tag string, a typedAnswer) {
		s := total
		if tag != "\x00all" {
			if s = by[tag]; s == nil {
				s = &typedStat{Tag: tag}
				by[tag] = s
			}
		}
		s.N++
		if a.Answer == "yes" || a.Answer == "no" {
			s.Confident++
			if a.Answer == a.Label {
				s.Correct++
			}
		}
	}
	for _, a := range as {
		add("\x00all", a)
		if a.Tag != "" {
			add(a.Tag, a)
		}
	}
	tags := []string{}
	for t := range by {
		tags = append(tags, t)
	}
	sort.Strings(tags)
	out := []typedStat{}
	stats := []*typedStat{}
	for _, t := range tags {
		stats = append(stats, by[t])
	}
	if total.N == 0 {
		return out
	}
	for _, s := range append(stats, total) {
		s.Coverage = float64(s.Confident) / float64(s.N)
		s.Escalation = 1 - s.Coverage
		if s.Confident > 0 {
			acc := float64(s.Correct) / float64(s.Confident)
			s.SelectiveAccuracy = &acc
		}
		out = append(out, *s)
	}
	return out
}

// bandAnswer: a typed probability through the variant's bands: yes at or above yes, no at or below no, else abstain.
func bandAnswer(p, yes, no float64) string {
	switch {
	case p >= yes:
		return "yes"
	case p <= no:
		return "no"
	}
	return "abstain"
}
