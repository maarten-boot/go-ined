package snmp

import (
	"context"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/maarten-boot/go-ined/internal/inventory"
	"github.com/maarten-boot/go-ined/pkg/apitypes"
)

// keepChecks is how many finished check jobs are kept.
const keepChecks = 20

// checks runs SNMP check jobs: "Check SNMP now" on a group or a selection
// (SPEC-515; Design-snmp-gui.md G1).
type checks struct {
	m        *Manager
	mu       sync.Mutex
	seq      int
	jobs     map[string]*check
	finished []string
	wg       sync.WaitGroup
}

type check struct {
	view   apitypes.SNMPCheck
	seq    int // the order of starting, for the list
	cancel context.CancelFunc
	done   chan struct{}
	sent   time.Time // the last progress event; under checks.mu
}

func newChecks(m *Manager) *checks { return &checks{m: m, jobs: map[string]*check{}} }

// StartCheck starts a check of a group (with its subgroups) or of hosts.
// It answers at once; snmp.check.progress (at most once a second) and
// snmp.check.finished follow.
func (m *Manager) StartCheck(in apitypes.SNMPCheckCreate) (apitypes.SNMPCheck, error) {
	var ids []int64
	switch {
	case (in.GroupID == nil) == (len(in.HostIDs) == 0):
		return apitypes.SNMPCheck{}, inventory.Errorf(inventory.Invalid, "give either group_id or host_ids")
	case in.GroupID != nil:
		var err error
		if ids, err = m.inv.HostsUnder(*in.GroupID); err != nil {
			return apitypes.SNMPCheck{}, err
		}
	default:
		for _, id := range in.HostIDs {
			if _, err := m.inv.SNMPTarget(id); err != nil {
				return apitypes.SNMPCheck{}, err
			}
			if !slices.Contains(ids, id) {
				ids = append(ids, id)
			}
		}
	}
	c := m.jobs
	c.mu.Lock()
	c.seq++
	id := "snmp-" + strconv.Itoa(c.seq)
	if m.cfg.RunID != "" {
		id = m.cfg.RunID + "-" + id
	}
	ctx, cancel := context.WithCancel(context.Background())
	j := &check{seq: c.seq, cancel: cancel, done: make(chan struct{}), view: apitypes.SNMPCheck{
		ID: id, GroupID: in.GroupID, State: apitypes.DiscoveryRunning, Total: len(ids),
		StartedAt: m.cfg.Now().UTC().Truncate(time.Millisecond),
	}}
	c.jobs[id] = j
	c.wg.Add(1)
	v := j.view
	c.mu.Unlock()
	go func() {
		defer c.wg.Done()
		c.run(ctx, j, ids)
	}()
	return v, nil
}

func (c *checks) run(ctx context.Context, j *check, ids []int64) {
	m := c.m
	sem := make(chan struct{}, m.cfg.Concurrency)
	var wg sync.WaitGroup
	progress := func() { // at most once a second
		c.mu.Lock()
		v, due := j.view, time.Since(j.sent) >= time.Second
		if due {
			j.sent = time.Now()
		}
		c.mu.Unlock()
		if due {
			m.publish(apitypes.EventSNMPCheckProgress, v)
		}
	}
	count := func(f func(v *apitypes.SNMPCheck)) {
		c.mu.Lock()
		f(&j.view)
		j.view.Checked++
		c.mu.Unlock()
	}
	for _, id := range ids {
		select {
		case <-ctx.Done():
		case sem <- struct{}{}:
		}
		if ctx.Err() != nil {
			break
		}
		wg.Go(func() {
			defer func() { <-sem }()
			t, err := m.inv.SNMPTarget(id)
			if err != nil || !m.mayAsk(t) {
				count(func(v *apitypes.SNMPCheck) { v.Skipped++ })
				return
			}
			m.mu.Lock()
			m.lists[id] = listKey(t)
			m.mu.Unlock()
			if _, err := m.find(ctx, t, true); err != nil || ctx.Err() != nil {
				if ctx.Err() == nil {
					m.log.Error("SNMP check failed", "host", id, "err", err)
				}
				count(func(v *apitypes.SNMPCheck) { v.Skipped++ })
				return
			}
			found := false
			if nt, err := m.inv.SNMPTarget(id); err == nil {
				found = nt.Agent.Version != ""
			}
			count(func(v *apitypes.SNMPCheck) {
				if found {
					v.Found++
				} else {
					v.NoAnswer++
				}
			})
			progress()
		})
	}
	wg.Wait()
	c.mu.Lock()
	j.view.State = apitypes.DiscoveryDone
	if ctx.Err() != nil {
		j.view.State = apitypes.DiscoveryCancelled
	}
	now := m.cfg.Now().UTC().Truncate(time.Millisecond)
	j.view.FinishedAt = &now
	v := j.view
	c.finished = append(c.finished, j.view.ID)
	for len(c.finished) > keepChecks {
		delete(c.jobs, c.finished[0])
		c.finished = c.finished[1:]
	}
	c.mu.Unlock()
	m.log.Info("SNMP check finished", "id", v.ID, "state", v.State, "total", v.Total, "found", v.Found,
		"no_answer", v.NoAnswer, "skipped", v.Skipped)
	m.publish(apitypes.EventSNMPCheckFinished, v)
	close(j.done)
}

func (m *Manager) publish(typ string, v any) {
	if m.cfg.Notify != nil {
		m.cfg.Notify.Publish(typ, v)
	}
}

// Checks lists the check jobs kept, oldest first.
func (m *Manager) Checks() []apitypes.SNMPCheck {
	c := m.jobs
	c.mu.Lock()
	defer c.mu.Unlock()
	js := make([]*check, 0, len(c.jobs))
	for _, j := range c.jobs {
		js = append(js, j)
	}
	slices.SortFunc(js, func(a, b *check) int { return a.seq - b.seq })
	out := make([]apitypes.SNMPCheck, len(js))
	for i, j := range js {
		out[i] = j.view
	}
	return out
}

// GetCheck returns a check job.
func (m *Manager) GetCheck(id string) (apitypes.SNMPCheck, error) {
	c := m.jobs
	c.mu.Lock()
	defer c.mu.Unlock()
	j := c.jobs[id]
	if j == nil {
		return apitypes.SNMPCheck{}, inventory.Errorf(inventory.NotFound, "SNMP check %s not found", id)
	}
	return j.view, nil
}

// CancelCheck stops a check job; it ends cancelled.
func (m *Manager) CancelCheck(id string) (apitypes.SNMPCheck, error) {
	c := m.jobs
	c.mu.Lock()
	j := c.jobs[id]
	c.mu.Unlock()
	if j == nil {
		return apitypes.SNMPCheck{}, inventory.Errorf(inventory.NotFound, "SNMP check %s not found", id)
	}
	j.cancel()
	return m.GetCheck(id)
}

// Close cancels the check jobs and waits for them (SPEC-804).
func (m *Manager) Close() {
	c := m.jobs
	c.mu.Lock()
	for _, j := range c.jobs {
		j.cancel()
	}
	c.mu.Unlock()
	c.wg.Wait()
}
