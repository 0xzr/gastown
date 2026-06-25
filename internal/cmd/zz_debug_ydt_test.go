package cmd

import (
	"io"
	"os"
	"testing"
)

func TestDebugYDT_NoHumanReadable(t *testing.T) {
	testDAG := newTestDAG(t).
		Task("gt-j1", "JSON Task 1", withRig("gastown")).
		Task("gt-j2", "JSON Task 2", withRig("gastown")).BlockedBy("gt-j1")
	testDAG.Setup(t)

	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	oldStderr := os.Stderr
	rErr, wErr, _ := os.Pipe()
	os.Stderr = wErr

	convoyStageJSON = true
	defer func() { convoyStageJSON = false }()

	err := runConvoyStage(nil, []string{"gt-j1", "gt-j2"})
	w.Close()
	wErr.Close()
	os.Stdout = oldStdout
	os.Stderr = oldStderr

	outBytes, _ := io.ReadAll(r)
	output := string(outBytes)
	errBytes, _ := io.ReadAll(rErr)
	t.Logf("runConvoyStage err = %v", err)
	t.Logf("STDOUT=[%s]", output)
	t.Logf("STDERR=[%s]", string(errBytes))
}

func TestDebugYDT_NoRig(t *testing.T) {
	testDAG := newTestDAG(t).
		Task("zz-norig", "No Rig Task", "")
	testDAG.Setup(t)

	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	convoyStageJSON = true
	defer func() { convoyStageJSON = false }()

	err := runConvoyStage(nil, []string{"zz-norig"})
	w.Close()
	os.Stdout = oldStdout
	outBytes, _ := io.ReadAll(r)
	t.Logf("runConvoyStage err = %v", err)
	t.Logf("STDOUT=[%s]", string(outBytes))
}
