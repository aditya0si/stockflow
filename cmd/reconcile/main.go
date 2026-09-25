// Command reconcile runs the report-only reconciliation checks once and exits.
// It never repairs data. Exit codes: 0 clean, 1 findings (unless
// -fail-on-findings=false), 2 operational error.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/oliaditya05/stockflow/internal/platform/postgres"
	"github.com/oliaditya05/stockflow/internal/reconciliation"
	"github.com/oliaditya05/stockflow/migrations"
)

func main() {
	failOnFindings := flag.Bool("fail-on-findings", true, "exit 1 when findings are present")
	asJSON := flag.Bool("json", true, "print the run as JSON")
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		fmt.Fprintln(os.Stderr, "DATABASE_URL is required")
		os.Exit(2)
	}

	pool, err := postgres.Open(ctx, postgres.Config{URL: databaseURL, ConnectTimeout: 5 * time.Second})
	if err != nil {
		fmt.Fprintln(os.Stderr, "connect:", err)
		os.Exit(2)
	}
	defer pool.Close()

	if err := postgres.Migrate(ctx, pool, migrations.FS); err != nil {
		fmt.Fprintln(os.Stderr, "migrate:", err)
		os.Exit(2)
	}

	run, err := (&reconciliation.Service{Pool: pool}).Run(ctx, "reconcile-cli")
	if err != nil {
		fmt.Fprintln(os.Stderr, "reconcile:", err)
		os.Exit(2)
	}

	if *asJSON {
		encoded, err := json.MarshalIndent(run, "", "  ")
		if err != nil {
			fmt.Fprintln(os.Stderr, "encode:", err)
			os.Exit(2)
		}
		fmt.Println(string(encoded))
	} else {
		fmt.Printf("run %s %s: %d checks, %d findings\n", run.ID, run.Status, run.ChecksRun, run.FindingsCount)
		for _, finding := range run.Findings {
			fmt.Printf("  [%s] expected=%s observed=%s (%s)\n",
				finding.CheckName, finding.Expected, finding.Observed, refString(finding.EntityRef))
		}
	}

	if run.FindingsCount > 0 && *failOnFindings {
		os.Exit(1)
	}
}

func refString(ref *string) string {
	if ref == nil {
		return "-"
	}
	return *ref
}
