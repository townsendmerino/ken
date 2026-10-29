//go:build bench

package contextbench

import (
	"fmt"
	"math/rand"
	"sort"
	"strings"
)

// BootstrapCI represents a bootstrap mean and 95% confidence interval [Low, High].
type BootstrapCI struct {
	N    int     `json:"n"`
	Mean float64 `json:"mean"`
	Low  float64 `json:"low"`
	High float64 `json:"high"`
}

func (b BootstrapCI) String() string {
	return fmt.Sprintf("%.4f [%.4f, %.4f] (N=%d)", b.Mean, b.Low, b.High, b.N)
}

// PairedBootstrapCI computes the mean difference and 95% confidence interval using
// numResamples bootstrap draws with replacement and a deterministic PRNG seed.
func PairedBootstrapCI(diffs []float64, numResamples int, seed int64) BootstrapCI {
	n := len(diffs)
	if n == 0 {
		return BootstrapCI{}
	}
	if numResamples <= 0 {
		numResamples = 10000
	}

	sum := 0.0
	for _, d := range diffs {
		sum += d
	}
	mean := sum / float64(n)

	if n == 1 {
		return BootstrapCI{N: 1, Mean: mean, Low: mean, High: mean}
	}

	rng := rand.New(rand.NewSource(seed))
	bootMeans := make([]float64, numResamples)

	for b := 0; b < numResamples; b++ {
		bSum := 0.0
		for i := 0; i < n; i++ {
			idx := rng.Intn(n)
			bSum += diffs[idx]
		}
		bootMeans[b] = bSum / float64(n)
	}

	sort.Float64s(bootMeans)

	lowIdx := int(0.025 * float64(numResamples))
	highIdx := int(0.975 * float64(numResamples))
	if highIdx >= numResamples {
		highIdx = numResamples - 1
	}

	return BootstrapCI{
		N:    n,
		Mean: mean,
		Low:  bootMeans[lowIdx],
		High: bootMeans[highIdx],
	}
}

// PairComparison holds comparative analysis between arm A and arm B.
type PairComparison struct {
	ArmA       string                 `json:"arm_a"`
	ArmB       string                 `json:"arm_b"`
	Metric     string                 `json:"metric"`
	Budget     int                    `json:"budget"`
	Overall    BootstrapCI            `json:"overall"`
	ByLanguage map[string]BootstrapCI `json:"by_language"`
	BySource   map[string]BootstrapCI `json:"by_source"`
	ByRepo     map[string]BootstrapCI `json:"by_repo"`
}

// ComputePairComparison computes paired differences (A - B) for the given metric and budget.
func ComputePairComparison(
	resultsA, resultsB []TaskResult,
	metricName string,
	metricExtractor func(TaskResult) float64,
	budget int,
	seed int64,
) PairComparison {
	mapB := make(map[string]TaskResult)
	for _, rb := range resultsB {
		if rb.Budget == budget {
			mapB[rb.InstanceID] = rb
		}
	}

	var overallDiffs []float64
	diffsByLang := make(map[string][]float64)
	diffsBySource := make(map[string][]float64)
	diffsByRepo := make(map[string][]float64)

	armA := ""
	armB := ""

	for _, ra := range resultsA {
		if ra.Budget != budget {
			continue
		}
		rb, ok := mapB[ra.InstanceID]
		if !ok {
			continue
		}
		armA = ra.Arm
		armB = rb.Arm

		valA := metricExtractor(ra)
		valB := metricExtractor(rb)
		diff := valA - valB

		overallDiffs = append(overallDiffs, diff)
		diffsByLang[ra.Language] = append(diffsByLang[ra.Language], diff)
		diffsBySource[ra.Source] = append(diffsBySource[ra.Source], diff)
		diffsByRepo[ra.Repo] = append(diffsByRepo[ra.Repo], diff)
	}

	ciByLang := make(map[string]BootstrapCI, len(diffsByLang))
	for k, list := range diffsByLang {
		ciByLang[k] = PairedBootstrapCI(list, 10000, seed)
	}

	ciBySource := make(map[string]BootstrapCI, len(diffsBySource))
	for k, list := range diffsBySource {
		ciBySource[k] = PairedBootstrapCI(list, 10000, seed)
	}

	ciByRepo := make(map[string]BootstrapCI, len(diffsByRepo))
	for k, list := range diffsByRepo {
		ciByRepo[k] = PairedBootstrapCI(list, 10000, seed)
	}

	return PairComparison{
		ArmA:       armA,
		ArmB:       armB,
		Metric:     metricName,
		Budget:     budget,
		Overall:    PairedBootstrapCI(overallDiffs, 10000, seed),
		ByLanguage: ciByLang,
		BySource:   ciBySource,
		ByRepo:     ciByRepo,
	}
}

// FormatComparisonTable prints a summary markdown table for a list of pairwise comparisons.
func FormatComparisonTable(comparisons []PairComparison) string {
	var b strings.Builder
	b.WriteString("| Comparison | Metric | Budget | Mean Delta (A − B) | 95% Bootstrap CI | N Tasks |\n")
	b.WriteString("|---|---|---:|---:|---|---:|\n")
	for _, c := range comparisons {
		b.WriteString(fmt.Sprintf("| %s − %s | %s | %d | %+.4f | [%+.4f, %+.4f] | %d |\n",
			c.ArmA, c.ArmB, c.Metric, c.Budget, c.Overall.Mean, c.Overall.Low, c.Overall.High, c.Overall.N))
	}
	return b.String()
}
