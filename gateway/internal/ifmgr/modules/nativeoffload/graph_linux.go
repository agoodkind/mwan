//go:build linux && cgo && nativeoffload

package nativeoffload

import (
	"fmt"
	"math"
	"net"

	"github.com/agoodkind/netlink"
	"golang.org/x/sys/unix"

	"goodkind.io/mwan/internal/interfaceintent"
	"goodkind.io/mwan/internal/netif"
	"goodkind.io/mwan/internal/offload"
)

const (
	nativeRXDescriptors = 512
	nativeTXDescriptors = 512
	nativePollBudget    = 256
)

type boundLink struct {
	connection interfaceintent.Connection
	kernel     net.Interface
	identity   string
	data       offload.NativeDataID
	origin     offload.NativeOriginID
	hasOrigin  bool
}

func (module *Module) resolveGraph() (offload.NativeConfig, map[int]boundLink, error) {
	settings := module.cfg.Settings
	configuration := offload.NativeConfig{
		CPU: settings.CPU, MemoryMiB: settings.MemoryMiB,
		MemoryMode: offload.NativeHugepages, PairCapacity: settings.PairCapacity,
		MbufCount: settings.MbufCount, DrainTimeout: settings.ShutdownTimeout,
		ConntrackUpdateTimeout: settings.RequestTimeout,
		RXDescriptors:          nativeRXDescriptors, TXDescriptors: nativeTXDescriptors,
		HugepageDirectory: settings.HugepageDirectory, FilePrefix: settings.FilePrefix,
		PollBudget: nativePollBudget,
		IdleWait:   settings.IdleWait,
	}
	connections := interfaceintent.CloneNativeBindings(module.Env.Connections)
	if err := interfaceintent.ValidateNativeBindings(connections); err != nil {
		return configuration, nil, err
	}
	links := make(map[int]boundLink)
	controllers := make(map[interfaceintent.PCIAddress]offload.NativeControllerID)
	for _, connection := range connections {
		if connection.Enabled != nil && !*connection.Enabled || connection.Link == nil || connection.Link.NativeOffload == nil {
			continue
		}
		binding := connection.Link.NativeOffload
		link, err := netlink.LinkByName(connection.Name)
		if err != nil {
			return configuration, nil, fmt.Errorf("inspect native kernel receiver %s: %w", connection.Name, err)
		}
		kernel, err := net.InterfaceByIndex(link.Attrs().Index)
		if err != nil {
			return configuration, nil, fmt.Errorf("read native receiver identity: %w", err)
		}
		if kernel.Flags&net.FlagUp == 0 || link.Attrs().RawFlags&unix.IFF_LOWER_UP == 0 || len(kernel.HardwareAddr) != 6 || kernel.HardwareAddr[0]&1 != 0 || [6]byte(kernel.HardwareAddr) == [6]byte{} || kernel.MTU <= 0 || kernel.MTU > math.MaxUint16 {
			return configuration, nil, fmt.Errorf("native receiver %s requires an active Ethernet link with a valid MTU", connection.Name)
		}
		expectedMAC := connection.Link.HardwareAddress
		if expectedMAC == "" {
			expectedMAC = connection.Link.Match.HardwareAddress
		}
		if expectedMAC != "" && kernel.HardwareAddr.String() != expectedMAC {
			return configuration, nil, fmt.Errorf("native receiver %s does not match its configured MAC", connection.Name)
		}
		dataID := offload.NativeDataID(len(configuration.Data))
		data := offload.NativeDataDevice{PCI: binding.DataPCI, MAC: binding.DataMAC, Controller: offload.NativeNoController}
		bound := boundLink{connection: connection, kernel: *kernel, identity: netif.LinkOwnershipIdentity(link), data: dataID}
		if binding.Controller != nil {
			controller := binding.Controller
			controllerID, exists := controllers[controller.PCI]
			if !exists {
				controllerID = offload.NativeControllerID(len(configuration.Controllers))
				controllers[controller.PCI] = controllerID
				configuration.Controllers = append(configuration.Controllers, offload.NativeController{PCI: controller.PCI})
			}
			data.Controller, data.VF = controllerID, controller.DataVF
			receiverID := offload.NativeReceiverID(len(configuration.Receivers))
			configuration.Receivers = append(configuration.Receivers, offload.NativeKernelReceiver{Controller: controllerID, VF: controller.ReceiverVF, MAC: [6]byte(kernel.HardwareAddr)})
			if controller.PhysicalMiss {
				configuration.Controllers[controllerID].PhysicalReceiver = receiverID
				bound.origin = offload.NativeOriginID(len(configuration.Origins))
				bound.hasOrigin = true
				configuration.Origins = append(configuration.Origins, offload.NativeOrigin{Controller: controllerID, Kind: offload.NativePhysicalOrigin, Receiver: receiverID, Data: dataID, ReturnMode: offload.NativeReturnOriginal})
			}
		}
		configuration.Data = append(configuration.Data, data)
		configuration.MTU = max(configuration.MTU, uint16(kernel.MTU))
		links[kernel.Index] = bound
	}
	if len(configuration.Origins) == 0 {
		return configuration, nil, fmt.Errorf("native automatic admission requires a physical-miss ingress binding")
	}
	return configuration, links, nil
}

func (module *Module) verifyLinks() error {
	for index, binding := range module.links {
		link, err := netlink.LinkByName(binding.kernel.Name)
		if err != nil {
			return fmt.Errorf("inspect native receiver %s: %w", binding.kernel.Name, err)
		}
		attributes := link.Attrs()
		if !netif.LinkMatchesIdentity(link, index, binding.identity) || attributes.HardwareAddr.String() != binding.kernel.HardwareAddr.String() || attributes.MTU != binding.kernel.MTU || attributes.Flags&net.FlagUp == 0 || attributes.RawFlags&unix.IFF_LOWER_UP == 0 {
			return fmt.Errorf("native receiver %s changed its kernel identity or forwarding attributes", binding.kernel.Name)
		}
	}
	return nil
}
