package interfaceintent

import (
	"fmt"
	"log/slog"
	"net"
	"strconv"
)

// PCIAddress identifies a PCI function without encoding its physical topology.
type PCIAddress struct {
	Domain   uint16
	Bus      uint8
	Device   uint8
	Function uint8
}

// ParsePCIAddress requires the canonical lowercase dddd:bb:dd.f form.
func ParsePCIAddress(text string) (PCIAddress, error) {
	var address PCIAddress
	if len(text) != 12 || text[4] != ':' || text[7] != ':' || text[10] != '.' {
		return address, fmt.Errorf("PCI address %q requires dddd:bb:dd.f", text)
	}
	domain, err := strconv.ParseUint(text[:4], 16, 16)
	if err != nil {
		slog.Warn("native PCI domain parse failed", "address", text, "err", err)
		return address, fmt.Errorf("PCI address %q: %w", text, err)
	}
	address.Domain = uint16(domain)
	parts := []string{text[5:7], text[8:10], text[11:]}
	values := make([]uint8, len(parts))
	for i, part := range parts {
		value, err := strconv.ParseUint(part, 16, 8)
		if err != nil {
			slog.Warn("native PCI address parse failed", "address", text, "err", err)
			return address, fmt.Errorf("PCI address %q: %w", text, err)
		}
		values[i] = uint8(value)
	}
	address.Bus, address.Device, address.Function = values[0], values[1], values[2]
	if address.Device > 31 || address.Function > 7 || address.String() != text {
		return PCIAddress{}, fmt.Errorf("PCI address %q requires canonical dddd:bb:dd.f with device <= 31 and function <= 7", text)
	}
	return address, nil
}

// String returns the canonical PCI function identifier.
func (address PCIAddress) String() string {
	return fmt.Sprintf("%04x:%02x:%02x.%x", address.Domain, address.Bus, address.Device, address.Function)
}

// NativeOffloadBinding associates a kernel connection with a separately owned DATA device.
type NativeOffloadBinding struct {
	DataPCI    PCIAddress
	DataMAC    [6]byte
	Controller *NativeOffloadController
}

// NativeOffloadController selects receiver and DATA representors under one controller.
type NativeOffloadController struct {
	PCI          PCIAddress
	ReceiverVF   uint16
	DataVF       uint16
	PhysicalMiss bool
}

// Clone isolates the optional controller record from configuration consumers.
func (binding *NativeOffloadBinding) Clone() *NativeOffloadBinding {
	if binding == nil {
		return nil
	}
	cloned := *binding
	if binding.Controller != nil {
		controller := *binding.Controller
		cloned.Controller = &controller
	}
	return &cloned
}

// CloneNativeBindings copies connection and link records before a consumer owns bindings.
func CloneNativeBindings(connections []Connection) []Connection {
	cloned := append([]Connection(nil), connections...)
	for i := range cloned {
		if cloned[i].Link != nil {
			link := *cloned[i].Link
			link.NativeOffload = link.NativeOffload.Clone()
			link.Tunnel = link.Tunnel.Clone()
			cloned[i].Link = &link
		}
	}
	return cloned
}

// ValidateNativeBindings rejects graphs that cannot identify one physical ingress.
func ValidateNativeBindings(connections []Connection) error {
	type vfIdentity struct {
		controller PCIAddress
		vf         uint16
	}
	data := make(map[PCIAddress]string)
	controllers := make(map[PCIAddress]uint32)
	roles := make(map[vfIdentity]string)
	for _, connection := range connections {
		if connection.Link == nil || connection.Link.NativeOffload == nil {
			continue
		}
		binding := connection.Link.NativeOffload
		if err := validateNativeBinding(connection); err != nil {
			return err
		}
		if previous, found := data[binding.DataPCI]; found {
			return fmt.Errorf("native DATA PCI %s is shared by %s and %s", binding.DataPCI, previous, connection.Name)
		}
		data[binding.DataPCI] = connection.Name
		controller := binding.Controller
		if controller == nil {
			continue
		}
		if _, found := controllers[controller.PCI]; !found {
			controllers[controller.PCI] = 0
		}
		if controller.PhysicalMiss {
			controllers[controller.PCI]++
		}
		for _, vf := range []uint16{controller.ReceiverVF, controller.DataVF} {
			identity := vfIdentity{controller: controller.PCI, vf: vf}
			if previous, found := roles[identity]; found {
				return fmt.Errorf("native controller %s VF %d overlaps roles for %s and %s", controller.PCI, vf, previous, connection.Name)
			}
			roles[identity] = connection.Name
		}
	}
	if len(data) == 0 {
		return nil
	}
	if len(controllers) != 1 {
		return fmt.Errorf("native bindings require exactly one physical controller")
	}
	for controller, misses := range controllers {
		if _, found := data[controller]; found {
			return fmt.Errorf("native controller PCI %s also identifies a DATA device", controller)
		}
		if misses != 1 {
			return fmt.Errorf("native controller %s requires exactly one physical-miss receiver", controller)
		}
	}
	return validateNativeMACRoles(connections)
}

func validateNativeBinding(connection Connection) error {
	link := connection.Link
	binding := link.NativeOffload
	if link.Kind != KindPhysical || link.VLAN != nil || link.Tunnel != nil || link.BridgeMaster != "" {
		return fmt.Errorf("interface %s native offload requires an untagged physical link outside a bridge", connection.Name)
	}
	if _, err := ParsePCIAddress(binding.DataPCI.String()); err != nil {
		slog.Warn("native DATA PCI invalid", "interface", connection.Name, "err", err)
		return fmt.Errorf("interface %s DATA PCI: %w", connection.Name, err)
	}
	if binding.DataMAC == [6]byte{} || binding.DataMAC[0]&1 != 0 {
		return fmt.Errorf("interface %s native DATA MAC must be a nonzero unicast address", connection.Name)
	}
	if binding.Controller == nil {
		return nil
	}
	if _, err := ParsePCIAddress(binding.Controller.PCI.String()); err != nil {
		slog.Warn("native controller PCI invalid", "interface", connection.Name, "err", err)
		return fmt.Errorf("interface %s controller PCI: %w", connection.Name, err)
	}
	if binding.Controller.ReceiverVF == binding.Controller.DataVF {
		return fmt.Errorf("interface %s native receiver and DATA VF overlap", connection.Name)
	}
	return nil
}

func validateNativeMACRoles(connections []Connection) error {
	type identity struct {
		controller PCIAddress
		mac        [6]byte
	}
	seen := make(map[identity]string)
	for _, connection := range connections {
		if connection.Link == nil || connection.Link.NativeOffload == nil || connection.Link.NativeOffload.Controller == nil {
			continue
		}
		binding := connection.Link.NativeOffload
		receiver, err := nativeReceiverMAC(connection)
		if err != nil {
			return err
		}
		for _, address := range [][6]byte{binding.DataMAC, receiver} {
			if address == [6]byte{} {
				continue
			}
			key := identity{controller: binding.Controller.PCI, mac: address}
			if previous, found := seen[key]; found {
				return fmt.Errorf("native controller %s has overlapping MAC roles on %s and %s", key.controller, previous, connection.Name)
			}
			seen[key] = connection.Name
		}
	}
	return nil
}

func nativeReceiverMAC(connection Connection) ([6]byte, error) {
	link := connection.Link
	mac := link.HardwareAddress
	if mac == "" {
		mac = link.Match.HardwareAddress
	}
	if mac == "" {
		return [6]byte{}, nil
	}
	receiver, err := net.ParseMAC(mac)
	if err != nil {
		slog.Warn("native receiver MAC invalid", "interface", connection.Name, "err", err)
		return [6]byte{}, fmt.Errorf("interface %s receiver MAC: %w", connection.Name, err)
	}
	if len(receiver) != 6 || receiver[0]&1 != 0 || [6]byte(receiver) == [6]byte{} {
		return [6]byte{}, fmt.Errorf("interface %s receiver MAC requires a nonzero six-byte unicast address", connection.Name)
	}
	return [6]byte(receiver), nil
}
