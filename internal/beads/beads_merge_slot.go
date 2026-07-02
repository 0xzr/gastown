// Package beads provides merge slot management for serialized conflict resolution.
//
// The merge slot is a single bead identified by the label "gt:merge-slot".
// Its holder is stored in the bead's Description field as a JSON blob:
//
//	{"holder": "<actor>", "waiters": ["<actor1>", ...]}
//
// When holder is empty the slot is available. The bd merge-slot command was
// removed in v0.62; this implementation uses standard bead CRUD operations
// (Create/List/Show/Update) that remain available in v0.62+.
package beads

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

const (
	mergeSlotLabel = "gt:merge-slot"
	mergeSlotTitle = "merge-slot"
)

// MergeSlotStatus represents the result of checking a merge slot.
type MergeSlotStatus struct {
	ID        string   `json:"id"`
	Available bool     `json:"available"`
	Holder    string   `json:"holder,omitempty"`
	Waiters   []string `json:"waiters,omitempty"`
	Error     string   `json:"error,omitempty"`
}

// mergeSlotData is the JSON structure stored in the merge slot bead's Description.
type mergeSlotData struct {
	Holder  string   `json:"holder"`
	Waiters []string `json:"waiters,omitempty"`
}

// parseMergeSlotData decodes the merge slot state from a bead's Description field.
// It returns an error when the description is present but not valid JSON, so
// callers can distinguish "slot is empty" from "slot data is corrupted".
func parseMergeSlotData(issue *Issue) (mergeSlotData, error) {
	if issue.Description == "" {
		return mergeSlotData{}, nil
	}
	var data mergeSlotData
	if err := json.Unmarshal([]byte(issue.Description), &data); err != nil {
		return mergeSlotData{}, fmt.Errorf("parsing merge slot data: %w", err)
	}
	return data, nil
}

// mergeSlotStatusFromIssue builds a MergeSlotStatus from a bead issue.
// When the description cannot be parsed, the returned status carries the parse
// error in its Error field so the caller can report it without masking it.
func mergeSlotStatusFromIssue(issue *Issue) *MergeSlotStatus {
	data, err := parseMergeSlotData(issue)
	if err != nil {
		return &MergeSlotStatus{
			ID:    issue.ID,
			Error: err.Error(),
		}
	}
	return &MergeSlotStatus{
		ID:        issue.ID,
		Available: data.Holder == "",
		Holder:    data.Holder,
		Waiters:   data.Waiters,
	}
}

// getMergeSlotBead finds the merge slot bead (label=gt:merge-slot, title=merge-slot).
// Returns ErrNotFound if no slot bead exists.
func (b *Beads) getMergeSlotBead() (*Issue, error) {
	issues, err := b.List(ListOptions{Label: mergeSlotLabel})
	if err != nil {
		return nil, fmt.Errorf("listing merge slot beads: %w", err)
	}

	var matches []*Issue
	for _, issue := range issues {
		if issue.Title == mergeSlotTitle {
			matches = append(matches, issue)
		}
	}
	if len(matches) == 0 {
		return nil, ErrNotFound
	}
	if len(matches) > 1 {
		ids := make([]string, 0, len(matches))
		for _, issue := range matches {
			ids = append(ids, issue.ID)
		}
		sort.Strings(ids)
		return nil, fmt.Errorf("ambiguous merge slot beads with title %q and label %q: %s", mergeSlotTitle, mergeSlotLabel, strings.Join(ids, ", "))
	}

	// Show the bead to get its full Description (list output may be truncated).
	issue, err := b.Show(matches[0].ID)
	if err != nil {
		return nil, err
	}
	if issue.Title != mergeSlotTitle {
		return nil, ErrNotFound
	}
	return issue, nil
}

// MergeSlotCreate creates the merge slot bead for the current rig.
// The slot is used for serialized conflict resolution in the merge queue.
// Returns the slot ID if successful.
func (b *Beads) MergeSlotCreate() (string, error) {
	initial, _ := json.Marshal(mergeSlotData{})
	issue, err := b.Create(CreateOptions{
		Title:       mergeSlotTitle,
		Labels:      []string{mergeSlotLabel},
		Description: string(initial),
	})
	if err != nil {
		return "", fmt.Errorf("creating merge slot: %w", err)
	}
	return issue.ID, nil
}

// MergeSlotCheck checks the availability of the merge slot.
// Returns the current status including holder and waiters if held.
func (b *Beads) MergeSlotCheck() (*MergeSlotStatus, error) {
	issue, err := b.getMergeSlotBead()
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return &MergeSlotStatus{Error: "not found"}, nil
		}
		return nil, fmt.Errorf("checking merge slot: %w", err)
	}
	return mergeSlotStatusFromIssue(issue), nil
}

// MergeSlotAcquire attempts to acquire the merge slot for exclusive access.
// If holder is empty, defaults to the configured actor.
// If addWaiter is true and the slot is held, the requester is added to the
// waiters queue (informational; callers use retries for contention handling).
// Returns the acquisition result.
func (b *Beads) MergeSlotAcquire(holder string, addWaiter bool) (*MergeSlotStatus, error) {
	if holder == "" {
		holder = b.getActor()
	}

	issue, err := b.getMergeSlotBead()
	if err != nil {
		return nil, fmt.Errorf("acquiring merge slot: %w", err)
	}

	data, err := parseMergeSlotData(issue)
	if err != nil {
		return nil, fmt.Errorf("acquiring merge slot: %w", err)
	}

	if data.Holder != "" && data.Holder != holder {
		// Slot is held by someone else.
		if addWaiter {
			// Add to waiters list if not already present.
			alreadyWaiting := false
			for _, w := range data.Waiters {
				if w == holder {
					alreadyWaiting = true
					break
				}
			}
			if !alreadyWaiting {
				data.Waiters = append(data.Waiters, holder)
				newDesc, _ := json.Marshal(data)
				desc := string(newDesc)
				if err := b.Update(issue.ID, UpdateOptions{Description: &desc}); err != nil {
					return nil, fmt.Errorf("adding merge slot waiter: %w", err)
				}
			}
		}
		return &MergeSlotStatus{
			ID:      issue.ID,
			Holder:  data.Holder,
			Waiters: data.Waiters,
		}, nil
	}

	// Slot is available or we already hold it — acquire.
	data.Holder = holder
	// Remove from waiters if present.
	filtered := data.Waiters[:0]
	for _, w := range data.Waiters {
		if w != holder {
			filtered = append(filtered, w)
		}
	}
	data.Waiters = filtered

	newDesc, _ := json.Marshal(data)
	desc := string(newDesc)
	if err := b.Update(issue.ID, UpdateOptions{Description: &desc}); err != nil {
		return nil, fmt.Errorf("acquiring merge slot: %w", err)
	}

	return &MergeSlotStatus{
		ID:        issue.ID,
		Available: false,
		Holder:    holder,
		Waiters:   data.Waiters,
	}, nil
}

// MergeSlotRelease releases the merge slot after conflict resolution completes.
// If holder is provided, it verifies the slot is held by that holder before releasing.
func (b *Beads) MergeSlotRelease(holder string) error {
	issue, err := b.getMergeSlotBead()
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil // Nothing to release
		}
		return fmt.Errorf("releasing merge slot: %w", err)
	}

	data, err := parseMergeSlotData(issue)
	if err != nil {
		return fmt.Errorf("releasing merge slot: %w", err)
	}

	if data.Holder == "" {
		return nil // Already available
	}
	if holder != "" && data.Holder != holder {
		return fmt.Errorf("slot release failed: held by %q, not %q", data.Holder, holder)
	}

	// Clear holder; promote first waiter if any.
	var newHolder string
	var remainingWaiters []string
	if len(data.Waiters) > 0 {
		newHolder = data.Waiters[0]
		remainingWaiters = data.Waiters[1:]
	}

	newData := mergeSlotData{Holder: newHolder, Waiters: remainingWaiters}
	newDesc, _ := json.Marshal(newData)
	desc := string(newDesc)

	if err := b.Update(issue.ID, UpdateOptions{Description: &desc}); err != nil {
		return fmt.Errorf("releasing merge slot: %w", err)
	}

	return nil
}

// MergeSlotEnsureExists creates the merge slot if it doesn't exist.
// This is idempotent - safe to call multiple times.
//
// Atomicity: uses create-then-check (upsert) rather than check-then-create
// to avoid a TOCTOU race. Two callers that both observe "not found" and then
// both call Create would either collide (duplicate label error) or — worse,
// create duplicate slot beads on different rigs. By attempting Create first
// and falling back to a lookup on failure, the second caller always finds
// the slot created by the first (gastown-rvwqi).
func (b *Beads) MergeSlotEnsureExists() (string, error) {
	// Try to create the slot atomically. On a fresh rig this succeeds
	// and we return immediately.
	id, err := b.MergeSlotCreate()
	if err == nil {
		return id, nil
	}

	// Create failed — most likely because another caller raced us and
	// created the slot first (the bead would be reported as a duplicate
	// by `bd create`). Fall back to a lookup; if the slot now exists we
	// return its ID. This matches the EnsureRigBead pattern.
	status, checkErr := b.MergeSlotCheck()
	if checkErr != nil {
		return "", fmt.Errorf("ensuring merge slot: create failed (%w) and check failed (%v)", err, checkErr)
	}
	if status.Error == "not found" {
		// Slot still doesn't exist after a failed Create — surface the
		// original create error rather than silently inventing a slot.
		return "", fmt.Errorf("ensuring merge slot: %w", err)
	}
	if status.Error != "" {
		// Slot exists but its description could not be parsed. Surface the
		// corruption instead of returning a slot with unusable data.
		return "", fmt.Errorf("ensuring merge slot: corrupt merge slot data: %s", status.Error)
	}
	return status.ID, nil
}
