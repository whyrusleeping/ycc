// ycc-model-eval runs paid, opt-in behavioral model/tool evaluations.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/whyrusleeping/ycc/internal/config"
	"github.com/whyrusleeping/ycc/internal/modeleval"
)

func main() {
	var (
		live       = flag.Bool("live", false, "acknowledge that configured models may incur paid/live requests")
		list       = flag.Bool("list", false, "list scenarios and variants without running models")
		configPath = flag.String("config", "", "explicit ycc TOML model config (required for live runs)")
		models     = flag.String("models", "", "comma-separated logical model names")
		variants   = flag.String("variants", "baseline,concise", "comma-separated prompt/tool variants")
		scenarios  = flag.String("scenarios", "", "comma-separated scenarios (default: all seven)")
		repeats    = flag.Int("repeats", 2, "runs per scenario/model/variant (minimum 2)")
		maxRuns    = flag.Int("max-runs", 0, "explicit paid-run ceiling; required and at most 100")
		maxTurns   = flag.Int("max-turns", 12, "model turns per run (1-20)")
		timeout    = flag.Duration("timeout", 2*time.Minute, "wall timeout per run (maximum 10m)")
		output     = flag.String("output", "", "write JSON report to this file instead of stdout")
	)
	flag.Parse()

	if *list {
		fmt.Println("Scenarios:")
		for _, s := range modeleval.Scenarios() {
			fmt.Printf("  %s (v%s)\n", s.Name, s.Version)
		}
		fmt.Println("Variants:")
		for _, v := range modeleval.Variants() {
			fmt.Printf("  %s (v%s, strategy=%s, investigation_delegate=%t, implementation_delegate=%t)\n", v.Name, v.Version, v.Strategy, v.DelegationTool, v.ImplementationTool)
		}
		return
	}
	if !*live {
		fatal("refusing model execution without -live; use -list for the offline catalog")
	}
	modelNames := split(*models)
	variantNames := split(*variants)
	scenarioNames := split(*scenarios)
	if *configPath == "" || len(modelNames) == 0 {
		fatal("-config and -models are required for live runs")
	}
	if *maxRuns <= 0 || *maxRuns > 100 {
		fatal("-max-runs is required and must be in [1,100]")
	}
	if *repeats < 2 || *repeats > *maxRuns {
		fatal("-repeats must be at least 2 and no greater than -max-runs")
	}
	scenarioCount := len(scenarioNames)
	if scenarioCount == 0 {
		scenarioCount = len(modeleval.Scenarios())
	}
	variantCount := len(variantNames)
	if variantCount == 0 {
		variantCount = 2
	}
	planned := 1
	for _, factor := range []int{scenarioCount, variantCount, len(modelNames), *repeats} {
		if factor <= 0 || planned > *maxRuns/factor {
			fatal("planned matrix exceeds acknowledged -max-runs=%d", *maxRuns)
		}
		planned *= factor
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		fatal("load config: %v", err)
	}
	fingerprint, err := modeleval.ConfigFingerprint(*configPath)
	if err != nil {
		fatal("fingerprint config: %v", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	report, err := modeleval.Run(ctx, modeleval.Options{
		Registry: config.NewRegistry(cfg), Models: modelNames,
		VariantNames: variantNames, ScenarioNames: scenarioNames,
		Repeats: *repeats, Timeout: *timeout, MaxTurns: *maxTurns, MaxRuns: *maxRuns,
		ConfigFingerprint: fingerprint,
	})
	if writeErr := writeReport(*output, report); writeErr != nil {
		fatal("write report: %v", writeErr)
	}
	if err != nil {
		fatal("run (partial report saved): %v", err)
	}
}

func writeReport(path string, report modeleval.Report) error {
	var w io.Writer = os.Stdout
	if path == "" {
		return modeleval.Encode(w, report)
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := modeleval.Encode(f, report); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

func split(value string) []string {
	var out []string
	for _, item := range strings.Split(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "ycc-model-eval: "+format+"\n", args...)
	os.Exit(2)
}
