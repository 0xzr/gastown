//go:build !windows

package doltserver

import (
	"os/exec"
	"syscall"
	"testing"
)

func TestSetTestProcessGroupUsesParentDeathSignal(t *testing.T) {
	cmd := exec.Command("true")
	setTestProcessGroup(cmd)
	if cmd.SysProcAttr == nil {
		t.Fatal("setTestProcessGroup did not set SysProcAttr")
	}
	if !cmd.SysProcAttr.Setpgid {
		t.Fatal("setTestProcessGroup did not isolate the process group")
	}
	if cmd.SysProcAttr.Pdeathsig != syscall.SIGKILL {
		t.Fatalf("Pdeathsig = %v, want SIGKILL", cmd.SysProcAttr.Pdeathsig)
	}
}

func TestProductionProcessGroupDoesNotUseParentDeathSignal(t *testing.T) {
	cmd := exec.Command("true")
	setProcessGroup(cmd)
	if cmd.SysProcAttr == nil {
		t.Fatal("setProcessGroup did not set SysProcAttr")
	}
	if cmd.SysProcAttr.Pdeathsig != 0 {
		t.Fatalf("production Pdeathsig = %v, want 0", cmd.SysProcAttr.Pdeathsig)
	}
}
