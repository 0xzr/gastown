package doctor

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/util"
)

func TestParseSystemctlShowOutputAndClassifyDoltService(t *testing.T) {
	status := parseSystemctlShowOutput(strings.Join([]string{
		"LoadState=loaded",
		"ActiveState=active",
		"SubState=running",
		"UnitFileState=enabled",
		"ExecMainPID=1234",
	}, "\n"))
	if got := classifyDoltServiceStatus(status); got != StatusOK {
		t.Fatalf("active enabled service classified as %v, want OK", got)
	}

	status["UnitFileState"] = "disabled"
	if got := classifyDoltServiceStatus(status); got != StatusError {
		t.Fatalf("disabled service classified as %v, want Error", got)
	}

	status["UnitFileState"] = "enabled"
	status["ActiveState"] = "failed"
	if got := classifyDoltServiceStatus(status); got != StatusError {
		t.Fatalf("failed service classified as %v, want Error", got)
	}

	delete(status, "UnitFileState")
	status["ActiveState"] = "active"
	if got := classifyDoltServiceStatus(status); got != StatusWarning {
		t.Fatalf("missing UnitFileState classified as %v, want Warning", got)
	}
}

func TestProductionDoltQueryCanaryCheckClassifiesLatencyAndErrors(t *testing.T) {
	check := newProductionDoltQueryCanaryCheck(productionDoltQueryCanaryDeps{
		runP95: func(_ context.Context, _ string, samples int) productionDoltQueryCanaryResult {
			if samples != productionDoltQuerySamples {
				t.Fatalf("samples = %d, want %d", samples, productionDoltQuerySamples)
			}
			return productionDoltQueryCanaryResult{
				SampleCount: samples,
				Min:         1 * time.Millisecond,
				P95:         42 * time.Millisecond,
				Max:         50 * time.Millisecond,
			}
		},
	})
	res := check.Run(&CheckContext{TownRoot: "/town"})
	if res.Status != StatusOK {
		t.Fatalf("fast canary status = %v, want OK", res.Status)
	}

	check = newProductionDoltQueryCanaryCheck(productionDoltQueryCanaryDeps{
		runP95: func(context.Context, string, int) productionDoltQueryCanaryResult {
			return productionDoltQueryCanaryResult{SampleCount: 20, P95: productionDoltQuerySlowP95}
		},
	})
	res = check.Run(&CheckContext{TownRoot: "/town"})
	if res.Status != StatusWarning {
		t.Fatalf("slow canary status = %v, want Warning", res.Status)
	}

	check = newProductionDoltQueryCanaryCheck(productionDoltQueryCanaryDeps{
		runP95: func(context.Context, string, int) productionDoltQueryCanaryResult {
			return productionDoltQueryCanaryResult{SampleCount: 3, P95: time.Second, Err: errors.New("sql failed")}
		},
	})
	res = check.Run(&CheckContext{TownRoot: "/town"})
	if res.Status != StatusError {
		t.Fatalf("failed canary status = %v, want Error", res.Status)
	}
}

func TestSummarizeLatencySamplesUsesP95Index(t *testing.T) {
	samples := make([]time.Duration, 0, 20)
	for i := 20; i >= 1; i-- {
		samples = append(samples, time.Duration(i)*time.Millisecond)
	}
	min, p95, max := summarizeLatencySamples(samples)
	if min != time.Millisecond || p95 != 19*time.Millisecond || max != 20*time.Millisecond {
		t.Fatalf("summary = min %s p95 %s max %s, want 1ms 19ms 20ms", min, p95, max)
	}

	samples = samples[:0]
	for i := 0; i < 19; i++ {
		samples = append(samples, 10*time.Millisecond)
	}
	samples = append(samples, 2*time.Second)
	_, p95, max = summarizeLatencySamples(samples)
	if p95 != 10*time.Millisecond || max != 2*time.Second {
		t.Fatalf("summary with one outlier = p95 %s max %s, want p95 10ms max 2s", p95, max)
	}

	min, p95, max = summarizeLatencySamples(nil)
	if min != 0 || p95 != 0 || max != 0 {
		t.Fatalf("empty summary = min %s p95 %s max %s, want zeroes", min, p95, max)
	}
}

func TestRunDoltQueryP95CanaryRespectsOverallContext(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	start := time.Now()
	res := runDoltQueryP95CanaryWith(ctx, "/town", productionDoltQuerySamples, func(ctx context.Context, _ string) (time.Duration, error) {
		<-ctx.Done()
		return 0, ctx.Err()
	})
	if res.Err == nil || !errors.Is(res.Err, context.DeadlineExceeded) {
		t.Fatalf("Err = %v, want context deadline exceeded", res.Err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("canary did not respect overall timeout; elapsed %s", elapsed)
	}
	if res.SampleCount != 0 {
		t.Fatalf("SampleCount = %d, want 0", res.SampleCount)
	}
}

func TestClassifyDaemonHeartbeat(t *testing.T) {
	now := time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name    string
		running bool
		last    time.Time
		want    CheckStatus
	}{
		{name: "not running", running: false, last: now, want: StatusError},
		{name: "missing heartbeat", running: true, want: StatusWarning},
		{name: "fresh", running: true, last: now.Add(-2 * time.Minute), want: StatusOK},
		{name: "stale", running: true, last: now.Add(-productionDaemonHeartbeatWarn), want: StatusWarning},
		{name: "very stale", running: true, last: now.Add(-productionDaemonHeartbeatFail), want: StatusError},
		{name: "future timestamp clamps fresh", running: true, last: now.Add(time.Minute), want: StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyDaemonHeartbeat(tt.running, tt.last, now); got != tt.want {
				t.Fatalf("classifyDaemonHeartbeat = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestProductionDaemonHeartbeatCheckAllowsFreshStartupWithoutHeartbeat(t *testing.T) {
	now := time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)
	check := newProductionDaemonHeartbeatCheck(productionDaemonDeps{
		now: func() time.Time { return now },
		snapshot: func(string) (productionDaemonSnapshot, error) {
			return productionDaemonSnapshot{
				Running:        true,
				PID:            1234,
				StartedAt:      now.Add(-30 * time.Second),
				HeartbeatCount: 0,
			}, nil
		},
	})

	res := check.Run(&CheckContext{TownRoot: "/town"})
	if res.Status != StatusOK {
		t.Fatalf("fresh startup without heartbeat status = %v, want OK; message=%q details=%v", res.Status, res.Message, res.Details)
	}
	if !strings.Contains(res.Message, "heartbeat pending") {
		t.Fatalf("message = %q, want startup grace explanation", res.Message)
	}
}

func TestProductionSpaceClassifiers(t *testing.T) {
	if got := classifyProductionRootSpace(diskInfo(2*1024*1024*1024, 10*1024*1024*1024, 80)); got != StatusOK {
		t.Fatalf("root OK classified as %v, want OK", got)
	}
	if got := classifyProductionRootSpace(diskInfo(700*1024*1024, 10*1024*1024*1024, 90)); got != StatusWarning {
		t.Fatalf("root low classified as %v, want Warning", got)
	}
	if got := classifyProductionRootSpace(diskInfo(100*1024*1024, 10*1024*1024*1024, 99)); got != StatusError {
		t.Fatalf("root critical classified as %v, want Error", got)
	}

	if got := classifyProductionSHMSpace(diskInfo(512*1024*1024, 1024*1024*1024, 50)); got != StatusOK {
		t.Fatalf("shm OK classified as %v, want OK", got)
	}
	if got := classifyProductionSHMSpace(diskInfo(128*1024*1024, 1024*1024*1024, 80)); got != StatusWarning {
		t.Fatalf("shm low classified as %v, want Warning", got)
	}
	if got := classifyProductionSHMSpace(diskInfo(32*1024*1024, 1024*1024*1024, 97)); got != StatusError {
		t.Fatalf("shm critical classified as %v, want Error", got)
	}
}

func TestParseProcLoadavgAndClassify(t *testing.T) {
	load, err := parseProcLoadavg("0.50 1.25 2.00 3/100 12345\n")
	if err != nil {
		t.Fatalf("parseProcLoadavg: %v", err)
	}
	if load.One != 0.50 || load.Five != 1.25 || load.Fifteen != 2.00 {
		t.Fatalf("load = %+v, want 0.50/1.25/2.00", load)
	}
	if _, err := parseProcLoadavg("bad"); err == nil {
		t.Fatal("parseProcLoadavg malformed input returned nil error")
	}

	if got := classifyLoadAverage(productionLoadAverages{One: 4.9}, 4); got != StatusOK {
		t.Fatalf("load below CPU count classified as %v, want OK", got)
	}
	if got := classifyLoadAverage(productionLoadAverages{One: 5.0}, 4); got != StatusWarning {
		t.Fatalf("load at 1.25x CPU count classified as %v, want Warning", got)
	}
	if got := classifyLoadAverage(productionLoadAverages{One: 8.0}, 4); got != StatusError {
		t.Fatalf("load at 2x CPU count classified as %v, want Error", got)
	}
}

func TestClassifyRejectLedgerSize(t *testing.T) {
	tests := []struct {
		name string
		size int64
		want CheckStatus
	}{
		{name: "empty", size: 0, want: StatusOK},
		{name: "below warning", size: productionRejectLedgerWarnBytes - 1, want: StatusOK},
		{name: "warning", size: productionRejectLedgerWarnBytes, want: StatusWarning},
		{name: "error", size: productionRejectLedgerFailBytes, want: StatusError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyRejectLedgerSize(tt.size); got != tt.want {
				t.Fatalf("classifyRejectLedgerSize(%d) = %v, want %v", tt.size, got, tt.want)
			}
		})
	}
}

func TestParseDoltListenersAndRandomPortFilter(t *testing.T) {
	input := strings.Join([]string{
		"p100",
		"n127.0.0.1:3307",
		"p200",
		"n*:48123",
		"n*:48123",
		"n*:48124",
		"p201",
		"n[::1]:49000",
		"noise",
	}, "\n")

	listeners := parseDoltListenersFromLsof(input)
	if len(listeners) != 4 {
		t.Fatalf("listeners = %#v, want 4 unique pid/port pairs", listeners)
	}
	random := randomPortDoltListeners(listeners, 3307)
	if len(random) != 2 {
		t.Fatalf("random = %#v, want one entry per random-port PID", random)
	}
	if random[0].PID != 200 || random[0].Port != 48123 {
		t.Fatalf("first random listener = %+v, want PID 200 port 48123", random[0])
	}
	if random[1].PID != 201 || random[1].Port != 49000 {
		t.Fatalf("second random listener = %+v, want PID 201 port 49000", random[1])
	}
}

func diskInfo(available, total uint64, usedPercent float64) *util.DiskSpaceInfo {
	return &util.DiskSpaceInfo{
		AvailableBytes: available,
		TotalBytes:     total,
		UsedBytes:      total - available,
		UsedPercent:    usedPercent,
	}
}
