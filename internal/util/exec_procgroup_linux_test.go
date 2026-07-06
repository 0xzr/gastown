//go:build linux

package util

import (
	"os/exec"
	"syscall"
	"testing"
)

func TestSetTestProcessGroup_SetsPdeathsig(t *testing.T) {
	cmd := exec.Command("true")
	SetTestProcessGroup(cmd)
	if cmd.SysProcAttr == nil {
		t.Fatal("SetTestProcessGroup did not set SysProcAttr")
	}
	if !cmd.SysProcAttr.Setpgid {
		t.Error("expected Setpgid")
	}
	if cmd.SysProcAttr.Pdeathsig != syscall.SIGKILL {
		t.Errorf("expected Pdeathsig=SIGKILL, got %v", cmd.SysProcAttr.Pdeathsig)
	}
}
