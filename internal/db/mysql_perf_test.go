//go:build dbperf

// One-off measurement harness — does NOT run as part of `go test ./...`.
//
// Question: is mysqlAppendSamples wall time a hotspot worth parallelizing
// via errgroup? Per the perf-campaign discipline (instrument → measure
// → decide), we measure before optimizing.
//
// To run:
//
//	docker run -d --rm --name ken-mysql-perf -p 53306:3306 \
//	  -e MYSQL_ROOT_PASSWORD=test -e MYSQL_DATABASE=test mysql:8
//	export KEN_DB_MYSQL_TEST_DSN='root:test@tcp(127.0.0.1:53306)/?parseTime=true'
//	go test -tags=mysqlperf -run TestPerf_MySQLAppendSamples -v ./internal/db/
//
// What it measures:
//   - Total mysqlAppendSamples wall time vs total introspection wall time
//     (the % matters; if sample-fetch is <10% of total, parallelizing is moot).
//   - Per-table sample-query wall time (sequential), so we can see whether
//     time-per-table is roughly constant (parallelism wins) or bimodal
//     (some tables dominate; parallelism wins less).
//   - Where in the introspection pipeline time is actually spent
//     (list-tables, annotate-constraints, annotate-indexes, annotate-FKs,
//     list-views, list-routines, approx-row-counts, sample-loop).
//
// Fixture: 50 tables × 100 rows, mix of column shapes (int+text, int+timestamp,
// int+varchar). Representative of a medium dev/staging MySQL.

package db

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"sort"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

const (
	perfSchema   = "ken_perf_samples"
	perfTables   = 50
	perfRowsPer  = 100
	perfSampleN  = 5 // matches default sample-rows policy
	perfWarmRuns = 1
	perfTimedRun = 3 // median of 3
)

func TestPerf_MySQLAppendSamples(t *testing.T) {
	dsn := os.Getenv("KEN_DB_MYSQL_TEST_DSN")
	if dsn == "" {
		t.Skip("set KEN_DB_MYSQL_TEST_DSN; see harness docstring")
	}

	ctx := context.Background()
	conn, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer conn.Close()
	if err := conn.PingContext(ctx); err != nil {
		t.Fatalf("ping: %v", err)
	}

	// Build the fixture (idempotent: DROP + CREATE).
	t.Logf("building fixture: %d tables × %d rows in schema %q...", perfTables, perfRowsPer, perfSchema)
	buildStart := time.Now()
	if err := perfBuildFixture(ctx, conn); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	t.Logf("fixture built in %v", time.Since(buildStart))
	t.Cleanup(func() {
		_, _ = conn.ExecContext(ctx, "DROP DATABASE IF EXISTS "+perfSchema)
	})

	// Warm the connection pool + MySQL caches.
	for i := 0; i < perfWarmRuns; i++ {
		_ = perfRunSampleLoop(ctx, conn)
	}

	// Per-table timing (sequential): captures the distribution.
	t.Log("--- per-table sequential timing ---")
	perTable, total := perfPerTableTiming(ctx, t, conn)
	sort.Slice(perTable, func(i, j int) bool { return perTable[i] < perTable[j] })
	min := perTable[0]
	max := perTable[len(perTable)-1]
	median := perTable[len(perTable)/2]
	var sum time.Duration
	for _, d := range perTable {
		sum += d
	}
	mean := sum / time.Duration(len(perTable))
	t.Logf("per-table sample-query wall: n=%d  min=%v  median=%v  mean=%v  max=%v  sum=%v  total_wall=%v",
		len(perTable), min, median, mean, max, sum, total)

	// Full mysqlAppendSamples timing (3-trial median).
	t.Log("--- mysqlAppendSamples (full function) timing, 3 trials ---")
	var trials []time.Duration
	for i := 0; i < perfTimedRun; i++ {
		d := perfRunSampleLoop(ctx, conn)
		trials = append(trials, d)
		t.Logf("trial %d: %v", i+1, d)
	}
	sort.Slice(trials, func(i, j int) bool { return trials[i] < trials[j] })
	t.Logf("mysqlAppendSamples median of %d: %v", perfTimedRun, trials[perfTimedRun/2])

	// Total introspection timing (the denominator — what fraction is sample-fetch?).
	t.Log("--- full introspection (indexSchemaMySQL) timing, 3 trials ---")
	var fullTrials []time.Duration
	for i := 0; i < perfTimedRun; i++ {
		opts := Options{DSN: dsn, SampleRows: perfSampleN, IncludeSchemas: []string{perfSchema}}
		s := time.Now()
		_, err := indexSchemaMySQL(ctx, opts)
		if err != nil {
			t.Fatalf("indexSchemaMySQL: %v", err)
		}
		fullTrials = append(fullTrials, time.Since(s))
	}
	sort.Slice(fullTrials, func(i, j int) bool { return fullTrials[i] < fullTrials[j] })
	fullMedian := fullTrials[perfTimedRun/2]
	sampleMedian := trials[perfTimedRun/2]
	t.Logf("full introspection median: %v", fullMedian)
	t.Logf("sample-loop fraction of total introspection: %.1f%%",
		float64(sampleMedian)*100.0/float64(fullMedian))

	// Verdict heuristic.
	t.Log("--- verdict ---")
	frac := float64(sampleMedian) / float64(fullMedian)
	switch {
	case frac < 0.10:
		t.Logf("sample-loop is <10%% of introspection wall (%.1f%%) — parallelizing it is NOT worth the complexity", frac*100)
	case frac < 0.30:
		t.Logf("sample-loop is %.1f%% of introspection wall — borderline; ideal speedup would shave %.1f%% off worst case (Amdahl)", frac*100, frac*100*0.875)
	default:
		t.Logf("sample-loop is %.1f%% of introspection wall — parallelizing has real headroom; Amdahl ceiling ≈ %.1fx", frac*100, 1/(1-frac*0.875))
	}
}

func perfBuildFixture(ctx context.Context, conn *sql.DB) error {
	if _, err := conn.ExecContext(ctx, "DROP DATABASE IF EXISTS "+perfSchema); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, "CREATE DATABASE "+perfSchema); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, "USE "+perfSchema); err != nil {
		return err
	}
	for i := 0; i < perfTables; i++ {
		// Three table shapes, rotated, so the sample loop hits varied col types.
		var createStmt string
		switch i % 3 {
		case 0:
			createStmt = fmt.Sprintf(
				"CREATE TABLE `t%03d` (id INT PRIMARY KEY, label TEXT, payload TEXT)", i)
		case 1:
			createStmt = fmt.Sprintf(
				"CREATE TABLE `t%03d` (id INT PRIMARY KEY, label VARCHAR(64), updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP)", i)
		case 2:
			createStmt = fmt.Sprintf(
				"CREATE TABLE `t%03d` (id INT PRIMARY KEY, score DOUBLE, json_blob JSON)", i)
		}
		if _, err := conn.ExecContext(ctx, createStmt); err != nil {
			return fmt.Errorf("create t%03d: %w", i, err)
		}
		// Insert rows in one multi-row statement.
		var values string
		for r := 0; r < perfRowsPer; r++ {
			if r > 0 {
				values += ", "
			}
			switch i % 3 {
			case 0:
				values += fmt.Sprintf("(%d, 'label-%d', 'lorem ipsum dolor sit amet payload row %d')", r, r, r)
			case 1:
				values += fmt.Sprintf("(%d, 'label-%d', CURRENT_TIMESTAMP)", r, r)
			case 2:
				values += fmt.Sprintf(`(%d, %f, JSON_OBJECT('k', 'v', 'n', %d))`, r, float64(r)*1.5, r)
			}
		}
		stmt := fmt.Sprintf("INSERT INTO `t%03d` VALUES %s", i, values)
		if _, err := conn.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("insert t%03d: %w", i, err)
		}
	}
	// ANALYZE so information_schema.tables.table_rows reflects reality.
	for i := 0; i < perfTables; i++ {
		_, _ = conn.ExecContext(ctx, fmt.Sprintf("ANALYZE TABLE `%s`.`t%03d`", perfSchema, i))
	}
	return nil
}

// perfRunSampleLoop replicates what mysqlAppendSamples does end-to-end on
// our fixture, but without depending on the rest of the introspection
// pipeline. We synthesize a minimal snapshot the function expects.
func perfRunSampleLoop(ctx context.Context, conn *sql.DB) time.Duration {
	// Build a minimal schemaSnapshot with the fixture tables.
	snap := &schemaSnapshot{}
	for i := 0; i < perfTables; i++ {
		snap.tables = append(snap.tables, tableInfo{
			schema: perfSchema,
			name:   fmt.Sprintf("t%03d", i),
			columns: []columnInfo{
				{name: "id", isPrimaryKey: true},
			},
		})
	}
	opts := Options{SampleRows: perfSampleN, IncludeSchemas: []string{perfSchema}}
	start := time.Now()
	mysqlAppendSamples(ctx, conn, snap, opts)
	return time.Since(start)
}

func perfPerTableTiming(ctx context.Context, t *testing.T, conn *sql.DB) ([]time.Duration, time.Duration) {
	durations := make([]time.Duration, 0, perfTables)
	totalStart := time.Now()
	for i := 0; i < perfTables; i++ {
		name := fmt.Sprintf("t%03d", i)
		q := fmt.Sprintf("SELECT * FROM `%s`.`%s` ORDER BY `id` LIMIT ?", perfSchema, name)
		s := time.Now()
		rows, err := conn.QueryContext(ctx, q, perfSampleN)
		if err != nil {
			t.Fatalf("sample query %s: %v", name, err)
		}
		cols, _ := rows.Columns()
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for j := range vals {
				ptrs[j] = &vals[j]
			}
			_ = rows.Scan(ptrs...)
		}
		rows.Close()
		durations = append(durations, time.Since(s))
	}
	return durations, time.Since(totalStart)
}
