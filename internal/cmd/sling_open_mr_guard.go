package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/style"
)

var errSlingSkipped = errors.New("sling skipped")

type slingSkipError struct {
	msg string
}

func (e slingSkipError) Error() string {
	return e.msg
}

func (e slingSkipError) Is(target error) bool {
	return target == errSlingSkipped
}

func isSlingSkip(err error) bool {
	return errors.Is(err, errSlingSkipped)
}

func checkOpenMRDispatchGuard(townRoot, defaultBeadsDir, beadID string) error {
	if beadID == "" {
		return nil
	}
	if defaultBeadsDir == "" && townRoot != "" {
		defaultBeadsDir = filepath.Join(townRoot, ".beads")
	}
	if defaultBeadsDir == "" {
		return nil
	}

	bd := beadsForOpenMRDispatchGuard(townRoot, defaultBeadsDir, beadID)
	openMRs, err := bd.FindOpenMRsForIssue(beadID)
	if err != nil {
		// Availability wins here: if merge-queue state cannot be read, allow
		// dispatch instead of making transient bead-store problems strand work.
		fmt.Fprintf(os.Stderr, "%s Warning: could not read merge queue for bead %s: %v; allowing dispatch\n",
			style.Dim.Render("⚠"), beadID, err)
		return nil
	}
	if len(openMRs) == 0 {
		return nil
	}

	mr := openMRs[len(openMRs)-1]
	return slingSkipError{msg: fmt.Sprintf("bead %s has open MR %s — dispatch blocked; process the MR instead", beadID, mr.ID)}
}

func beadsForOpenMRDispatchGuard(townRoot, defaultBeadsDir, beadID string) *beads.Beads {
	beadsDir := beads.ResolveBeadsDirForID(defaultBeadsDir, beadID)
	workDir := filepath.Dir(beadsDir)
	if townRoot != "" && beadsDir == filepath.Join(townRoot, ".beads") {
		workDir = townRoot
	}
	return beads.NewWithBeadsDir(workDir, beadsDir)
}
