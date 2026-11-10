package sandbox

import (
	"testing"
	"time"
)

var reportNow = time.Date(2026, 10, 7, 15, 0, 0, 0, time.UTC)

func run(kind, ws, proj, status string, at time.Time, f func(*SetupRun)) SetupRun {
	r := SetupRun{Kind: kind, WorkspaceID: ws, ProjectID: proj, Status: status, CreatedAt: at, SetupKey: "k1"}
	if f != nil {
		f(&r)
	}
	return r
}

func daysAgo(n int) time.Time { return reportNow.Add(-time.Duration(n) * 24 * time.Hour) }

func TestBuildCacheReport_Totals(t *testing.T) {
	runs := []SetupRun{
		run(SetupRunTask, "ws1", "p1", SetupStateRan, daysAgo(20), nil),
		run(SetupRunTask, "ws1", "p1", SetupStateRan, daysAgo(2), func(r *SetupRun) {
			r.SetupMs, r.TotalMs, r.ToolsDownloaded = 60_000, 70_000, []string{"go@1.25.1"}
		}),
		run(SetupRunTask, "ws1", "p1", SetupStateSkipped, daysAgo(1), func(r *SetupRun) { r.TotalMs = 1_000 }),
		run(SetupRunTask, "ws1", "p1", SetupStateRestored, daysAgo(1), func(r *SetupRun) {
			r.RestoreMs, r.TotalMs = 10_000, 11_000
		}),
		run(SetupRunTask, "ws1", "p1", SetupStateFailed, reportNow, nil),
		run(SetupRunTask, "ws1", "p2", SetupStateNone, reportNow, nil),
		run(SetupRunPrewarm, "ws1", "p1", SetupStateRan, daysAgo(1), func(r *SetupRun) { r.SetupMs = 99_000 }),
	}
	rep := buildCacheReport(cacheReportInput{now: reportNow, rangeName: "7d", days: 7, runs: runs})

	tot := rep.Totals
	if tot.Runs != 5 || tot.WithSetup != 4 || tot.Hits != 2 || tot.Prewarms != 1 || tot.ToolDownloads != 1 {
		t.Fatalf("totals = %+v", tot)
	}
	if tot.HitRate == nil || *tot.HitRate != 0.5 {
		t.Fatalf("hit rate = %v", tot.HitRate)
	}
	if tot.AvgRanSetupMs != 60_000 || tot.TimeSavedMs != 110_000 {
		t.Fatalf("avg ran %d, saved %d", tot.AvgRanSetupMs, tot.TimeSavedMs)
	}
	if tot.AvgMs[SetupStateRan] != 70_000 || tot.RunsDownloading != 1 {
		t.Fatalf("avg ms %v, downloading %d", tot.AvgMs, tot.RunsDownloading)
	}
	if len(rep.Daily) != 7 || rep.Daily[0].Date != "2026-10-01" || rep.Daily[6].Date != "2026-10-07" {
		t.Fatalf("days = %+v", rep.Daily)
	}
	if d := rep.Daily[5]; d.Skipped != 1 || d.Restored != 1 || d.Prewarms != 1 {
		t.Fatalf("yesterday = %+v", d)
	}
	if d := rep.Daily[6]; d.Failed != 1 || d.None != 1 {
		t.Fatalf("today = %+v", d)
	}
	if len(rep.Projects) != 2 || rep.Projects[0].ProjectID != "p1" || rep.Projects[0].LastStatus != SetupStateFailed ||
		rep.Projects[0].AvgSetupMs != 60_000 || rep.Projects[0].LastPrewarm == nil {
		t.Fatalf("projects = %+v", rep.Projects)
	}
	if len(rep.Signals) != 0 {
		t.Fatalf("signals = %+v", rep.Signals)
	}
}

func TestBuildCacheReport_TimeSavedFallsBackToPrewarms(t *testing.T) {
	runs := []SetupRun{
		run(SetupRunPrewarm, "ws1", "p1", SetupStateRan, daysAgo(1), func(r *SetupRun) { r.SetupMs = 30_000 }),
		run(SetupRunTask, "ws1", "p1", SetupStateSkipped, reportNow, nil),
	}
	rep := buildCacheReport(cacheReportInput{now: reportNow, rangeName: "7d", days: 7, runs: runs})
	if rep.Totals.TimeSavedMs != 30_000 {
		t.Fatalf("saved = %d", rep.Totals.TimeSavedMs)
	}
}

func TestBuildCacheReport_Signals(t *testing.T) {
	var runs []SetupRun
	for i := 0; i < 6; i++ {
		status := SetupStateRan
		if i == 0 {
			status = SetupStateSkipped
		}
		runs = append(runs, run(SetupRunTask, "ws1", "p-low", status, daysAgo(3), nil))
	}
	runs = append(runs, run(SetupRunPrewarm, "ws1", "p-pre", SetupStateRan, daysAgo(6), nil))
	for i := 3; i >= 1; i-- {
		runs = append(runs, run(SetupRunPrewarm, "ws1", "p-pre", SetupStateFailed, daysAgo(i), func(r *SetupRun) {
			r.Error = "npm ERR"
		}))
	}
	for i := 0; i < 7; i++ {
		key := string(rune('a' + i))
		runs = append(runs, run(SetupRunTask, "ws1", "p-churn", SetupStateRestored, daysAgo(2), func(r *SetupRun) { r.SetupKey = key }))
	}
	runs = append([]SetupRun{run(SetupRunTask, "ws1", "p-low", SetupStateRan, daysAgo(40), func(r *SetupRun) {
		r.ToolsUsed = []string{"node@22"}
	})}, runs...)
	runs = append(runs, run(SetupRunTask, "ws1", "p-low", SetupStateSkipped, daysAgo(1), func(r *SetupRun) {
		r.ToolsUsed = []string{"go@1.25"}
	}))
	logSince := daysAgo(45)
	latest := []CacheUsage{{
		WorkspaceID: "ws1", MeasuredAt: reportNow, TotalBytes: 900, CapacityBytes: 1000, UsedBytes: 900,
		Tools: []CacheTool{{Tool: "go", Version: "1.25.1", Bytes: 200}, {Tool: "node", Version: "22.3.0", Bytes: 100}},
	}}
	first := []CacheUsage{{WorkspaceID: "ws1", MeasuredAt: daysAgo(6), TotalBytes: 400}}
	rep := buildCacheReport(cacheReportInput{now: reportNow, rangeName: "7d", days: 7, runs: runs,
		latest: latest, first: first, logSince: &logSince})

	got := map[string]CacheSignal{}
	for _, s := range rep.Signals {
		got[s.Kind+"/"+s.WorkspaceID+"/"+s.ProjectID+"/"+s.Tool] = s
	}
	for _, want := range []string{
		SignalCacheGrowth + "/ws1//", SignalCacheFull + "/ws1//",
		SignalLowHit + "/ws1/p-low/", SignalPrewarmFailing + "/ws1/p-pre/", SignalKeyChurn + "/ws1/p-churn/",
		SignalToolUnused + "///node@22.3.0",
	} {
		if _, ok := got[want]; !ok {
			t.Errorf("missing signal %s in %+v", want, rep.Signals)
		}
	}
	if len(rep.Signals) != 6 {
		t.Errorf("signals = %+v", rep.Signals)
	}
	if s := got[SignalCacheGrowth+"/ws1//"]; s.Value != 125 || s.Threshold != SignalGrowthPct {
		t.Errorf("growth = %+v", s)
	}
	if s := got[SignalToolUnused+"///node@22.3.0"]; s.Value != 40 {
		t.Errorf("unused node = %+v", s)
	}
	for _, p := range rep.Projects {
		if p.ProjectID == "p-pre" && (p.PrewarmFailStreak != 3 || p.LastPrewarm.Error != "npm ERR") {
			t.Errorf("p-pre = %+v / %+v", p, p.LastPrewarm)
		}
	}
	var goRow CacheToolRow
	for _, tr := range rep.Tools {
		if tr.Tool == "go" {
			goRow = tr
		}
	}
	if goRow.LastUsed == nil || goRow.Unused || len(goRow.Workspaces) != 1 {
		t.Errorf("go row = %+v", goRow)
	}
}

func TestBuildCacheReport_UnusedNeedsHistory(t *testing.T) {
	logSince := daysAgo(3)
	latest := []CacheUsage{{WorkspaceID: "ws1", MeasuredAt: reportNow, Tools: []CacheTool{{Tool: "go", Version: "1.24.0"}}}}
	rep := buildCacheReport(cacheReportInput{now: reportNow, rangeName: "7d", days: 7, latest: latest, logSince: &logSince})
	if len(rep.Tools) != 1 || rep.Tools[0].Unused || len(rep.Signals) != 0 {
		t.Fatalf("tools %+v, signals %+v", rep.Tools, rep.Signals)
	}
}

func TestBuildCacheReport_SnapshotOnlyProject(t *testing.T) {
	latest := []CacheUsage{{WorkspaceID: "ws1", MeasuredAt: reportNow,
		Snapshots: []CacheSnapshotDir{{ProjectID: "gone", Count: 2, Bytes: 5 << 20, Newest: "abc"}}}}
	rep := buildCacheReport(cacheReportInput{now: reportNow, rangeName: "30d", days: 30, latest: latest})
	if len(rep.Projects) != 1 || rep.Projects[0].SnapshotBytes != 5<<20 || rep.Projects[0].WorkspaceID != "ws1" ||
		rep.Projects[0].HitRate != nil || rep.Projects[0].Statuses == nil {
		t.Fatalf("projects = %+v", rep.Projects)
	}
	if rep.Totals.HitRate != nil || len(rep.Daily) != 30 {
		t.Fatalf("totals %+v, %d days", rep.Totals, len(rep.Daily))
	}
}

func TestVersionMatches(t *testing.T) {
	for _, tc := range []struct {
		want, have string
		ok         bool
	}{
		{"1.25.1", "1.25.1", true},
		{"22", "22.3.0", true},
		{"2", "22.3.0", false},
		{"1.25", "1.24.9", false},
		{"lts", "20.1.0", true},
		{"latest", "1.0.0", true},
	} {
		if got := versionMatches(tc.want, tc.have); got != tc.ok {
			t.Errorf("versionMatches(%q, %q) = %t", tc.want, tc.have, got)
		}
	}
}
