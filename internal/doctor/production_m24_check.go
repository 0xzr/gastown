package doctor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/steveyegge/gastown/internal/daemon"
	"github.com/steveyegge/gastown/internal/doltserver"
	"github.com/steveyegge/gastown/internal/util"
)

const (
	productionCommandTimeout = 2 * time.Second
	productionBDListTimeout  = 5 * time.Second
	productionBDListSlow     = 4 * time.Second

	productionDaemonHeartbeatWarn = 15 * time.Minute
	productionDaemonHeartbeatFail = 30 * time.Minute

	productionSHMWarnBytes = 256 * 1024 * 1024
	productionSHMFailBytes = 64 * 1024 * 1024

	productionRejectLedgerWarnBytes = 25 * 1024 * 1024
	productionRejectLedgerFailBytes = 100 * 1024 * 1024
)

type productionServiceDeps struct {
	serviceStatus func(context.Context, string) (map[string]string, error)
}

func defaultProductionServiceDeps() productionServiceDeps {
	return productionServiceDeps{serviceStatus: systemdUserServiceStatus}
}

type ProductionDoltServiceCheck struct {
	BaseCheck
	deps productionServiceDeps
}

func NewProductionDoltServiceCheck() *ProductionDoltServiceCheck {
	return newProductionDoltServiceCheck(defaultProductionServiceDeps())
}

func newProductionDoltServiceCheck(deps productionServiceDeps) *ProductionDoltServiceCheck {
	return &ProductionDoltServiceCheck{
		BaseCheck: BaseCheck{
			CheckName:        "prod-dolt-service",
			CheckDescription: "Verify gt-dolt.service is active and enabled",
			CheckCategory:    CategoryProduction,
		},
		deps: deps,
	}
}

func (c *ProductionDoltServiceCheck) Run(ctx *CheckContext) *CheckResult {
	status, err := c.deps.serviceStatus(context.Background(), "gt-dolt.service")
	if err != nil {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusWarning,
			Message: "gt-dolt.service status unavailable",
			Details: []string{err.Error()},
		}
	}

	checkStatus := classifyDoltServiceStatus(status)
	details := []string{formatServiceStatusDetail("gt-dolt.service", status)}
	message := "gt-dolt.service active and enabled"
	if checkStatus == StatusWarning {
		message = "gt-dolt.service enablement unknown"
	}
	if checkStatus == StatusError {
		message = "gt-dolt.service is not active and enabled"
	}
	return &CheckResult{
		Name:    c.Name(),
		Status:  checkStatus,
		Message: message,
		Details: details,
	}
}

func systemdSystemServiceStatus(ctx context.Context, service string) (map[string]string, error) {
	cmdCtx, cancel := context.WithTimeout(ctx, productionCommandTimeout)
	defer cancel()
	cmd := exec.CommandContext(cmdCtx, "systemctl", "show", service,
		"--property=LoadState,ActiveState,SubState,Result,ExecMainPID,ExecMainStatus,NRestarts,UnitFileState,ActiveEnterTimestamp",
		"--no-pager")
	out, err := cmd.CombinedOutput()
	if errors.Is(cmdCtx.Err(), context.DeadlineExceeded) {
		return nil, fmt.Errorf("systemctl show %s timed out after %s", service, productionCommandTimeout)
	}
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg != "" {
			return nil, fmt.Errorf("%w: %s", err, msg)
		}
		return nil, err
	}
	return parseSystemctlShowOutput(string(out)), nil
}

func parseSystemctlShowOutput(output string) map[string]string {
	status := make(map[string]string)
	for _, line := range strings.Split(output, "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok || key == "" {
			continue
		}
		status[key] = value
	}
	return status
}

func classifyDoltServiceStatus(status map[string]string) CheckStatus {
	result := StatusOK
	if status["LoadState"] != "loaded" || status["ActiveState"] != "active" {
		result = maxStatus(result, StatusError)
	}
	unitState := status["UnitFileState"]
	switch unitState {
	case "enabled", "enabled-runtime":
	case "":
		result = maxStatus(result, StatusWarning)
	default:
		result = maxStatus(result, StatusError)
	}
	return result
}

func formatServiceStatusDetail(service string, status map[string]string) string {
	return fmt.Sprintf("%s: load=%s active=%s sub=%s enabled=%s result=%s pid=%s restarts=%s",
		service,
		status["LoadState"],
		status["ActiveState"],
		status["SubState"],
		status["UnitFileState"],
		status["Result"],
		status["ExecMainPID"],
		status["NRestarts"],
	)
}

type productionBDCanaryResult struct {
	Elapsed       time.Duration
	OutputBytes   int
	OutputPreview string
	Err           error
}

type productionBDCanaryDeps struct {
	runList func(context.Context, string) productionBDCanaryResult
}

func defaultProductionBDCanaryDeps() productionBDCanaryDeps {
	return productionBDCanaryDeps{runList: runBDListCanary}
}

type ProductionDoltQueryCanaryCheck struct {
	BaseCheck
	deps productionBDCanaryDeps
}

func NewProductionDoltQueryCanaryCheck() *ProductionDoltQueryCanaryCheck {
	return newProductionDoltQueryCanaryCheck(defaultProductionBDCanaryDeps())
}

func newProductionDoltQueryCanaryCheck(deps productionBDCanaryDeps) *ProductionDoltQueryCanaryCheck {
	return &ProductionDoltQueryCanaryCheck{
		BaseCheck: BaseCheck{
			CheckName:        "prod-dolt-query-canary",
			CheckDescription: "Run a bounded read-only bd list canary and report latency",
			CheckCategory:    CategoryProduction,
		},
		deps: deps,
	}
}

func (c *ProductionDoltQueryCanaryCheck) Run(ctx *CheckContext) *CheckResult {
	res := c.deps.runList(context.Background(), ctx.TownRoot)
	status := classifyBDCanary(res.Elapsed, res.Err)
	details := []string{
		"Command: bd --readonly list --limit=1 --json --flat",
		"Latency: " + res.Elapsed.Round(time.Millisecond).String(),
		fmt.Sprintf("Output bytes: %d", res.OutputBytes),
	}
	if res.OutputPreview != "" {
		details = append(details, "Output preview: "+res.OutputPreview)
	}
	if res.Err != nil {
		details = append(details, "Error: "+res.Err.Error())
	}

	message := "bd list canary OK"
	if status == StatusWarning {
		message = "bd list canary slow"
	}
	if status == StatusError {
		message = "bd list canary failed"
	}
	return &CheckResult{
		Name:    c.Name(),
		Status:  status,
		Message: message,
		Details: details,
	}
}

func runBDListCanary(ctx context.Context, townRoot string) productionBDCanaryResult {
	cmdCtx, cancel := context.WithTimeout(ctx, productionBDListTimeout)
	defer cancel()

	start := time.Now()
	cmd := exec.CommandContext(cmdCtx, "bd", "--readonly", "list", "--limit=1", "--json", "--flat")
	cmd.Dir = townRoot
	out, err := cmd.CombinedOutput()
	elapsed := time.Since(start)
	if errors.Is(cmdCtx.Err(), context.DeadlineExceeded) {
		err = fmt.Errorf("bd list timed out after %s", productionBDListTimeout)
	}

	return productionBDCanaryResult{
		Elapsed:       elapsed,
		OutputBytes:   len(out),
		OutputPreview: trimCommandOutput(string(out), 300),
		Err:           err,
	}
}

func classifyBDCanary(elapsed time.Duration, err error) CheckStatus {
	if err != nil {
		return StatusError
	}
	if elapsed >= productionBDListSlow {
		return StatusWarning
	}
	return StatusOK
}

type productionDaemonSnapshot struct {
	Running        bool
	PID            int
	StartedAt      time.Time
	LastHeartbeat  time.Time
	HeartbeatCount int64
}

type productionDaemonDeps struct {
	now      func() time.Time
	snapshot func(string) (productionDaemonSnapshot, error)
}

func defaultProductionDaemonDeps() productionDaemonDeps {
	return productionDaemonDeps{
		now:      time.Now,
		snapshot: readDaemonSnapshot,
	}
}

type ProductionDaemonHeartbeatCheck struct {
	BaseCheck
	deps productionDaemonDeps
}

func NewProductionDaemonHeartbeatCheck() *ProductionDaemonHeartbeatCheck {
	return newProductionDaemonHeartbeatCheck(defaultProductionDaemonDeps())
}

func newProductionDaemonHeartbeatCheck(deps productionDaemonDeps) *ProductionDaemonHeartbeatCheck {
	return &ProductionDaemonHeartbeatCheck{
		BaseCheck: BaseCheck{
			CheckName:        "prod-daemon-heartbeat",
			CheckDescription: "Check daemon running status and heartbeat age",
			CheckCategory:    CategoryProduction,
		},
		deps: deps,
	}
}

func (c *ProductionDaemonHeartbeatCheck) Run(ctx *CheckContext) *CheckResult {
	snap, err := c.deps.snapshot(ctx.TownRoot)
	if err != nil {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusError,
			Message: "daemon status unavailable",
			Details: []string{err.Error()},
		}
	}

	now := c.deps.now()
	status := classifyDaemonHeartbeat(snap.Running, snap.LastHeartbeat, now)
	startedAge := nonNegativeSub(now, snap.StartedAt)
	if snap.Running && snap.LastHeartbeat.IsZero() && !snap.StartedAt.IsZero() && startedAge < productionDaemonHeartbeatWarn {
		status = StatusOK
	}
	details := []string{
		fmt.Sprintf("Running: %t", snap.Running),
		fmt.Sprintf("PID: %d", snap.PID),
		fmt.Sprintf("Heartbeat count: %d", snap.HeartbeatCount),
	}
	if !snap.StartedAt.IsZero() {
		details = append(details, "Uptime: "+now.Sub(snap.StartedAt).Round(time.Second).String())
	}
	if !snap.LastHeartbeat.IsZero() {
		details = append(details, "Last heartbeat age: "+nonNegativeSub(now, snap.LastHeartbeat).Round(time.Second).String())
	} else {
		details = append(details, "Last heartbeat age: unknown")
	}

	message := "daemon heartbeat fresh"
	if !snap.Running {
		message = "daemon is not running"
	} else if snap.LastHeartbeat.IsZero() && status == StatusOK {
		message = "daemon recently started; heartbeat pending"
	} else if snap.LastHeartbeat.IsZero() {
		message = "daemon heartbeat missing"
	} else if status == StatusWarning {
		message = "daemon heartbeat stale"
	} else if status == StatusError {
		message = "daemon heartbeat very stale"
	}

	return &CheckResult{
		Name:    c.Name(),
		Status:  status,
		Message: message,
		Details: details,
	}
}

func readDaemonSnapshot(townRoot string) (productionDaemonSnapshot, error) {
	running, pid, err := daemon.IsRunning(townRoot)
	if err != nil {
		return productionDaemonSnapshot{}, err
	}
	state, err := daemon.LoadState(townRoot)
	if err != nil {
		return productionDaemonSnapshot{}, err
	}
	return productionDaemonSnapshot{
		Running:        running,
		PID:            pid,
		StartedAt:      state.StartedAt,
		LastHeartbeat:  state.LastHeartbeat,
		HeartbeatCount: state.HeartbeatCount,
	}, nil
}

func classifyDaemonHeartbeat(running bool, lastHeartbeat, now time.Time) CheckStatus {
	if !running {
		return StatusError
	}
	if lastHeartbeat.IsZero() {
		return StatusWarning
	}
	age := nonNegativeSub(now, lastHeartbeat)
	switch {
	case age >= productionDaemonHeartbeatFail:
		return StatusError
	case age >= productionDaemonHeartbeatWarn:
		return StatusWarning
	default:
		return StatusOK
	}
}

type productionDiskDeps struct {
	diskSpace func(string) (*util.DiskSpaceInfo, error)
}

func defaultProductionDiskDeps() productionDiskDeps {
	return productionDiskDeps{diskSpace: util.GetDiskSpace}
}

type ProductionFreeSpaceCheck struct {
	BaseCheck
	deps productionDiskDeps
}

func NewProductionFreeSpaceCheck() *ProductionFreeSpaceCheck {
	return newProductionFreeSpaceCheck(defaultProductionDiskDeps())
}

func newProductionFreeSpaceCheck(deps productionDiskDeps) *ProductionFreeSpaceCheck {
	return &ProductionFreeSpaceCheck{
		BaseCheck: BaseCheck{
			CheckName:        "prod-free-space",
			CheckDescription: "Check town disk and /dev/shm free space",
			CheckCategory:    CategoryProduction,
		},
		deps: deps,
	}
}

func (c *ProductionFreeSpaceCheck) Run(ctx *CheckContext) *CheckResult {
	status := StatusOK
	var details []string

	rootInfo, err := c.deps.diskSpace(ctx.TownRoot)
	if err != nil {
		status = maxStatus(status, StatusWarning)
		details = append(details, "Town root: unavailable ("+err.Error()+")")
	} else {
		status = maxStatus(status, classifyProductionRootSpace(rootInfo))
		details = append(details, formatSpaceDetail("Town root "+ctx.TownRoot, rootInfo))
	}

	shmInfo, err := c.deps.diskSpace("/dev/shm")
	if err != nil {
		status = maxStatus(status, StatusWarning)
		details = append(details, "/dev/shm: unavailable ("+err.Error()+")")
	} else {
		status = maxStatus(status, classifyProductionSHMSpace(shmInfo))
		details = append(details, formatSpaceDetail("/dev/shm", shmInfo))
	}

	message := "disk and /dev/shm space OK"
	if status == StatusWarning {
		message = "disk or /dev/shm space low"
	}
	if status == StatusError {
		message = "disk or /dev/shm space critical"
	}
	return &CheckResult{
		Name:    c.Name(),
		Status:  status,
		Message: message,
		Details: details,
	}
}

func classifyProductionRootSpace(info *util.DiskSpaceInfo) CheckStatus {
	if info == nil {
		return StatusWarning
	}
	if info.AvailableMB() < util.DiskSpaceMinimumMB || info.UsedPercent >= util.DiskSpaceCriticalPercent {
		return StatusError
	}
	if info.AvailableMB() < util.DiskSpaceWarningMB {
		return StatusWarning
	}
	return StatusOK
}

func classifyProductionSHMSpace(info *util.DiskSpaceInfo) CheckStatus {
	if info == nil {
		return StatusWarning
	}
	if info.AvailableBytes < productionSHMFailBytes || info.UsedPercent >= util.DiskSpaceCriticalPercent {
		return StatusError
	}
	if info.AvailableBytes < productionSHMWarnBytes {
		return StatusWarning
	}
	return StatusOK
}

func formatSpaceDetail(label string, info *util.DiskSpaceInfo) string {
	return fmt.Sprintf("%s: %s free of %s (%.1f%% used)",
		label,
		info.AvailableHuman(),
		util.FormatBytesHuman(info.TotalBytes),
		info.UsedPercent,
	)
}

type productionLoadAverages struct {
	One     float64
	Five    float64
	Fifteen float64
}

type productionLoadDeps struct {
	load     func() (productionLoadAverages, error)
	cpuCount func() int
}

func defaultProductionLoadDeps() productionLoadDeps {
	return productionLoadDeps{
		load:     readProcLoadavg,
		cpuCount: runtime.NumCPU,
	}
}

type ProductionLoadAverageCheck struct {
	BaseCheck
	deps productionLoadDeps
}

func NewProductionLoadAverageCheck() *ProductionLoadAverageCheck {
	return newProductionLoadAverageCheck(defaultProductionLoadDeps())
}

func newProductionLoadAverageCheck(deps productionLoadDeps) *ProductionLoadAverageCheck {
	return &ProductionLoadAverageCheck{
		BaseCheck: BaseCheck{
			CheckName:        "prod-load-average",
			CheckDescription: "Check system load average",
			CheckCategory:    CategoryProduction,
		},
		deps: deps,
	}
}

func (c *ProductionLoadAverageCheck) Run(ctx *CheckContext) *CheckResult {
	load, err := c.deps.load()
	if err != nil {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusWarning,
			Message: "load average unavailable",
			Details: []string{err.Error()},
		}
	}
	cpus := c.deps.cpuCount()
	status := classifyLoadAverage(load, cpus)
	message := "load average OK"
	if status == StatusWarning {
		message = "load average elevated"
	}
	if status == StatusError {
		message = "load average critical"
	}
	return &CheckResult{
		Name:    c.Name(),
		Status:  status,
		Message: message,
		Details: []string{
			fmt.Sprintf("Load average: %.2f %.2f %.2f", load.One, load.Five, load.Fifteen),
			fmt.Sprintf("CPU count: %d", normalizeCPUCount(cpus)),
		},
	}
}

func readProcLoadavg() (productionLoadAverages, error) {
	data, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return productionLoadAverages{}, err
	}
	return parseProcLoadavg(string(data))
}

func parseProcLoadavg(input string) (productionLoadAverages, error) {
	fields := strings.Fields(input)
	if len(fields) < 3 {
		return productionLoadAverages{}, fmt.Errorf("malformed /proc/loadavg: %q", strings.TrimSpace(input))
	}
	one, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return productionLoadAverages{}, fmt.Errorf("parse 1m load: %w", err)
	}
	five, err := strconv.ParseFloat(fields[1], 64)
	if err != nil {
		return productionLoadAverages{}, fmt.Errorf("parse 5m load: %w", err)
	}
	fifteen, err := strconv.ParseFloat(fields[2], 64)
	if err != nil {
		return productionLoadAverages{}, fmt.Errorf("parse 15m load: %w", err)
	}
	return productionLoadAverages{One: one, Five: five, Fifteen: fifteen}, nil
}

func classifyLoadAverage(load productionLoadAverages, cpus int) CheckStatus {
	cpuCount := float64(normalizeCPUCount(cpus))
	switch {
	case load.One >= cpuCount*2:
		return StatusError
	case load.One >= cpuCount*1.25:
		return StatusWarning
	default:
		return StatusOK
	}
}

func normalizeCPUCount(cpus int) int {
	if cpus < 1 {
		return 1
	}
	return cpus
}

type productionLedgerFile struct {
	Path string
	Size int64
}

type productionRejectLedgerDeps struct {
	glob func(string) ([]string, error)
	stat func(string) (os.FileInfo, error)
}

func defaultProductionRejectLedgerDeps() productionRejectLedgerDeps {
	return productionRejectLedgerDeps{
		glob: filepath.Glob,
		stat: os.Stat,
	}
}

type ProductionRejectLedgerCheck struct {
	BaseCheck
	deps productionRejectLedgerDeps
}

func NewProductionRejectLedgerCheck() *ProductionRejectLedgerCheck {
	return newProductionRejectLedgerCheck(defaultProductionRejectLedgerDeps())
}

func newProductionRejectLedgerCheck(deps productionRejectLedgerDeps) *ProductionRejectLedgerCheck {
	return &ProductionRejectLedgerCheck{
		BaseCheck: BaseCheck{
			CheckName:        "prod-reject-ledger",
			CheckDescription: "Check reject-driver ledger footprint",
			CheckCategory:    CategoryProduction,
		},
		deps: deps,
	}
}

func (c *ProductionRejectLedgerCheck) Run(ctx *CheckContext) *CheckResult {
	files, statWarnings, err := collectRejectLedgerFiles(ctx.TownRoot, c.deps)
	if err != nil {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusWarning,
			Message: "reject ledger size unavailable",
			Details: []string{err.Error()},
		}
	}

	var total int64
	details := append([]string{}, statWarnings...)
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	for _, file := range files {
		total += file.Size
		rel, relErr := filepath.Rel(ctx.TownRoot, file.Path)
		if relErr != nil {
			rel = file.Path
		}
		details = append(details, fmt.Sprintf("%s: %s", rel, formatInt64Bytes(file.Size)))
	}

	status := classifyRejectLedgerSize(total)
	if len(statWarnings) > 0 {
		status = maxStatus(status, StatusWarning)
	}
	message := "reject ledger footprint OK"
	if len(files) == 0 {
		message = "no reject ledgers found"
		details = append(details, "Checked: .runtime/reject-driver/ledger*.jsonl")
	} else if status == StatusWarning {
		message = "reject ledger footprint elevated"
	} else if status == StatusError {
		message = "reject ledger footprint critical"
	}
	if len(files) > 0 {
		details = append(details, "Total: "+formatInt64Bytes(total))
	}
	return &CheckResult{
		Name:    c.Name(),
		Status:  status,
		Message: message,
		Details: details,
	}
}

func collectRejectLedgerFiles(townRoot string, deps productionRejectLedgerDeps) ([]productionLedgerFile, []string, error) {
	patterns := []string{
		filepath.Join(townRoot, ".runtime", "reject-driver", "ledger-*.jsonl"),
		filepath.Join(townRoot, ".runtime", "reject-driver", "ledger.jsonl"),
	}
	seen := make(map[string]bool)
	var paths []string
	for _, pattern := range patterns {
		matches, err := deps.glob(pattern)
		if err != nil {
			return nil, nil, err
		}
		for _, match := range matches {
			if seen[match] {
				continue
			}
			seen[match] = true
			paths = append(paths, match)
		}
	}

	var files []productionLedgerFile
	var warnings []string
	for _, path := range paths {
		info, err := deps.stat(path)
		if err != nil {
			warnings = append(warnings, "Stat failed: "+path+" ("+err.Error()+")")
			continue
		}
		if info.IsDir() {
			continue
		}
		files = append(files, productionLedgerFile{Path: path, Size: info.Size()})
	}
	return files, warnings, nil
}

func classifyRejectLedgerSize(totalBytes int64) CheckStatus {
	switch {
	case totalBytes >= productionRejectLedgerFailBytes:
		return StatusError
	case totalBytes >= productionRejectLedgerWarnBytes:
		return StatusWarning
	default:
		return StatusOK
	}
}

type productionDoltListener struct {
	PID  int
	Port int
}

type productionDoltListenerDeps struct {
	listeners    func(context.Context) ([]productionDoltListener, error)
	expectedPort func(string) int
}

func defaultProductionDoltListenerDeps() productionDoltListenerDeps {
	return productionDoltListenerDeps{
		listeners: findDoltListeners,
		expectedPort: func(townRoot string) int {
			return doltserver.DefaultConfig(townRoot).Port
		},
	}
}

type ProductionRandomDoltListenersCheck struct {
	BaseCheck
	deps productionDoltListenerDeps
}

func NewProductionRandomDoltListenersCheck() *ProductionRandomDoltListenersCheck {
	return newProductionRandomDoltListenersCheck(defaultProductionDoltListenerDeps())
}

func newProductionRandomDoltListenersCheck(deps productionDoltListenerDeps) *ProductionRandomDoltListenersCheck {
	return &ProductionRandomDoltListenersCheck{
		BaseCheck: BaseCheck{
			CheckName:        "prod-random-dolt-listeners",
			CheckDescription: "Count orphan random-port Dolt sql-server listeners",
			CheckCategory:    CategoryProduction,
		},
		deps: deps,
	}
}

func (c *ProductionRandomDoltListenersCheck) Run(ctx *CheckContext) *CheckResult {
	listeners, err := c.deps.listeners(context.Background())
	if err != nil {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusWarning,
			Message: "Dolt listener scan unavailable",
			Details: []string{err.Error()},
		}
	}
	expectedPort := c.deps.expectedPort(ctx.TownRoot)
	if expectedPort <= 0 {
		expectedPort = doltserver.DefaultPort
	}
	random := randomPortDoltListeners(listeners, expectedPort)
	if len(random) == 0 {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusOK,
			Message: fmt.Sprintf("no random-port Dolt listeners found (expected port %d)", expectedPort),
		}
	}

	var details []string
	for _, listener := range random {
		details = append(details, fmt.Sprintf("PID %d listening on port %d", listener.PID, listener.Port))
	}
	return &CheckResult{
		Name:    c.Name(),
		Status:  StatusWarning,
		Message: fmt.Sprintf("%d random-port Dolt listener(s) found", len(random)),
		Details: details,
		FixHint: "Investigate before stopping any production Dolt process",
	}
}

func findDoltListeners(ctx context.Context) ([]productionDoltListener, error) {
	cmdCtx, cancel := context.WithTimeout(ctx, productionCommandTimeout)
	defer cancel()
	cmd := exec.CommandContext(cmdCtx, "lsof", "-a", "-c", "dolt", "-sTCP:LISTEN", "-i", "TCP", "-n", "-P", "-F", "pn")
	out, err := cmd.CombinedOutput()
	if errors.Is(cmdCtx.Err(), context.DeadlineExceeded) {
		return nil, fmt.Errorf("lsof Dolt listener scan timed out after %s", productionCommandTimeout)
	}
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && strings.TrimSpace(string(out)) == "" {
			return nil, nil
		}
		msg := trimCommandOutput(string(out), 300)
		if msg != "" {
			return nil, fmt.Errorf("%w: %s", err, msg)
		}
		return nil, err
	}
	return parseDoltListenersFromLsof(string(out)), nil
}

func parseDoltListenersFromLsof(output string) []productionDoltListener {
	var listeners []productionDoltListener
	currentPID := 0
	seen := make(map[productionDoltListener]bool)
	for _, line := range strings.Split(output, "\n") {
		if line == "" {
			continue
		}
		switch line[0] {
		case 'p':
			pid, err := strconv.Atoi(strings.TrimSpace(line[1:]))
			if err == nil {
				currentPID = pid
			}
		case 'n':
			if currentPID <= 0 {
				continue
			}
			port := parseListenerPort(line[1:])
			if port <= 0 {
				continue
			}
			listener := productionDoltListener{PID: currentPID, Port: port}
			if !seen[listener] {
				seen[listener] = true
				listeners = append(listeners, listener)
			}
		}
	}
	sort.Slice(listeners, func(i, j int) bool {
		if listeners[i].PID == listeners[j].PID {
			return listeners[i].Port < listeners[j].Port
		}
		return listeners[i].PID < listeners[j].PID
	})
	return listeners
}

func parseListenerPort(addr string) int {
	idx := strings.LastIndex(addr, ":")
	if idx < 0 || idx+1 >= len(addr) {
		return 0
	}
	port, err := strconv.Atoi(strings.TrimSpace(addr[idx+1:]))
	if err != nil {
		return 0
	}
	return port
}

func randomPortDoltListeners(listeners []productionDoltListener, expectedPort int) []productionDoltListener {
	if expectedPort <= 0 {
		expectedPort = doltserver.DefaultPort
	}
	var random []productionDoltListener
	seenPID := make(map[int]bool)
	for _, listener := range listeners {
		if listener.Port == expectedPort || seenPID[listener.PID] {
			continue
		}
		seenPID[listener.PID] = true
		random = append(random, listener)
	}
	sort.Slice(random, func(i, j int) bool {
		if random[i].PID == random[j].PID {
			return random[i].Port < random[j].Port
		}
		return random[i].PID < random[j].PID
	})
	return random
}

func nonNegativeSub(now, then time.Time) time.Duration {
	if now.Before(then) {
		return 0
	}
	return now.Sub(then)
}

func trimCommandOutput(output string, limit int) string {
	output = strings.TrimSpace(output)
	if limit <= 0 || len(output) <= limit {
		return output
	}
	return output[:limit] + "..."
}

func formatInt64Bytes(size int64) string {
	if size < 0 {
		return fmt.Sprintf("%d B", size)
	}
	return util.FormatBytesHuman(uint64(size))
}
