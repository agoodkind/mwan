//go:build linux && cgo && nativeoffload

package nativeoffload

import (
	"fmt"
	"log/slog"
	"math"
	"net"

	"github.com/vishvananda/netlink"
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

func (module *Module) resolveGraph() (configuration offload.NativeConfig, links map[int]boundLink, resultErr error) {
	defer func() {
		if resultErr != nil {
			slog.Warn("native hardware graph resolution failed", "err", resultErr)
		}
	}()
	settings := module.cfg.Settings
	configuration = offload.NativeConfig{
		CPU: settings.CPU, MemoryMiB: settings.MemoryMiB,
		MemoryMode: offload.NativeHugepages, PairCapacity: settings.PairCapacity,
		MbufCount: settings.MbufCount, DrainTimeout: settings.ShutdownTimeout,
		ConntrackUpdateTimeout: settings.RequestTimeout,
		RXDescriptors:          nativeRXDescriptors, TXDescriptors: nativeTXDescriptors,
		HugepageDirectory: settings.HugepageDirectory, FilePrefix: settings.FilePrefix,
		PollBudget:  nativePollBudget,
		IdleWait:    settings.IdleWait,
		Controllers: nil, Receivers: nil, Data: nil, Origins: nil, MTU: 0,
	}
	connections := interfaceintent.CloneNativeBindings(module.Env.Connections)
	if err := interfaceintent.ValidateNativeBindings(connections); err != nil {
		return configuration, nil, fmt.Errorf("validate native hardware bindings: %w", err)
	}
	links = make(map[int]boundLink)
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
		if kernel.Flags&net.FlagUp == 0 || link.Attrs().RawFlags&unix.IFF_LOWER_UP == 0 || len(kernel.HardwareAddr) != 6 || kernel.HardwareAddr[0]&1 != 0 || [6]byte(kernel.HardwareAddr) == [6]byte{} {
			return configuration, nil, fmt.Errorf("native receiver %s requires an active Ethernet link with a valid MTU", connection.Name)
		}
		mtu, err := checkedNativeMTU(kernel.MTU)
		if err != nil {
			return configuration, nil, fmt.Errorf("check native receiver %s MTU: %w", connection.Name, err)
		}
		expectedMAC := connection.Link.HardwareAddress
		if expectedMAC == "" {
			expectedMAC = connection.Link.Match.HardwareAddress
		}
		if expectedMAC != "" && kernel.HardwareAddr.String() != expectedMAC {
			return configuration, nil, fmt.Errorf("native receiver %s does not match its configured MAC", connection.Name)
		}
		dataIndex, err := checkedNativeIndex(len(configuration.Data))
		if err != nil {
			return configuration, nil, fmt.Errorf("select native DATA index: %w", err)
		}
		dataID := offload.NativeDataID(dataIndex)
		data := offload.NativeDataDevice{PCI: binding.DataPCI, MAC: binding.DataMAC, Controller: offload.NativeNoController, VF: 0}
		bound := boundLink{connection: connection, kernel: *kernel, identity: netif.LinkOwnershipIdentity(link), data: dataID, origin: 0, hasOrigin: false}
		if err := appendNativeController(&configuration, controllers, binding, kernel, &bound, &data); err != nil {
			return configuration, nil, err
		}
		configuration.Data = append(configuration.Data, data)
		configuration.MTU = max(configuration.MTU, mtu)
		links[kernel.Index] = bound
	}
	if len(configuration.Origins) == 0 {
		return configuration, nil, fmt.Errorf("native automatic admission requires a physical-miss ingress binding")
	}
	return configuration, links, nil
}

func (module *Module) verifyLinks() (resultErr error) {
	defer func() {
		if resultErr != nil {
			slog.Warn("native receiver identity verification failed", "err", resultErr)
		}
	}()
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

func appendNativeController(configuration *offload.NativeConfig, controllers map[interfaceintent.PCIAddress]offload.NativeControllerID, binding *interfaceintent.NativeOffloadBinding, kernel *net.Interface, bound *boundLink, data *offload.NativeDataDevice) (resultErr error) {
	if binding.Controller == nil {
		return nil
	}
	defer func() {
		if resultErr != nil {
			slog.Warn("native controller binding failed", "controller", binding.Controller.PCI, "err", resultErr)
		}
	}()
	controller := binding.Controller
	controllerID, exists := controllers[controller.PCI]
	if !exists {
		controllerIndex, err := checkedNativeIndex(len(configuration.Controllers))
		if err != nil {
			return fmt.Errorf("select native controller index: %w", err)
		}
		controllerID = offload.NativeControllerID(controllerIndex)
		controllers[controller.PCI] = controllerID
		configuration.Controllers = append(configuration.Controllers, offload.NativeController{PCI: controller.PCI, PhysicalReceiver: 0})
	}
	data.Controller, data.VF = controllerID, controller.DataVF
	receiverIndex, err := checkedNativeIndex(len(configuration.Receivers))
	if err != nil {
		return fmt.Errorf("select native receiver index: %w", err)
	}
	receiverID := offload.NativeReceiverID(receiverIndex)
	configuration.Receivers = append(configuration.Receivers, offload.NativeKernelReceiver{Controller: controllerID, VF: controller.ReceiverVF, MAC: [6]byte(kernel.HardwareAddr)})
	if controller.PhysicalMiss {
		configuration.Controllers[controllerID].PhysicalReceiver = receiverID
		originIndex, err := checkedNativeIndex(len(configuration.Origins))
		if err != nil {
			return fmt.Errorf("select native origin index: %w", err)
		}
		bound.origin = offload.NativeOriginID(originIndex)
		bound.hasOrigin = true
		configuration.Origins = append(configuration.Origins, offload.NativeOrigin{Controller: controllerID, Kind: offload.NativePhysicalOrigin, Receiver: receiverID, Data: bound.data, ReturnMode: offload.NativeReturnOriginal, SourceVF: 0})
	}

	return nil
}

func checkedNativeIndex(index int) (uint32, error) {
	if index < 0 || index > math.MaxUint32 {
		return 0, fmt.Errorf("native object index %d exceeds the ABI range", index)
	}
	return uint32(index), nil
}

func checkedNativeMTU(mtu int) (uint16, error) {
	if mtu <= 0 || mtu > math.MaxUint16 {
		return 0, fmt.Errorf("native MTU %d exceeds the ABI range", mtu)
	}
	return uint16(mtu), nil
}
