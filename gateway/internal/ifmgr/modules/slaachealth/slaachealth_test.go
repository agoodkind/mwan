package slaachealth_test

import (
	"context"
	"io"
	"log/slog"
	"slices"
	"testing"
	"time"

	"goodkind.io/mwan/internal/ifmgr"
	"goodkind.io/mwan/internal/ifmgr/modules/slaachealth"
	"goodkind.io/mwan/internal/notify"
)

//testdouble:external the host kernel controls /proc/sys and a test cannot safely write to it
type recordingSysctl struct {
	values []string
	cancel context.CancelFunc
}

func (r *recordingSysctl) Get(context.Context, string) (string, error) { return "", nil }

func (r *recordingSysctl) Set(ctx context.Context, _, value string) error {
	defer r.cancel()
	if err := ctx.Err(); err != nil {
		return err
	}
	r.values = append(r.values, value)
	return nil
}

func (r *recordingSysctl) DryRun() bool { return false }

func TestReconcileRestoresIPv6AfterCancellation(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sysctl := &recordingSysctl{cancel: cancel}
	module, err := slaachealth.New(slaachealth.Config{
		Iface:             "mwan-absent0",
		AlertAfter:        time.Hour,
		MaxTogglesPerHour: 1,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	env := &ifmgr.Env{
		Log:    log,
		Sysctl: sysctl,
		Alerts: ifmgr.WrapNotifier(notify.FromConfig(notify.EmailConfig{}, notify.Config{}, log, "mwan-ifmgr")),
	}
	if err := module.Init(context.Background(), env); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := module.Reconcile(ctx, log); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if want := []string{"1", "0"}; !slices.Equal(sysctl.values, want) {
		t.Fatalf("Set values = %v, want %v", sysctl.values, want)
	}
}
