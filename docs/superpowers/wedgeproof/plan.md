> Read the [repository context](../../README.md) before using copied commands or historical plans.

# Wedge-proof the opnsense chardev drainer: Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

> **Historical.** The gateway Go module moved to agoodkind/mwan in configs commit 6f18c39d. The `mwan/go`, `mwan/yang` and `third_party/yang` paths on this page refer to the tree as it was.

**Goal:** Make the mwan-opnsense break-glass channel impossible to permanently wedge, and bound the transient.

**Architecture:** Two invariants in the host drainer (`mwan/go/cmd/mwan/opnsense_drain.go`) and its systemd unit. Invariant 1: the chardev fd never closes while the VM is up, so qemu never strands the descriptor (no permanent wedge). Invariant 2: the chardev reader never blocks on the client, so a hung bridge cannot stall the drain (bounded transient).

**Tech Stack:** Go (FreeBSD plus linux build), systemd fd-store via `github.com/coreos/go-systemd/v22`, ansible-templated unit.

## Global Constraints

- The wedge signature: a CPU at RIP `ffffffff809e3xx` (virtqueue_poll) AND a CPU at `ffffffff80c32c5x` (lock_delay), both HLT=0. Idle: both HLT=1 at `ffffffff810b37c6`.
- Never drop chardev BYTES to a surviving client. Dropping bytes corrupts the framed yamux stream (the rejected design from commit 2f5f419). Drop the whole CLIENT instead, and flush queued bytes on a client swap.
- Do not change the documented transport invariants in `docs/ops/opnsense/daemon.md` ("What not to touch"): no-op `serialStream.Close`, termios, 8 KiB write cap, ack pacing, yamux keepalive off, named virtio port, TIOCFLUSH-on-open, session-rebuild loop.
- All Go build, lint, and test run via `make -C mwan/go check` and `make -C mwan/go test`, never raw go.
- Deploy units only via the ansible template path, never hand-edited on hosts.

---

## File structure

- `mwan/go/cmd/mwan/opnsense_drain.go`: reader/writer decouple with a bounded queue and client-swap flush (Task 1); fd-store never-close plus fresh-dial startup guard (Task 2).
- `mwan/go/cmd/mwan/opnsense_drain_test.go`: unit tests for both.
- The drain systemd unit template rendered by `ansible/playbooks/tasks/mwan-opnsense-host-deploy.yml`: `FileDescriptorStorePreserve=yes`, `RestartSec=0` (Task 3).

---

### Task 1: Decouple the chardev reader from the client write

The reader must never block on the client. A bounded queue sits between a reader goroutine (always drains the chardev) and a writer goroutine (forwards to the current client). On queue overflow the client is dropped, never the bytes of a surviving session. The queue is flushed on a client swap so a new session never receives the prior session's bytes.

**Files:**
- Modify: `mwan/go/cmd/mwan/opnsense_drain.go` (`drainHub`, `drainChardev`, `setClient`)
- Test: `mwan/go/cmd/mwan/opnsense_drain_test.go`

**Interfaces:**
- Consumes: `drainHub` (existing), `net.Conn` client and chardev.
- Produces: `drainChardev(ctx, log, hub, chardev) error` keeps its signature. Internally a reader goroutine plus a writer goroutine joined by a bounded `chan []byte`. `drainHub.setClient` flushes the pending queue on swap.

- [ ] **Step 1: Write the failing test for "reader never blocks on a hung client"**

```go
// In opnsense_drain_test.go. A hung client (never reads) must not stop the
// chardev drain: a large guest write still completes.
func TestDrainReaderNeverBlocksOnHungClient(t *testing.T) {
	chLocal, chRemote := socketPair(t)            // chLocal = drainer's chardev side
	defer chLocal.Close(); defer chRemote.Close()
	hubClient, hungBridge := socketPair(t)        // hubClient attached; hungBridge never reads
	defer hubClient.Close(); defer hungBridge.Close()
	ctx, cancel := context.WithCancel(context.Background()); defer cancel()
	hub := &drainHub{}
	hub.setClient(hubClient)
	go func() { _ = drainChardev(ctx, testLog(), hub, chLocal) }()
	payload := make([]byte, 4<<20)
	done := make(chan error, 1)
	go func() { _ = chRemote.SetWriteDeadline(time.Now().Add(5 * time.Second)); _, e := chRemote.Write(payload); done <- e }()
	select {
	case e := <-done:
		if e != nil { t.Fatalf("guest write stalled, reader blocked on hung client: %v", e) }
	case <-time.After(6 * time.Second):
		t.Fatal("guest write did not complete: reader blocked on the hung client (wedge risk)")
	}
}
```

- [ ] **Step 2: Run it and verify it fails.** Run `make -C mwan/go test`. The current blocking `c.Write` stalls the read. Expected: FAIL or timeout.

- [ ] **Step 3: Implement the decouple**

```go
const drainQueueDepth = 256 // ~16 MiB of 64 KiB chunks; a progressing client never fills it

// setClient swaps the client and flushes any queued bytes from the prior
// session, so a reconnecting bridge never receives the dead session's tail.
func (h *drainHub) setClient(c net.Conn) {
	h.mu.Lock()
	old := h.client
	h.client = c
	for h.toClient != nil { // drain prior-session bytes
		select {
		case <-h.toClient:
			continue
		default:
		}
		break
	}
	h.mu.Unlock()
	if old != nil { _ = old.Close() }
}

func drainChardev(ctx context.Context, log *slog.Logger, hub *drainHub, chardev net.Conn) error {
	dctx, dcancel := context.WithCancel(ctx); defer dcancel()
	q := make(chan []byte, drainQueueDepth)
	hub.mu.Lock(); hub.toClient = q; hub.mu.Unlock()
	defer func() { hub.mu.Lock(); hub.toClient = nil; hub.mu.Unlock() }()
	readErr := make(chan error, 1)
	spawn(ctx, log, "drain reader", func() { // never blocks on the client
		buf := make([]byte, drainBufSize)
		for {
			n, err := chardev.Read(buf)
			if n > 0 {
				data := make([]byte, n); copy(data, buf[:n])
				select {
				case q <- data:
				default: // queue full: client hung or too slow. Drop the CLIENT, never a survivor's bytes.
					if c := hub.getClient(); c != nil { hub.clearClient(c); _ = c.Close() }
				}
			}
			if err != nil { select { case readErr <- err: default: }; return }
		}
	})
	spawn(ctx, log, "drain writer", func() {
		for {
			select {
			case <-dctx.Done(): return
			case data := <-q:
				if c := hub.getClient(); c != nil {
					if _, werr := c.Write(data); werr != nil { hub.clearClient(c); _ = c.Close() }
				} // no client: discard (the chardev still drains, guest write completes)
			}
		}
	})
	select {
	case <-ctx.Done(): return nil
	case err := <-readErr: return err
	}
}
```

Add `toClient chan []byte` to the `drainHub` struct. Keep `getClient` and `clearClient` as is.

- [ ] **Step 4: Add the no-byte-loss-to-a-survivor test**

```go
// A client that keeps reading loses no bytes: what the guest writes arrives intact.
func TestDrainNoByteLossToReadingClient(t *testing.T) {
	chLocal, chRemote := socketPair(t); defer chLocal.Close(); defer chRemote.Close()
	hubClient, bridge := socketPair(t); defer hubClient.Close(); defer bridge.Close()
	ctx, cancel := context.WithCancel(context.Background()); defer cancel()
	hub := &drainHub{}; hub.setClient(hubClient)
	go func() { _ = drainChardev(ctx, testLog(), hub, chLocal) }()
	msg := bytes.Repeat([]byte("MWN1"), 4096) // 16 KiB
	go func() { _, _ = chRemote.Write(msg) }()
	got := readN(t, bridge, len(msg))
	if got != string(msg) { t.Fatalf("byte loss to a reading client") }
}
```

- [ ] **Step 5: Run tests and verify they pass.** Run `make -C mwan/go test`, then `make -C mwan/go check`. Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add mwan/go/cmd/mwan/opnsense_drain.go mwan/go/cmd/mwan/opnsense_drain_test.go
git commit -m "Decouple the opnsense drain reader from the client write"
```

---

### Task 2: Guarantee the chardev fd never closes (fd-store invariant)

The fd is stored before serving on the fresh-dial path, never closed on any drainer exit, and a startup guard makes a silent fresh re-dial impossible while the VM is up. A fresh dial is the only host-side disconnect window.

**Files:**
- Modify: `mwan/go/cmd/mwan/opnsense_drain.go` (`runOPNsenseHostDrain`, `openChardev`, `runDrainRelay`)
- Test: `mwan/go/cmd/mwan/opnsense_drain_test.go`

**Interfaces:**
- Consumes: `acquireChardev(ctx, log, byName, path) (net.Conn, adopted bool, err error)`, `storeChardevFD`.
- Produces: `openChardev` stores the fd whenever it dials fresh. A `reclaimExpected bool` (set true once an fd has been stored or adopted) gates a loud ERROR when a later fresh dial happens, since that is the disconnect window.

- [ ] **Step 1: Write the failing test for the unexpected fresh dial warning**

The implementer wires this against `openChardev` using the existing chardev test seam. The assertion: when `acquireChardev` returns `adopted=false` a second time while `reclaimExpected` is true, the drainer logs an ERROR containing `fresh chardev dial while a stored fd was expected`. Build the test from `TestDrainRelayEndToEnd`'s fake-chardev pattern.

```go
func TestFreshDialAfterStoreIsFlagged(t *testing.T) {
	var mu sync.Mutex
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	// drive runDrainRelay with an openChardev that dials fresh twice (no adopt),
	// then assert the second dial logged the disconnect-window error.
	_ = mu; _ = log
	t.Skip("implementer: wire to runDrainRelay/openChardev seam; assert the ERROR text on the 2nd fresh dial")
}
```

- [ ] **Step 2: Run it and verify it fails.** Run `make -C mwan/go test`.

- [ ] **Step 3: Implement the guard.** In `runOPNsenseHostDrain`/`openChardev`, track `reclaimExpected`, set true after the first successful store or adopt. When `acquireChardev` returns `adopted=false` while `reclaimExpected` is true, call `log.ErrorContext(ctx, "opnsense drain: fresh chardev dial while a stored fd was expected; this is the only wedge window")` before storing the new fd. Confirm with a comment that no path closes the chardev fd except the VM-down re-open and the ctx-cancel teardown.

- [ ] **Step 4: Run tests and check.** Run `make -C mwan/go test`, then `make -C mwan/go check`. Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add mwan/go/cmd/mwan/opnsense_drain.go mwan/go/cmd/mwan/opnsense_drain_test.go
git commit -m "Flag an unexpected fresh chardev dial in the opnsense drainer"
```

---

### Task 3: Unit preserves the fd-store across a crash and respawns fast

**Files:**
- Modify: the drain `.service` template rendered by `ansible/playbooks/tasks/mwan-opnsense-host-deploy.yml` (locate it first).

**Interfaces:**
- Produces: the unit declares `FileDescriptorStoreMax=1`, `FileDescriptorStorePreserve=yes`, `RestartSec=0`.

- [ ] **Step 1: Locate the unit template.** Run `grep -rln "FileDescriptorStoreMax|mwan-opnsense-drain" mwan ansible`. Expected: the `.service` or `.service.j2` and the deploy task.

- [ ] **Step 2: Add the directives.** In the `[Service]` block ensure:

```
FileDescriptorStoreMax=1
FileDescriptorStorePreserve=yes
RestartSec=0
Restart=always
```

- [ ] **Step 3: Verify the render.** Run `grep -nE "FileDescriptorStorePreserve|RestartSec" <template>`. Expected: both present.

- [ ] **Step 4: Commit**

```bash
git add <template path>
git commit -m "Preserve the drainer fd-store across crashes and respawn immediately"
```

---

## Validation (acceptance, run during execution, not a code task)

Deploy the new binary and unit to the testbed OPNsense guest via the surgical path, then run the sub-second wedge-proof harness: `qmp_sampler.py` (~60 ms QMP register sampling) plus the matrix driver. Drive both directions at >=256 MB cut deep mid-transfer, against every chardev-touching disruption, accumulation with no reset between trials, >=30 genuine trials per cell, with per-trial `.reg`, `.health`, and adopted-fd artifacts retained.

Pass criteria:
1. Zero PERMANENT wedge: every trial self-recovers to a working exec probe, recover-at recorded over a >=180 s window.
2. Any transient strand is bounded and self-clearing, with its duration measured from the sub-second samples.
3. "adopted reclaimed chardev fd" is logged per restart, counted from a pre-trigger timestamp.
4. The fd-store-off positive control still produces the sustained signature.
5. A deliberately hung bridge does not stall the chardev read; the reader keeps draining and the client is dropped.

## Self-review

- Spec coverage: invariant 1 is Tasks 2 and 3; invariant 2 is Task 1; acceptance is the Validation section.
- Placeholders: Task 2 Step 1 is a `t.Skip` the implementer must wire to the `openChardev` seam. It is flagged explicitly, not silent.
- Type consistency: `drainHub.toClient chan []byte`, the `setClient` flush, and the unchanged `drainChardev` signature are consistent across tasks.
