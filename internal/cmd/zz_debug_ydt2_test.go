package cmd

import (
	"io"
	"os"
	"strings"
	"testing"
)

func TestDebugYDT_Log(t *testing.T) {
	testDAG := newTestDAG(t).
		Task("gt-j1", "JSON Task 1", withRig("gastown")).
		Task("gt-j2", "JSON Task 2", withRig("gastown")).BlockedBy("gt-j1")
	_, logPath := testDAG.Setup(t)

	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	convoyStageJSON = true
	defer func() { convoyStageJSON = false }()
	_ = runConvoyStage(nil, []string{"gt-j1", "gt-j2"})
	w.Close()
	os.Stdout = oldStdout
	outBytes, _ := io.ReadAll(r)
	t.Logf("STDOUT=[%s]", string(outBytes))

	logBytes, _ := os.ReadFile(logPath)
	t.Logf("BD LOG:\n%s", string(logBytes))
	_ = strings.TrimSpace
}
