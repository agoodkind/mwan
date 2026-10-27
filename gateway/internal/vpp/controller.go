package vpp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sync"

	"github.com/google/renameio/v2"
	"github.com/google/uuid"
	"go.fd.io/govpp/api"
	interfaces "go.fd.io/govpp/binapi/interface"
	"go.fd.io/govpp/binapi/interface_types"
	"go.fd.io/govpp/binapi/nat64"
	"go.fd.io/govpp/binapi/tapv2"
)

// TAP combines a creation request with the connection identity used for ownership.
type TAP struct {
	ConnectionID string
	Create       tapv2.TapCreateV3
	RXMode       interface_types.RxMode
	Worker       uint32
}

// Settings supplies process resources without replacing WANCONFIG network policy.
type Settings struct {
	Endpoints Endpoints
	StateFile string
	TAPs      []TAP
	NAT64     *nat64.Nat64PluginEnableDisable
}

// Controller serializes mutations against the durable ownership journal.
type Controller struct {
	mu       sync.Mutex
	settings Settings
}

type ownedTAP struct {
	Name    string                         `json:"Name"`
	Tag     string                         `json:"Tag"`
	Index   interface_types.InterfaceIndex `json:"Index"`
	Request tapv2.TapCreateV3              `json:"Request"`
}

type journal struct {
	APISocket          string        `json:"APISocket"`
	Generation         string        `json:"Generation"`
	NAT64Enabled       bool          `json:"NAT64Enabled"`
	NAT64Owned         bool          `json:"NAT64Owned"`
	NAT64EnablePending bool          `json:"NAT64EnablePending"`
	TAPs               []ownedTAP    `json:"TAPs"`
	Objects            []ownedObject `json:"Objects"`
	Pending            []ownedObject `json:"Pending"`
}

// NewController requires configured sockets and an absolute durable ownership path.
func NewController(settings Settings) (*Controller, error) {
	if err := settings.Endpoints.Validate(); err != nil {
		return nil, err
	}
	if !filepath.IsAbs(settings.StateFile) {
		return nil, fmt.Errorf("VPP ownership state requires an absolute path")
	}
	return &Controller{mu: sync.Mutex{}, settings: settings}, nil
}

// Reconcile verifies ownership before applying one complete router policy.
func (controller *Controller) Reconcile(ctx context.Context, log *slog.Logger, policy Policy) (failure error) {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	defer func() {
		if failure != nil {
			log.ErrorContext(ctx, "reconcile VPP router policy failed", "error", failure)
		}
	}()
	if err := controller.Validate(policy); err != nil {
		return err
	}
	lock, err := controller.lockJournal(ctx)
	if err != nil {
		return err
	}
	defer func() {
		if err := lock.Close(); err != nil {
			log.ErrorContext(ctx, "close VPP ownership lock failed", "error", err)
		}
	}()
	state, err := controller.readJournal(ctx)
	if err != nil {
		return err
	}
	if err := controller.refreshGeneration(ctx, &state); err != nil {
		return err
	}
	client, err := Open(ctx, controller.settings.Endpoints, requiredMessages()...)
	if err != nil {
		return err
	}
	defer client.Close()
	requestContext, cancel := client.requestContext(ctx)
	defer cancel()
	observed, err := client.Interfaces(requestContext)
	if err != nil {
		return err
	}
	wantedTAPs := controller.desiredTAPs()
	if err := client.preflightHosts(requestContext, wantedTAPs, observed); err != nil {
		return err
	}
	if err := controller.resolvePending(requestContext, client, &state, observed); err != nil {
		return err
	}
	indices, err := controller.reconcileTAPs(requestContext, log, client, &state, observed, wantedTAPs, policy)
	if err != nil {
		return err
	}
	if err := controller.reconcileNAT64State(requestContext, client, &state, policy.NAT64); err != nil {
		return err
	}
	wantedObjects, err := marshalDesiredObjects(policy, indices)
	if err != nil {
		return err
	}
	return controller.reconcileObjects(requestContext, client, &state, observed, wantedObjects)
}

func ownershipTagPrefix(connectionID string) string {
	return "mwan-vpp:" + uuid.NewSHA1(uuid.NameSpaceOID, []byte(connectionID)).String() + ":"
}

func (controller *Controller) readJournal(ctx context.Context) (result journal, failure error) {
	defer func() {
		if failure != nil {
			slog.ErrorContext(ctx, "read VPP ownership journal failed", "error", failure)
		}
	}()
	state := journal{APISocket: controller.settings.Endpoints.APISocket, Generation: "", NAT64Enabled: false, NAT64Owned: false, NAT64EnablePending: false, TAPs: nil, Objects: nil, Pending: nil}
	data, err := os.ReadFile(controller.settings.StateFile)
	// The first reconcile creates the ownership journal.
	if errors.Is(err, os.ErrNotExist) {
		return state, nil
	}
	if err != nil {
		return state, fmt.Errorf("read VPP ownership journal: %w", err)
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return state, fmt.Errorf("decode VPP ownership journal: %w", err)
	}
	if state.APISocket != controller.settings.Endpoints.APISocket {
		return state, fmt.Errorf("VPP ownership journal belongs to API socket %s", state.APISocket)
	}
	for _, object := range append(slices.Clone(state.Objects), state.Pending...) {
		if err := object.validate(); err != nil {
			return state, fmt.Errorf("invalid VPP ownership journal: %w", err)
		}
	}
	return state, nil
}

func (controller *Controller) resolvePending(ctx context.Context, client *Client, state *journal, observed map[string]*interfaces.SwInterfaceDetails) (failure error) {
	defer func() {
		if failure != nil {
			slog.ErrorContext(ctx, "resolve VPP pending ownership failed", "error", failure)
		}
	}()
	for _, object := range state.Pending {
		if index, indexed := object.interfaceIndex(); indexed {
			entry, exists := observed[object.Interface]
			if !exists || entry.SwIfIndex != index || !slices.ContainsFunc(state.TAPs, func(tap ownedTAP) bool { return tap.Index == index && tap.Tag == entry.Tag }) {
				continue
			}
		}
		present, err := client.objectPresent(ctx, object)
		if err != nil {
			return fmt.Errorf("resolve pending VPP ownership %s: %w", object.key(), err)
		}
		if present {
			state.Objects = slices.DeleteFunc(state.Objects, func(value ownedObject) bool { return value.key() == object.key() })
			state.Objects = append(state.Objects, object)
		}
	}
	state.Pending = nil
	return controller.writeJournal(ctx, *state)
}

func (controller *Controller) writeJournal(ctx context.Context, state journal) (failure error) {
	defer func() {
		if failure != nil {
			slog.ErrorContext(ctx, "save VPP ownership journal failed", "error", failure)
		}
	}()
	data, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("encode VPP ownership journal: %w", err)
	}
	if err := renameio.WriteFile(controller.settings.StateFile, data, 0o600); err != nil {
		return fmt.Errorf("save VPP ownership journal: %w", err)
	}
	return nil
}

func (controller *Controller) removeObject(ctx context.Context, client *Client, state *journal, observed map[string]*interfaces.SwInterfaceDetails, object ownedObject) error {
	if object.Interface != "" {
		entry, present := observed[object.Interface]
		if present && !slices.ContainsFunc(state.TAPs, func(value ownedTAP) bool {
			return value.Name == object.Interface && value.Tag == entry.Tag && value.Index == entry.SwIfIndex
		}) {
			return fmt.Errorf("VPP interface %s changed identity before cleanup", object.Interface)
		}
		if !present {
			state.Objects = slices.DeleteFunc(state.Objects, func(value ownedObject) bool { return value.key() == object.key() })
			return controller.writeJournal(ctx, *state)
		}
		if index, indexed := object.interfaceIndex(); indexed && index != entry.SwIfIndex {
			state.Objects = slices.DeleteFunc(state.Objects, func(value ownedObject) bool { return value.key() == object.key() })
			return controller.writeJournal(ctx, *state)
		}
	}
	present, err := client.objectPresent(ctx, object)
	if err != nil {
		return err
	}
	if present {
		if err := client.applyObject(ctx, object, false); err != nil {
			return err
		}
	}
	state.Objects = slices.DeleteFunc(state.Objects, func(value ownedObject) bool { return value.key() == object.key() })
	return controller.writeJournal(ctx, *state)
}

func (controller *Controller) removeTAP(ctx context.Context, client *Client, state *journal, observed map[string]*interfaces.SwInterfaceDetails, tap ownedTAP) (failure error) {
	defer func() {
		if failure != nil {
			slog.ErrorContext(ctx, "remove owned VPP TAP failed", "interface", tap.Name, "error", failure)
		}
	}()
	for _, object := range slices.Clone(state.Objects) {
		if object.Interface == tap.Name {
			if err := controller.removeObject(ctx, client, state, observed, object); err != nil {
				return err
			}
		}
	}
	if entry, present := observed[tap.Name]; present {
		if entry.Tag != tap.Tag || entry.SwIfIndex != tap.Index {
			return fmt.Errorf("VPP TAP %s changed identity before removal", tap.Name)
		}
		if _, err := tapv2.NewServiceClient(client.connection).TapDeleteV2(ctx, &tapv2.TapDeleteV2{SwIfIndex: tap.Index}); err != nil {
			return fmt.Errorf("delete owned VPP TAP %s: %w", tap.Name, err)
		}
		delete(observed, tap.Name)
	}
	state.TAPs = slices.DeleteFunc(state.TAPs, func(value ownedTAP) bool { return value.Name == tap.Name })
	return controller.writeJournal(ctx, *state)
}

// Interfaces reads VPP names, indexes, and owner tags for mutation checks.
func (client *Client) Interfaces(ctx context.Context) (result map[string]*interfaces.SwInterfaceDetails, failure error) {
	defer func() {
		if failure != nil {
			slog.ErrorContext(ctx, "read VPP interface identities failed", "error", failure)
		}
	}()
	stream, err := interfaces.NewServiceClient(client.connection).SwInterfaceDump(ctx, &interfaces.SwInterfaceDump{SwIfIndex: ^interface_types.InterfaceIndex(0)})
	entries, err := collect(stream, err)
	if err != nil {
		return nil, fmt.Errorf("read VPP interfaces: %w", err)
	}
	result = make(map[string]*interfaces.SwInterfaceDetails, len(entries))
	for _, entry := range entries {
		result[entry.InterfaceName] = entry
	}
	return result, nil
}

type dumpStream[T api.Message] interface {
	Recv() (T, error)
	Close() error
}

func collect[T api.Message](stream dumpStream[T], failure error) ([]T, error) {
	if failure != nil {
		return nil, failure
	}
	defer func() { _ = stream.Close() }()
	var entries []T
	for {
		entry, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return entries, nil
		}
		if err != nil {
			slog.Error("receive VPP API dump failed", "error", err)
			return nil, fmt.Errorf("receive VPP API dump: %w", err)
		}
		entries = append(entries, entry)
	}
}
