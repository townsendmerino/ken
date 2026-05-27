//go:build dbperf

// One-off measurement harness for Postgres — does NOT run as part of
// `go test ./...`. Sibling to mysql_perf_test.go; same shape so the
// engine asymmetry is auditable in one place.
//
// To run:
//
//	docker run -d --rm --name ken-postgres-perf -p 55432:5432 \
//	  -e POSTGRES_PASSWORD=test postgres:16-alpine
//	export KEN_DB_TEST_DSN='postgres://postgres:test@127.0.0.1:55432/postgres?sslmode=disable'
//	go test -tags=dbperf -run TestPerf_PostgresSampleRows -v ./internal/db/
//
// Fixture: 50 tables × 100 rows, mix of column shapes (int+text+text,
// int+varchar+timestamp, int+double+jsonb). Mirrors the MySQL harness
// for direct cross-engine comparison.

package db

import (
	"context"
	"fmt"
	"os"
	"sort"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	pgPerfSchema  = "ken_perf_samples"
	pgPerfTables  = 50
	pgPerfRowsPer = 100
	pgPerfSampleN = 5
)

func TestPerf_PostgresSampleRows(t *testing.T) {
	dsn := os.Getenv("KEN_DB_TEST_DSN")
	if dsn == "" {
		t.Skip("set KEN_DB_TEST_DSN; see harness docstring")
	}

	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("pgx.Connect: %v", err)
	}
	defer conn.Close(ctx)

	t.Logf("building fixture: %d tables × %d rows in schema %q...", pgPerfTables, pgPerfRowsPer, pgPerfSchema)
	buildStart := time.Now()
	if err := pgPerfBuildFixture(ctx, conn); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	t.Logf("fixture built in %v", time.Since(buildStart))
	t.Cleanup(func() {
		_, _ = conn.Exec(ctx, "DROP SCHEMA IF EXISTS "+pgPerfSchema+" CASCADE")
	})

	// Warm.
	_ = pgPerfRunSampleLoop(ctx, conn)

	// Per-table timing.
	t.Log("--- per-table sequential timing ---")
	perTable, total := pgPerfPerTableTiming(ctx, t, conn)
	sort.Slice(perTable, func(i, j int) bool { return perTable[i] < perTable[j] })
	var sum time.Duration
	for _, d := range perTable {
		sum += d
	}
	mean := sum / time.Duration(len(perTable))
	t.Logf("per-table sample-query wall: n=%d  min=%v  median=%v  mean=%v  max=%v  sum=%v  total_wall=%v",
		len(perTable), perTable[0], perTable[len(perTable)/2], mean,
		perTable[len(perTable)-1], sum, total)

	// Full sampleRowsImpl, 3 trials.
	t.Log("--- sampleRowsImpl (full function) timing, 3 trials ---")
	var trials []time.Duration
	for i := 0; i < 3; i++ {
		d := pgPerfRunSampleLoop(ctx, conn)
		trials = append(trials, d)
		t.Logf("trial %d: %v", i+1, d)
	}
	sort.Slice(trials, func(i, j int) bool { return trials[i] < trials[j] })
	sampleMedian := trials[1]
	t.Logf("sampleRowsImpl median of 3: %v", sampleMedian)

	// Full introspection (IndexSchema), 3 trials.
	t.Log("--- full introspection (IndexSchema) timing, 3 trials ---")
	var fullTrials []time.Duration
	for i := 0; i < 3; i++ {
		opts := Options{DSN: dsn, SampleRows: pgPerfSampleN, IncludeSchemas: []string{pgPerfSchema}}
		s := time.Now()
		_, err := IndexSchema(ctx, opts)
		if err != nil {
			t.Fatalf("IndexSchema: %v", err)
		}
		fullTrials = append(fullTrials, time.Since(s))
	}
	sort.Slice(fullTrials, func(i, j int) bool { return fullTrials[i] < fullTrials[j] })
	fullMedian := fullTrials[1]
	t.Logf("full introspection median: %v", fullMedian)
	t.Logf("sample-loop fraction of total introspection: %.1f%%",
		float64(sampleMedian)*100.0/float64(fullMedian))

	t.Log("--- verdict ---")
	frac := float64(sampleMedian) / float64(fullMedian)
	switch {
	case frac < 0.10:
		t.Logf("sample-loop is <10%% of introspection wall (%.1f%%) — parallelizing it is NOT worth the complexity", frac*100)
	case frac < 0.30:
		t.Logf("sample-loop is %.1f%% of introspection wall — borderline", frac*100)
	default:
		t.Logf("sample-loop is %.1f%% — parallelizing has real headroom; Amdahl ceiling ≈ %.1fx", frac*100, 1/(1-frac*0.875))
	}
}

func pgPerfBuildFixture(ctx context.Context, conn *pgx.Conn) error {
	if _, err := conn.Exec(ctx, "DROP SCHEMA IF EXISTS "+pgPerfSchema+" CASCADE"); err != nil {
		return err
	}
	if _, err := conn.Exec(ctx, "CREATE SCHEMA "+pgPerfSchema); err != nil {
		return err
	}
	for i := 0; i < pgPerfTables; i++ {
		var create string
		switch i % 3 {
		case 0:
			create = fmt.Sprintf(
				"CREATE TABLE %s.t%03d (id INT PRIMARY KEY, label TEXT, payload TEXT)", pgPerfSchema, i)
		case 1:
			create = fmt.Sprintf(
				"CREATE TABLE %s.t%03d (id INT PRIMARY KEY, label VARCHAR(64), updated_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP)", pgPerfSchema, i)
		case 2:
			create = fmt.Sprintf(
				"CREATE TABLE %s.t%03d (id INT PRIMARY KEY, score DOUBLE PRECISION, json_blob JSONB)", pgPerfSchema, i)
		}
		if _, err := conn.Exec(ctx, create); err != nil {
			return fmt.Errorf("create t%03d: %w", i, err)
		}
		// Build multi-row insert.
		var values string
		for r := 0; r < pgPerfRowsPer; r++ {
			if r > 0 {
				values += ", "
			}
			switch i % 3 {
			case 0:
				values += fmt.Sprintf("(%d, 'label-%d', 'lorem ipsum dolor sit amet payload row %d')", r, r, r)
			case 1:
				values += fmt.Sprintf("(%d, 'label-%d', CURRENT_TIMESTAMP)", r, r)
			case 2:
				values += fmt.Sprintf(`(%d, %f, '{"k":"v","n":%d}'::jsonb)`, r, float64(r)*1.5, r)
			}
		}
		stmt := fmt.Sprintf("INSERT INTO %s.t%03d VALUES %s", pgPerfSchema, i, values)
		if _, err := conn.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("insert t%03d: %w", i, err)
		}
		_, _ = conn.Exec(ctx, fmt.Sprintf("ANALYZE %s.t%03d", pgPerfSchema, i))
	}
	return nil
}

func pgPerfRunSampleLoop(ctx context.Context, conn *pgx.Conn) time.Duration {
	snap := &schemaSnapshot{}
	for i := 0; i < pgPerfTables; i++ {
		snap.tables = append(snap.tables, tableInfo{
			schema: pgPerfSchema,
			name:   fmt.Sprintf("t%03d", i),
			columns: []columnInfo{
				{name: "id", isPrimaryKey: true},
			},
		})
	}
	opts := Options{SampleRows: pgPerfSampleN, IncludeSchemas: []string{pgPerfSchema}}
	start := time.Now()
	sampleRowsImpl(ctx, conn, snap, opts)
	return time.Since(start)
}

func pgPerfPerTableTiming(ctx context.Context, t *testing.T, conn *pgx.Conn) ([]time.Duration, time.Duration) {
	durations := make([]time.Duration, 0, pgPerfTables)
	totalStart := time.Now()
	for i := 0; i < pgPerfTables; i++ {
		name := fmt.Sprintf("t%03d", i)
		q := fmt.Sprintf(`SELECT * FROM %s."%s" ORDER BY "id" LIMIT $1`, pgPerfSchema, name)
		s := time.Now()
		rows, err := conn.Query(ctx, q, pgPerfSampleN)
		if err != nil {
			t.Fatalf("sample query %s: %v", name, err)
		}
		for rows.Next() {
			_, _ = rows.Values()
		}
		rows.Close()
		durations = append(durations, time.Since(s))
	}
	return durations, time.Since(totalStart)
}
