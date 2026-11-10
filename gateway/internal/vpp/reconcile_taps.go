package vpp

import (
	"context"
	"fmt"
	"log/slog"
	"reflect"
	"slices"
	"strings"

	"github.com/google/uuid"
	interfaces "go.fd.io/govpp/binapi/interface"
	"go.fd.io/govpp/binapi/interface_types"
	"go.fd.io/govpp/binapi/tapv2"
)

func (controller *Controller) desiredTAPs() map[string]TAP {
	wanted := make(map[string]TAP, len(controller.settings.TAPs))
	for _, resource := range controller.settings.TAPs {
		wanted[fmt.Sprintf("tap%d", resource.Create.ID)] = resource
	}
	return wanted
}

func (controller *Controller) reconcileTAPs(ctx context.Context, log *slog.Logger, client *Client, state *journal, observed map[string]*interfaces.SwInterfaceDetails, wanted map[string]TAP, policy Policy) (map[string]interface_types.InterfaceIndex, error) {
	if err := controller.removeObsoleteTAPs(ctx, client, state, observed, wanted); err != nil {
		return nil, err
	}
	indices := make(map[string]interface_types.InterfaceIndex, len(wanted))
	for name, resource := range wanted {
		entry, err := client.ensureTAP(ctx, log, name, resource, observed)
		if err != nil {
			return nil, err
		}
		indices[name] = entry.SwIfIndex
		resource.Create.Tag = entry.Tag
		record := ownedTAP{Name: name, Tag: entry.Tag, Index: entry.SwIfIndex, Request: resource.Create}
		state.TAPs = slices.DeleteFunc(state.TAPs, func(value ownedTAP) bool { return value.Name == name })
		state.TAPs = append(state.TAPs, record)
		if err := controller.writeJournal(ctx, *state); err != nil {
			return nil, err
		}
		if err := client.configureTAP(ctx, resource, entry.SwIfIndex, policy); err != nil {
			return nil, err
		}
	}
	return indices, nil
}

func (controller *Controller) removeObsoleteTAPs(ctx context.Context, client *Client, state *journal, observed map[string]*interfaces.SwInterfaceDetails, wanted map[string]TAP) error {
	for _, previous := range slices.Clone(state.TAPs) {
		desired, keep := wanted[previous.Name]
		previousRequest := previous.Request
		previousRequest.Tag = ""
		if keep && reflect.DeepEqual(previousRequest, desired.Create) {
			continue
		}
		if err := controller.removeTAP(ctx, client, state, observed, previous); err != nil {
			return err
		}
	}
	return nil
}

func (client *Client) ensureTAP(ctx context.Context, log *slog.Logger, name string, resource TAP, observed map[string]*interfaces.SwInterfaceDetails) (result *interfaces.SwInterfaceDetails, failure error) {
	defer func() {
		if failure != nil {
			log.ErrorContext(ctx, "create VPP TAP failed", "interface", name, "error", failure)
		}
	}()
	tagPrefix := ownershipTagPrefix(resource.ConnectionID)
	entry, found := observed[name]
	if found {
		if !strings.HasPrefix(entry.Tag, tagPrefix) {
			return nil, fmt.Errorf("VPP interface %s belongs to tag %q; expected prefix %q", name, entry.Tag, tagPrefix)
		}
		return entry, nil
	}
	resource.Create.Tag = tagPrefix + uuid.NewString()[:8]
	reply, err := tapv2.NewServiceClient(client.connection).TapCreateV3(ctx, &resource.Create)
	if err != nil {
		return nil, fmt.Errorf("create VPP TAP %s: %w", name, err)
	}
	entry = &interfaces.SwInterfaceDetails{InterfaceName: name, Tag: resource.Create.Tag, SwIfIndex: reply.SwIfIndex}
	observed[name] = entry
	log.InfoContext(ctx, "VPP TAP created", "interface", name, "index", entry.SwIfIndex)
	return entry, nil
}

func (controller *Controller) reconcileNAT64State(ctx context.Context, client *Client, state *journal, policy *NAT64Policy) error {
	if policy == nil || state.NAT64Enabled {
		return nil
	}
	if controller.settings.NAT64 == nil {
		return fmt.Errorf("NAT64 policy requires configured VPP NAT64 table resources")
	}
	state.NAT64EnablePending = true
	if err := controller.writeJournal(ctx, *state); err != nil {
		return err
	}
	created, err := client.ensureNAT64(ctx, *controller.settings.NAT64)
	if err != nil {
		return err
	}
	state.NAT64Enabled = true
	state.NAT64Owned = created
	state.NAT64EnablePending = false
	return controller.writeJournal(ctx, *state)
}
