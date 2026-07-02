package doctor

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/steveyegge/gastown/internal/doltserver"
)

// ProductionChecks returns the focused, read-only checks for production triage.
func ProductionChecks() []Check {
	doltDeps := defaultProductionDoltDeps()
	return []Check{
		newProductionDoltServiceCheck(defaultProductionServiceDeps()),
		newProductionDoltDatabasesCheck(doltDeps),
		newProductionDoltQueryCanaryCheck(defaultProductionBDCanaryDeps()),
		newProductionDaemonHeartbeatCheck(defaultProductionDaemonDeps()),
		NewProductionTmuxOwnershipCheck(),
		newProductionFreeSpaceCheck(defaultProductionDiskDeps()),
		newProductionLoadAverageCheck(defaultProductionLoadDeps()),
		newProductionRejectLedgerCheck(defaultProductionRejectLedgerDeps()),
		newProductionRandomDoltListenersCheck(defaultProductionDoltListenerDeps()),
		newProductionLegacyDatabasePollutionCheck(doltDeps),
		newProductionUMANSEvidenceCheck(defaultProductionUMANSDeps()),
	}
}

type productionDoltDeps struct {
	configuredDatabases func(string) []string
	listDatabases       func(string) ([]string, error)
	protectedDatabases  func() []string
}

func defaultProductionDoltDeps() productionDoltDeps {
	return productionDoltDeps{
		configuredDatabases: doltserver.ConfiguredProductionDatabases,
		listDatabases:       doltserver.ListDatabases,
		protectedDatabases:  doltserver.ProtectedSharedServerDatabaseNames,
	}
}

type ProductionDoltDatabasesCheck struct {
	BaseCheck
	deps productionDoltDeps
}

func NewProductionDoltDatabasesCheck() *ProductionDoltDatabasesCheck {
	return newProductionDoltDatabasesCheck(defaultProductionDoltDeps())
}

func newProductionDoltDatabasesCheck(deps productionDoltDeps) *ProductionDoltDatabasesCheck {
	return &ProductionDoltDatabasesCheck{
		BaseCheck: BaseCheck{
			CheckName:        "prod-dolt-databases",
			CheckDescription: "Expose configured and available production Dolt databases",
			CheckCategory:    CategoryProduction,
		},
		deps: deps,
	}
}

func (c *ProductionDoltDatabasesCheck) Run(ctx *CheckContext) *CheckResult {
	configured := cleanStrings(c.deps.configuredDatabases(ctx.TownRoot))
	if len(configured) == 0 {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusError,
			Message: "no production databases configured",
			Details: []string{"Expected at least the town-level HQ database"},
		}
	}

	available, err := c.deps.listDatabases(ctx.TownRoot)
	if err != nil {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusError,
			Message: "cannot list Dolt databases",
			Details: []string{err.Error(), "Configured: " + formatStringList(configured)},
		}
	}
	available = cleanSortedStrings(available)

	missing := missingStrings(configured, available)
	details := []string{
		"Configured: " + formatStringList(configured),
		"Available: " + formatStringList(available),
		"Protected shared: " + formatStringList(cleanSortedStrings(c.deps.protectedDatabases())),
	}
	if len(missing) > 0 {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusError,
			Message: "missing configured production DB(s): " + formatStringList(missing),
			Details: details,
		}
	}

	return &CheckResult{
		Name:    c.Name(),
		Status:  StatusOK,
		Message: "configured production DBs: " + formatStringList(configured),
		Details: details,
	}
}

type ProductionLegacyDatabasePollutionCheck struct {
	BaseCheck
	deps        productionDoltDeps
	legacyNames []string
}

func NewProductionLegacyDatabasePollutionCheck() *ProductionLegacyDatabasePollutionCheck {
	return newProductionLegacyDatabasePollutionCheck(defaultProductionDoltDeps())
}

func newProductionLegacyDatabasePollutionCheck(deps productionDoltDeps) *ProductionLegacyDatabasePollutionCheck {
	return &ProductionLegacyDatabasePollutionCheck{
		BaseCheck: BaseCheck{
			CheckName:        "prod-hardcoded-db-pollution",
			CheckDescription: "Detect legacy hardcoded gt/mo Dolt databases in production",
			CheckCategory:    CategoryProduction,
		},
		deps:        deps,
		legacyNames: []string{"gt", "mo"},
	}
}

func (c *ProductionLegacyDatabasePollutionCheck) Run(ctx *CheckContext) *CheckResult {
	configured := cleanStrings(c.deps.configuredDatabases(ctx.TownRoot))
	available, err := c.deps.listDatabases(ctx.TownRoot)
	if err != nil {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusError,
			Message: "cannot list Dolt databases",
			Details: []string{err.Error()},
		}
	}
	available = cleanSortedStrings(available)

	legacy := cleanSortedStrings(c.legacyNames)
	configuredHits := intersectStrings(configured, legacy)
	availableHits := intersectStrings(available, legacy)
	details := []string{
		"Checked legacy names: " + formatStringList(legacy),
		"Configured: " + formatStringList(configured),
		"Available: " + formatStringList(available),
	}

	if len(configuredHits) > 0 || len(availableHits) > 0 {
		hits := cleanSortedStrings(append(configuredHits, availableHits...))
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusError,
			Message: "legacy hardcoded DB(s) present: " + formatStringList(hits),
			Details: details,
			FixHint: "Do not remove live Dolt data from doctor; hand off for production cleanup",
		}
	}

	return &CheckResult{
		Name:    c.Name(),
		Status:  StatusOK,
		Message: "no legacy gt/mo databases found",
		Details: details,
	}
}

type productionUMANSDeps struct {
	homeDir       func() (string, error)
	now           func() time.Time
	serviceStatus func(context.Context, string) (map[string]string, error)
	readLastLine  func(string) (logEvidence, error)
	statFile      func(string) (os.FileInfo, error)
}

func defaultProductionUMANSDeps() productionUMANSDeps {
	return productionUMANSDeps{
		homeDir:       os.UserHomeDir,
		now:           time.Now,
		serviceStatus: systemdUserServiceStatus,
		readLastLine:  readLastLogLine,
		statFile:      os.Stat,
	}
}

type logEvidence struct {
	Path    string
	Line    string
	ModTime time.Time
}

type ProductionUMANSEvidenceCheck struct {
	BaseCheck
	deps productionUMANSDeps
}

func NewProductionUMANSEvidenceCheck() *ProductionUMANSEvidenceCheck {
	return newProductionUMANSEvidenceCheck(defaultProductionUMANSDeps())
}

func newProductionUMANSEvidenceCheck(deps productionUMANSDeps) *ProductionUMANSEvidenceCheck {
	return &ProductionUMANSEvidenceCheck{
		BaseCheck: BaseCheck{
			CheckName:        "prod-umans-evidence",
			CheckDescription: "Read UMANS proxy, canary, and token-lint production evidence",
			CheckCategory:    CategoryProduction,
		},
		deps: deps,
	}
}

func (c *ProductionUMANSEvidenceCheck) Run(ctx *CheckContext) *CheckResult {
	home, err := c.deps.homeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusWarning,
			Message: "UMANS evidence unavailable",
			Details: []string{"Cannot resolve user home directory"},
		}
	}

	status := StatusOK
	var details []string

	status = maxStatus(status, c.checkHelper(filepath.Join(home, ".local", "bin", "umans-proxy-canary.sh"), &details))
	status = maxStatus(status, c.checkHelper(filepath.Join(home, ".local", "bin", "umans-token-lint.sh"), &details))

	service, err := c.deps.serviceStatus(context.Background(), "umans-proxy.service")
	if err != nil {
		status = maxStatus(status, StatusWarning)
		details = append(details, "Service: unavailable ("+err.Error()+")")
	} else {
		serviceLine := fmt.Sprintf("Service: load=%s active=%s sub=%s result=%s pid=%s",
			service["LoadState"], service["ActiveState"], service["SubState"], service["Result"], service["ExecMainPID"])
		details = append(details, serviceLine)
		if service["LoadState"] == "loaded" && service["ActiveState"] != "active" {
			status = maxStatus(status, StatusError)
		} else if service["LoadState"] != "loaded" {
			status = maxStatus(status, StatusWarning)
		}
	}

	logDir := filepath.Join(home, "umans-dash", ".logs")
	status = maxStatus(status, c.checkLog("canary", filepath.Join(logDir, "umans-canary.log"), 30*time.Minute, &details))
	status = maxStatus(status, c.checkLog("token-lint", filepath.Join(logDir, "umans-token-lint.log"), 2*time.Hour, &details))

	if len(details) == 0 {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusWarning,
			Message: "UMANS evidence unavailable",
		}
	}

	message := "UMANS service/log evidence healthy"
	if status == StatusWarning {
		message = "UMANS evidence has warnings"
	}
	if status == StatusError {
		message = "UMANS proxy service unhealthy"
	}
	return &CheckResult{
		Name:    c.Name(),
		Status:  status,
		Message: message,
		Details: details,
	}
}

func (c *ProductionUMANSEvidenceCheck) checkHelper(path string, details *[]string) CheckStatus {
	info, err := c.deps.statFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			*details = append(*details, "Helper missing: "+path)
			return StatusWarning
		}
		*details = append(*details, "Helper unreadable: "+path+" ("+err.Error()+")")
		return StatusWarning
	}
	if info.IsDir() {
		*details = append(*details, "Helper is a directory: "+path)
		return StatusWarning
	}
	if info.Mode()&0o111 == 0 {
		*details = append(*details, "Helper not executable: "+path)
		return StatusWarning
	}
	*details = append(*details, "Helper present: "+path)
	return StatusOK
}

func (c *ProductionUMANSEvidenceCheck) checkLog(label, path string, staleAfter time.Duration, details *[]string) CheckStatus {
	ev, err := c.deps.readLastLine(path)
	if err != nil {
		if os.IsNotExist(err) {
			*details = append(*details, fmt.Sprintf("%s log missing: %s", label, path))
		} else {
			*details = append(*details, fmt.Sprintf("%s log unreadable: %s (%v)", label, path, err))
		}
		return StatusWarning
	}

	now := c.deps.now()
	age := now.Sub(ev.ModTime)
	if age < 0 {
		age = 0
	}
	lineStatus := classifyUMANSLogLine(ev.Line)
	parsedAt, ok := parseLogTimestamp(ev.Line)
	if ok {
		logAge := now.Sub(parsedAt)
		if logAge >= 0 && logAge > age {
			age = logAge
		}
	}

	*details = append(*details, fmt.Sprintf("%s log: %s (age %s)", label, ev.Line, age.Round(time.Second)))
	if age > staleAfter {
		return maxStatus(lineStatus, StatusWarning)
	}
	return lineStatus
}

func classifyUMANSLogLine(line string) CheckStatus {
	upper := strings.ToUpper(line)
	switch {
	case strings.Contains(upper, "CRITICAL"), strings.Contains(upper, "ERROR"):
		return StatusError
	case strings.Contains(upper, "FAIL"), strings.Contains(upper, "ALERT"), strings.Contains(upper, "RECOVERED"):
		return StatusWarning
	default:
		return StatusOK
	}
}

func parseLogTimestamp(line string) (time.Time, bool) {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, fields[0])
	if err == nil {
		return t, true
	}
	return time.Time{}, false
}

func systemdUserServiceStatus(ctx context.Context, service string) (map[string]string, error) {
	cmdCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cmdCtx, "systemctl", "--user", "show", service,
		"--property=LoadState,ActiveState,SubState,Result,ExecMainPID,ExecMainStatus,NRestarts,UnitFileState,FragmentPath,ActiveEnterTimestamp",
		"--no-pager")
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg != "" {
			return nil, fmt.Errorf("%w: %s", err, msg)
		}
		return nil, err
	}

	status := make(map[string]string)
	for _, line := range strings.Split(string(out), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok || key == "" {
			continue
		}
		status[key] = value
	}
	return status, nil
}

func readLastLogLine(path string) (logEvidence, error) {
	f, err := os.Open(path) //nolint:gosec // doctor reads fixed production evidence paths.
	if err != nil {
		return logEvidence{Path: path}, err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return logEvidence{Path: path}, err
	}

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024), 1024*1024)
	var last string
	for scanner.Scan() {
		if line := strings.TrimSpace(scanner.Text()); line != "" {
			last = line
		}
	}
	if err := scanner.Err(); err != nil {
		return logEvidence{Path: path}, err
	}
	if last == "" {
		return logEvidence{Path: path, ModTime: info.ModTime()}, fmt.Errorf("empty log")
	}
	return logEvidence{Path: path, Line: last, ModTime: info.ModTime()}, nil
}

func cleanSortedStrings(values []string) []string {
	cleaned := cleanStrings(values)
	sort.Strings(cleaned)
	return cleaned
}

func cleanStrings(values []string) []string {
	seen := make(map[string]bool)
	cleaned := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		cleaned = append(cleaned, value)
	}
	return cleaned
}

func missingStrings(want, have []string) []string {
	haveSet := make(map[string]bool, len(have))
	for _, value := range have {
		haveSet[value] = true
	}
	var missing []string
	for _, value := range want {
		if !haveSet[value] {
			missing = append(missing, value)
		}
	}
	return missing
}

func intersectStrings(left, right []string) []string {
	rightSet := make(map[string]bool, len(right))
	for _, value := range right {
		rightSet[value] = true
	}
	var out []string
	for _, value := range left {
		if rightSet[value] {
			out = append(out, value)
		}
	}
	return cleanSortedStrings(out)
}

func formatStringList(values []string) string {
	if len(values) == 0 {
		return "(none)"
	}
	return strings.Join(values, ",")
}

func maxStatus(a, b CheckStatus) CheckStatus {
	if b > a {
		return b
	}
	return a
}
