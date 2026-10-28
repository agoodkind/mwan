package vpp

import (
	"context"
	"fmt"
	"slices"

	interfaces "go.fd.io/govpp/binapi/interface"
)

func replaceOwnedObject(state *journal, object ownedObject) {
	state.Objects = slices.DeleteFunc(state.Objects, func(value ownedObject) bool { return value.key() == object.key() })
	state.Objects = append(state.Objects, object)
}

func (controller *Controller) reconcileObjects(ctx context.Context, client *Client, state *journal, observed map[string]*interfaces.SwInterfaceDetails, wanted []ownedObject) error {
	for _, previous := range slices.Clone(state.Objects) {
		if slices.ContainsFunc(wanted, func(value ownedObject) bool { return value.key() == previous.key() }) {
			continue
		}
		if err := controller.removeObject(ctx, client, state, observed, previous); err != nil {
			return err
		}
	}
	for _, object := range wanted {
		if err := controller.ensureObject(ctx, client, state, object); err != nil {
			return err
		}
	}
	return controller.writeJournal(ctx, *state)
}

func (controller *Controller) ensureObject(ctx context.Context, client *Client, state *journal, object ownedObject) error {
	present, err := client.objectPresent(ctx, object)
	if err != nil {
		return err
	}
	if present {
		if slices.ContainsFunc(state.Objects, func(value ownedObject) bool { return value.key() == object.key() }) {
			replaceOwnedObject(state, object)
		}
		return nil
	}
	state.Pending = append(state.Pending, object)
	if err := controller.writeJournal(ctx, *state); err != nil {
		return err
	}
	if err := client.applyObject(ctx, object, true); err != nil {
		return err
	}
	state.Pending = slices.DeleteFunc(state.Pending, func(value ownedObject) bool { return value.key() == object.key() })
	replaceOwnedObject(state, object)
	if err := controller.writeJournal(ctx, *state); err != nil {
		return err
	}
	present, err = client.objectPresent(ctx, object)
	if err != nil {
		return err
	}
	if !present {
		return fmt.Errorf("VPP did not retain %s", object.key())
	}
	return nil
}
