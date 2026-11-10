package networkjson

import (
	"fmt"
	"log/slog"
	"net"

	"goodkind.io/mwan/internal/interfaceintent"
)

type nativeOffloadWire struct {
	DataPCI    string                `json:"data-pci"`
	DataMAC    string                `json:"data-mac"`
	Controller *nativeControllerWire `json:"controller"`
}

type nativeControllerWire struct {
	PCI          string  `json:"pci"`
	ReceiverVF   *uint16 `json:"receiver-vf"`
	DataVF       *uint16 `json:"data-vf"`
	PhysicalMiss *bool   `json:"physical-miss"`
}

func buildNativeOffloadBinding(wire *nativeOffloadWire) (*interfaceintent.NativeOffloadBinding, error) {
	if wire == nil {
		return nil, nil
	}
	pci, err := interfaceintent.ParsePCIAddress(wire.DataPCI)
	if err != nil {
		slog.Warn("networkjson: native DATA PCI parse failed", "err", err)
		return nil, fmt.Errorf("native-offload/data-pci: %w", err)
	}
	mac, err := net.ParseMAC(wire.DataMAC)
	if err != nil {
		slog.Warn("networkjson: native DATA MAC parse failed", "err", err)
		return nil, fmt.Errorf("native-offload/data-mac: %w", err)
	}
	if len(mac) != 6 || mac[0]&1 != 0 || [6]byte(mac) == [6]byte{} {
		return nil, fmt.Errorf("native-offload/data-mac requires a nonzero six-byte unicast address")
	}
	binding := &interfaceintent.NativeOffloadBinding{DataPCI: pci, DataMAC: [6]byte(mac), Controller: nil}
	if wire.Controller != nil {
		controller := wire.Controller
		if controller.ReceiverVF == nil || controller.DataVF == nil || controller.PhysicalMiss == nil {
			return nil, fmt.Errorf("native-offload/controller requires receiver-vf, data-vf and physical-miss")
		}
		pci, err := interfaceintent.ParsePCIAddress(controller.PCI)
		if err != nil {
			slog.Warn("networkjson: native controller PCI parse failed", "err", err)
			return nil, fmt.Errorf("native-offload/controller/pci: %w", err)
		}
		binding.Controller = &interfaceintent.NativeOffloadController{
			PCI: pci, ReceiverVF: *controller.ReceiverVF, DataVF: *controller.DataVF, PhysicalMiss: *controller.PhysicalMiss,
		}
	}
	return binding, nil
}
