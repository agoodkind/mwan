package wanconfig

import (
	"net"

	"goodkind.io/mwan/internal/interfaceintent"
)

func nativeOffloadItems(path string, binding *interfaceintent.NativeOffloadBinding) []Item {
	if binding == nil {
		return nil
	}
	items := []Item{
		{Path: path + "/data-pci", Value: binding.DataPCI.String()},
		{Path: path + "/data-mac", Value: net.HardwareAddr(binding.DataMAC[:]).String()},
	}
	if controller := binding.Controller; controller != nil {
		items = append(items,
			Item{Path: path + "/controller/pci", Value: controller.PCI.String()},
			Item{Path: path + "/controller/receiver-vf", Value: uintValue(uint64(controller.ReceiverVF))},
			Item{Path: path + "/controller/data-vf", Value: uintValue(uint64(controller.DataVF))},
			Item{Path: path + "/controller/physical-miss", Value: boolValue(controller.PhysicalMiss)},
		)
	}
	return items
}
