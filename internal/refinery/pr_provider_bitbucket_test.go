package refinery

import "testing"

// TestBitbucketMergeCandidateBasis_FailClosedOnEmptyDestination pins rework
// finding 3 at the Bitbucket provider level: when the destination lookup
// fails or returns an empty/whitespace destination, mergeCandidateBasis must
// return UnknownBasis() rather than silently fall back to origin/main.
//
// The previous fix (3fd633e5) hardcoded baseRef := "main" and only overrode
// it when GetBitbucketPRDestination succeeded. For a non-main PR against
// "release" with a transient API failure, the verdict packet would have
// carried an origin/main basis — wrong diff, but a merge_candidate shape,
// so a stale APPROVED would authoritatively approve the merge under the
// fabricated basis. The rework flips this to fail-closed: no defensible
// basis means no authoritative verdict (gastown-6z5 rework).
func TestBitbucketMergeCandidateBasis_FailClosedOnEmptyDestination(t *testing.T) {
	p := &bitbucketPRProvider{}
	cases := []string{"", " ", "\t", "\n"}
	for _, in := range cases {
		got := p.mergeCandidateBasisForDestination(in)
		if got.Kind != "unknown" {
			t.Errorf("input %q: expected UnknownBasis, got %+v", in, got)
		}
		if got.IsMergeCandidate() {
			t.Errorf("input %q: fail-closed basis must not be a merge candidate, got %+v", in, got)
		}
	}
}
