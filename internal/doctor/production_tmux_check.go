package doctor

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/steveyegge/gastown/internal/session"
	"github.com/steveyegge/gastown/internal/tmux"
)

const (
	productionTmuxSocketWarn = 20
	productionTmuxSocketFail = 50
)

type productionTmuxDeps struct {
	expectedSocket func(string) string
	serverInfo     func(string) (*tmux.ServerInfo, error)
	countSockets   func(string) (int, []string, error)
	socketDir      func() string
}

func defaultProductionTmuxDeps() productionTmuxDeps {
	return productionTmuxDeps{
		expectedSocket: session.ExpectedTownSocketName,
		serverInfo: func(socket string) (*tmux.ServerInfo, error) {
			return tmux.NewTmuxWithSocket(socket).ServerInfo()
		},
		countSockets: countTmuxSockets,
		socketDir:    tmux.SocketDir,
	}
}

type ProductionTmuxOwnershipCheck struct {
	BaseCheck
	deps productionTmuxDeps
}

func NewProductionTmuxOwnershipCheck() *ProductionTmuxOwnershipCheck {
	return newProductionTmuxOwnershipCheck(defaultProductionTmuxDeps())
}

func newProductionTmuxOwnershipCheck(deps productionTmuxDeps) *ProductionTmuxOwnershipCheck {
	return &ProductionTmuxOwnershipCheck{
		BaseCheck: BaseCheck{
			CheckName:        "prod-tmux-ownership",
			CheckDescription: "Verify production tmux socket is gt-owned and socket count is sane",
			CheckCategory:    CategoryProduction,
		},
		deps: deps,
	}
}

func (c *ProductionTmuxOwnershipCheck) Run(ctx *CheckContext) *CheckResult {
	expected := c.deps.expectedSocket(ctx.TownRoot)
	info, err := c.deps.serverInfo(expected)
	if err != nil {
		status := StatusError
		message := fmt.Sprintf("production tmux server missing on socket %q", expected)
		if !errors.Is(err, tmux.ErrNoServer) {
			message = fmt.Sprintf("cannot inspect production tmux socket %q", expected)
		}
		return &CheckResult{
			Name:    c.Name(),
			Status:  status,
			Message: message,
			Details: []string{
				"Socket path: " + tmux.SocketPath(expected),
				err.Error(),
			},
			FixHint: "Restart the daemon to bootstrap a gt-owned tmux server, then let Gas Town agents restart normally",
		}
	}

	socketCount, socketNames, countErr := c.deps.countSockets(c.deps.socketDir())
	details := []string{
		fmt.Sprintf("Socket: %s (%s)", expected, tmux.SocketPath(expected)),
		fmt.Sprintf("Server PID: %d", info.PID),
		"Owner: " + emptyAs(info.Owner, "<unset>"),
		"Town root: " + emptyAs(info.TownRoot, "<unset>"),
		"Recorded socket: " + emptyAs(info.RecordedSocket, "<unset>"),
		"Origin: " + emptyAs(info.Origin, "<unset>"),
		"Origin session: " + emptyAs(info.OriginSession, "<unset>"),
		fmt.Sprintf("Sessions: %s", formatStringList(cleanSortedStrings(info.Sessions))),
	}
	if info.Argv != "" {
		details = append(details, "Server argv: "+info.Argv)
	}
	if info.OriginArgv != "" {
		details = append(details, "Origin argv: "+info.OriginArgv)
	}
	if countErr != nil {
		details = append(details, "Socket count unavailable: "+countErr.Error())
	} else {
		details = append(details, fmt.Sprintf("tmux socket count: %d", socketCount))
		if socketCount >= productionTmuxSocketWarn {
			details = append(details, "socket sample: "+formatStringList(sampleStrings(socketNames, 12)))
		}
	}

	var problems []string
	if info.SocketName != expected {
		problems = append(problems, fmt.Sprintf("inspected socket %q, expected %q", info.SocketName, expected))
	}
	if info.Owner != tmuxOwnerValue() {
		problems = append(problems, fmt.Sprintf("%s=%q", tmux.EnvTmuxOwner, info.Owner))
	}
	if info.TownRoot != ctx.TownRoot {
		problems = append(problems, fmt.Sprintf("%s=%q expected %q", tmux.EnvTmuxTownRoot, info.TownRoot, ctx.TownRoot))
	}
	if info.RecordedSocket != expected {
		problems = append(problems, fmt.Sprintf("%s=%q expected %q", tmux.EnvTmuxSocket, info.RecordedSocket, expected))
	}
	if !strings.HasPrefix(info.Origin, "gt-") {
		problems = append(problems, fmt.Sprintf("%s=%q is not gt-owned", tmux.EnvTmuxOrigin, info.Origin))
	}
	if hasBadTmuxOrigin(info.Argv) || hasBadTmuxOrigin(info.OriginArgv) || hasBadTmuxOrigin(info.OriginSession) {
		problems = append(problems, "tmux server origin looks like a test/refinery transient")
	}
	if countErr == nil && socketCount >= productionTmuxSocketFail {
		problems = append(problems, fmt.Sprintf("tmux socket count %d exceeds fail threshold %d", socketCount, productionTmuxSocketFail))
	}

	if len(problems) > 0 {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusError,
			Message: "production tmux server is not cleanly gt-owned",
			Details: append(details, "Problems: "+strings.Join(problems, "; ")),
			FixHint: "Run the M1.8 tmux migration/sweep: stop daemon, replace the accidental server with a gt-owned server, restart daemon, then remove stale gt-test/gastown-refinery sockets",
		}
	}
	if countErr == nil && socketCount >= productionTmuxSocketWarn {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusWarning,
			Message: fmt.Sprintf("production tmux server gt-owned, but socket count is high (%d)", socketCount),
			Details: details,
			FixHint: "Sweep dead gt-test/gastown-refinery socket files",
		}
	}

	return &CheckResult{
		Name:    c.Name(),
		Status:  StatusOK,
		Message: fmt.Sprintf("gt-owned tmux server on %s (pid=%d, sockets=%d)", expected, info.PID, socketCount),
		Details: details,
	}
}

func tmuxOwnerValue() string {
	return "gt"
}

func hasBadTmuxOrigin(s string) bool {
	return strings.Contains(s, "gt-test-") || strings.Contains(s, "gastown-refinery-")
}

func emptyAs(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

func countTmuxSockets(dir string) (int, []string, error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return 0, nil, nil
	}
	if err != nil {
		return 0, nil, err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.Type()&os.ModeSocket == 0 {
			continue
		}
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	return len(names), names, nil
}

func sampleStrings(values []string, max int) []string {
	if len(values) <= max {
		return values
	}
	out := append([]string(nil), values[:max]...)
	out = append(out, fmt.Sprintf("...+%d", len(values)-max))
	return out
}
