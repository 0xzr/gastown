package polecat

import (
	"os"
	"os/exec"
	"testing"
)

func TestDbgResetHead2(t *testing.T) {
	mgr, repoGit := setupPreserveManagerTest(t)
	if err := os.WriteFile(mgr.rig.Path+"/README.md", []byte("# modified WIP\n"), 0644); err != nil {
		t.Fatalf("modify: %v", err)
	}
	out, _ := exec.Command("git", "-C", mgr.rig.Path, "status", "--porcelain").Output()
	t.Logf("porcelain before snapshot: %q", string(out))
	sha, err := repoGit.PreserveWorktreeSnapshot()
	t.Logf("snapshot sha=%q err=%v", sha, err)
	out2, _ := exec.Command("git", "-C", mgr.rig.Path, "status", "--porcelain").Output()
	t.Logf("porcelain after snapshot: %q", string(out2))
	cached, _ := exec.Command("git", "-C", mgr.rig.Path, "diff", "--cached", "--name-only").Output()
	t.Logf("staged (cached) after snapshot: %q", string(cached))
}
