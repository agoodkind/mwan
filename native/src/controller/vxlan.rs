use super::*;
use crate::vxlan::{InvalidPlan, Plan};
use std::path::PathBuf;

#[derive(Default)]
pub(super) struct Context {
    generation: u64,
    endpoint: Option<MwanNativeVxlanEndpoint>,
    interface: Option<PathBuf>,
    plan: Option<Plan>,
    rule_generation: u64,
    flow: Option<NonNull<dpdk::rte_flow>>,
    cleanup_failed: bool,
}

impl Context {
    pub(super) fn owns_resources(&self) -> bool {
        self.endpoint.is_some() || self.flow.is_some() || self.cleanup_failed
    }
}

fn vxlan_fault(code: i32, operation: u32, receiver: u32, message: &str) -> Box<MwanNativeError> {
    fault(
        code,
        operation,
        MWAN_NATIVE_OBJECT_RECEIVER,
        receiver,
        0,
        message,
    )
}

impl OwnerState {
    fn vxlan_result(&self, slot: usize) -> MwanNativeVxlanResult {
        let context = &self.vxlan[slot];
        let state = if context.cleanup_failed {
            MWAN_NATIVE_VXLAN_CLEANUP_FAILED
        } else if context.flow.is_some() {
            MWAN_NATIVE_VXLAN_RULE_INSTALLED
        } else if context.endpoint.is_some() {
            MWAN_NATIVE_VXLAN_CONTEXT_READY
        } else {
            MWAN_NATIVE_VXLAN_CONTEXT_ABSENT
        };
        MwanNativeVxlanResult {
            context: MwanNativeHandle {
                instance: self.instance,
                generation: context.generation,
                slot: slot as u32,
                reserved: 0,
            },
            rule_generation: context.rule_generation,
            state,
            reserved: 0,
        }
    }

    fn vxlan_slot(&self, handle: MwanNativeHandle) -> NativeResult<usize> {
        let slot = handle.slot as usize;
        if handle.reserved != 0
            || handle.instance != self.instance
            || handle.generation == 0
            || slot >= self.config.receiver_count as usize
            || self.vxlan[slot].generation != handle.generation
            || self.vxlan[slot].endpoint.is_none()
        {
            return Err(vxlan_fault(
                libc::ESTALE,
                MWAN_NATIVE_OP_CONFIG,
                handle.slot,
                "Stale VXLAN context handle",
            ));
        }
        Ok(slot)
    }

    fn vxlan_rule_slot(&self, rule: MwanNativeVxlanResult) -> NativeResult<usize> {
        let slot = self.vxlan_slot(rule.context)?;
        if rule.reserved != 0
            || rule.rule_generation == 0
            || self.vxlan[slot].rule_generation != rule.rule_generation
            || self.vxlan[slot].plan.is_none()
        {
            return Err(vxlan_fault(
                libc::ESTALE,
                MWAN_NATIVE_OP_CONFIG,
                slot as u32,
                "Stale VXLAN rule generation",
            ));
        }
        Ok(slot)
    }

    fn open_vxlan_context(
        &mut self,
        endpoint: MwanNativeVxlanEndpoint,
    ) -> NativeResult<MwanNativeVxlanResult> {
        let slot = endpoint.receiver_id as usize;
        if !self.active || self.faulted || self.quiescing {
            return Err(vxlan_fault(
                libc::EBUSY,
                MWAN_NATIVE_OP_CONFIG,
                endpoint.receiver_id,
                "VXLAN requires an active healthy owner",
            ));
        }
        if endpoint.version != MWAN_NATIVE_VXLAN_VERSION
            || endpoint.record_size as usize != size_of::<MwanNativeVxlanEndpoint>()
            || endpoint.reserved != 0
            || !pci_valid(&endpoint.pci)
            || endpoint.linux_ifindex == 0
            || slot >= self.config.receiver_count as usize
        {
            return Err(vxlan_fault(
                libc::EINVAL,
                MWAN_NATIVE_OP_CONFIG,
                endpoint.receiver_id,
                "Invalid declared kernel VF TX endpoint",
            ));
        }
        let receiver = self.config.receivers[slot];
        if receiver.vf != endpoint.source_vf {
            return Err(vxlan_fault(
                libc::EXDEV,
                MWAN_NATIVE_OP_CONFIG,
                endpoint.receiver_id,
                "Kernel VF differs from the declared receiver",
            ));
        }
        if self.vxlan[slot].owns_resources() || self.vxlan[slot].generation == u64::MAX {
            return Err(vxlan_fault(
                libc::EBUSY,
                MWAN_NATIVE_OP_CONFIG,
                endpoint.receiver_id,
                "Kernel VF already owns a VXLAN context or exhausted its generations",
            ));
        }
        let interface = self.verify_vxlan_endpoint(&endpoint)?;
        let generation = self.vxlan[slot].generation + 1;
        self.vxlan[slot] = Context {
            generation,
            endpoint: Some(endpoint),
            interface: Some(interface),
            ..Context::default()
        };
        Ok(self.vxlan_result(slot))
    }

    fn verify_vxlan_endpoint(&self, endpoint: &MwanNativeVxlanEndpoint) -> NativeResult<PathBuf> {
        let unavailable = |error: std::io::Error| {
            vxlan_fault(
                error.raw_os_error().unwrap_or(libc::EIO),
                MWAN_NATIVE_OP_CONFIG,
                endpoint.receiver_id,
                "Expose kernel VF PCI, physfn, virtfn and net sysfs records to verify its TX endpoint",
            )
        };
        let receiver = self.config.receivers[endpoint.receiver_id as usize];
        let controller = std::fs::canonicalize(format!(
            "/sys/bus/pci/devices/{}",
            pci_name(&self.config.controllers[receiver.controller as usize].pci)
        ))
        .map_err(unavailable)?;
        let parent = match std::fs::canonicalize(controller.join("physfn")) {
            Ok(parent) => parent,
            Err(error) if error.raw_os_error() == Some(libc::ENOENT) => controller,
            Err(error) => return Err(unavailable(error)),
        };
        let expected = std::fs::canonicalize(parent.join(format!("virtfn{}", endpoint.source_vf)))
            .map_err(unavailable)?;
        let actual =
            std::fs::canonicalize(format!("/sys/bus/pci/devices/{}", pci_name(&endpoint.pci)))
                .map_err(unavailable)?;
        if actual != expected {
            return Err(vxlan_fault(
                libc::EXDEV,
                MWAN_NATIVE_OP_CONFIG,
                endpoint.receiver_id,
                "Kernel VF PCI does not match the controller's represented source VF",
            ));
        }
        let driver = std::fs::canonicalize(actual.join("driver")).map_err(unavailable)?;
        if driver.file_name().is_none_or(|name| name != "bnxt_en") {
            return Err(vxlan_fault(
                libc::ENOTSUP,
                MWAN_NATIVE_OP_CONFIG,
                endpoint.receiver_id,
                "The declared source VF must use the Linux bnxt_en driver",
            ));
        }
        for entry in std::fs::read_dir(actual.join("net")).map_err(unavailable)? {
            let entry = entry.map_err(unavailable)?;
            let interface = entry.path();
            let index = std::fs::read_to_string(interface.join("ifindex")).map_err(unavailable)?;
            if index.trim().parse::<u32>().ok() != Some(endpoint.linux_ifindex) {
                continue;
            }
            let mac = std::fs::read_to_string(interface.join("address")).map_err(unavailable)?;
            let expected = receiver.mac.map(|byte| format!("{byte:02x}")).join(":");
            if mac.trim() != expected {
                return Err(vxlan_fault(
                    libc::EXDEV,
                    MWAN_NATIVE_OP_CONFIG,
                    endpoint.receiver_id,
                    "Kernel VF MAC differs from its declared receiver identity",
                ));
            }
            return Ok(interface);
        }
        Err(vxlan_fault(
            libc::ENODEV,
            MWAN_NATIVE_OP_CONFIG,
            endpoint.receiver_id,
            "Linux ifindex does not identify the declared source VF",
        ))
    }

    fn install_vxlan(
        &mut self,
        handle: MwanNativeHandle,
        input: MwanNativeVxlanPlan,
    ) -> NativeResult<MwanNativeVxlanResult> {
        let slot = self.vxlan_slot(handle)?;
        if !self.active
            || self.faulted
            || self.quiescing
            || self.vxlan[slot].flow.is_some()
            || self.vxlan[slot].cleanup_failed
            || self.vxlan[slot].rule_generation == u64::MAX
        {
            return Err(vxlan_fault(
                libc::EBUSY,
                MWAN_NATIVE_OP_CREATE,
                slot as u32,
                "VXLAN context cannot admit a rule",
            ));
        }
        let plan = Plan::try_from(input).map_err(|InvalidPlan(message)| {
            vxlan_fault(libc::EINVAL, MWAN_NATIVE_OP_VALIDATE, slot as u32, message)
        })?;
        let receiver = self.config.receivers[slot];
        if input.inner_source_mac != receiver.mac
            || input.outer_source_mac != receiver.mac
            || input.underlay_mtu > self.config.mtu
        {
            return Err(vxlan_fault(
                libc::EINVAL,
                MWAN_NATIVE_OP_VALIDATE,
                slot as u32,
                "VXLAN source MACs and underlay MTU must match the declared kernel VF graph",
            ));
        }
        let endpoint = self.vxlan[slot].endpoint.ok_or_else(|| {
            vxlan_fault(
                libc::ENODEV,
                MWAN_NATIVE_OP_CONFIG,
                slot as u32,
                "VXLAN endpoint is unavailable",
            )
        })?;
        let interface = self.verify_vxlan_endpoint(&endpoint)?;
        if self.vxlan[slot].interface.as_ref() != Some(&interface) {
            return Err(vxlan_fault(
                libc::EXDEV,
                MWAN_NATIVE_OP_VALIDATE,
                slot as u32,
                "Kernel VF interface identity changed",
            ));
        }
        let mtu = std::fs::read_to_string(interface.join("mtu")).map_err(|error| {
            vxlan_fault(
                error.raw_os_error().unwrap_or(libc::EIO),
                MWAN_NATIVE_OP_VALIDATE,
                slot as u32,
                "Cannot read the declared kernel VF MTU",
            )
        })?;
        let mtu = mtu.trim().parse::<u16>().map_err(|_| {
            vxlan_fault(
                libc::EINVAL,
                MWAN_NATIVE_OP_VALIDATE,
                slot as u32,
                "Kernel VF MTU is invalid",
            )
        })?;
        if mtu > input.inner_mtu {
            return Err(vxlan_fault(
                libc::EMSGSIZE,
                MWAN_NATIVE_OP_VALIDATE,
                slot as u32,
                "Kernel VF MTU exceeds the VXLAN inner MTU budget",
            ));
        }
        self.build_vxlan_flow(slot, plan, true)?;
        self.vxlan[slot].rule_generation += 1;
        self.vxlan[slot].plan = Some(plan);
        self.vxlan[slot].flow = self.build_vxlan_flow(slot, plan, false)?;
        Ok(self.vxlan_result(slot))
    }

    fn remove_vxlan(&mut self, slot: usize) -> NativeResult<()> {
        if self.vxlan[slot].cleanup_failed {
            return Err(vxlan_fault(
                libc::EIO,
                MWAN_NATIVE_OP_DESTROY,
                slot as u32,
                "VXLAN deletion consumed an uncertain handle; context and plan require process recovery",
            ));
        }
        let Some(flow) = self.vxlan[slot].flow else {
            return Ok(());
        };
        let receiver = self.config.receivers[slot];
        let controller = self.controller_port(receiver.controller as usize)?;
        let mut detail = dpdk::rte_flow_error::default();
        // SAFETY: This live flow belongs to this controller. BNXT can consume
        // the handle on failure. This owner never retries the raw pointer.
        let result = unsafe { dpdk::rte_flow_destroy(controller, flow.as_ptr(), &mut detail) };
        self.vxlan[slot].flow = None;
        if result != 0 {
            self.vxlan[slot].cleanup_failed = true;
            self.faulted = true;
            self.quiescing = true;
            return Err(flow_fault(
                result,
                MWAN_NATIVE_OP_DESTROY,
                MWAN_NATIVE_OBJECT_RECEIVER,
                slot as u32,
                0,
                &detail,
            ));
        }
        Ok(())
    }

    pub(super) fn forget_vxlan_contexts(&mut self) {
        for context in &mut self.vxlan {
            context.endpoint = None;
            context.interface = None;
            context.plan = None;
        }
    }

    pub(super) fn suspend_vxlan_rules(&mut self) -> NativeResult<()> {
        let mut failure = None;
        for slot in 0..self.vxlan.len() {
            if let Err(error) = self.remove_vxlan(slot) {
                retain_cleanup_failure(&mut failure, error);
            }
        }
        match failure {
            Some(error) => Err(error),
            None => Ok(()),
        }
    }

    fn query_vxlan(&self, slot: usize, output: &mut MwanNativeVxlanStats) -> NativeResult<()> {
        output.result = self.vxlan_result(slot);
        let flow = self.vxlan[slot].flow.ok_or_else(|| {
            vxlan_fault(
                libc::ENOENT,
                MWAN_NATIVE_OP_QUERY,
                slot as u32,
                "VXLAN rule does not own a queryable hardware handle",
            )
        })?;
        let receiver = self.config.receivers[slot];
        let controller = self.controller_port(receiver.controller as usize)?;
        let counter_action = dpdk::rte_flow_action {
            type_: dpdk::RTE_FLOW_ACTION_TYPE_COUNT,
            conf: ptr::null(),
        };
        let mut count = dpdk::rte_flow_query_count::default();
        let mut detail = dpdk::rte_flow_error::default();
        // SAFETY: The live hardware handle belongs to this controller. All
        // output records remain writable through the synchronous query.
        let result = unsafe {
            dpdk::rte_flow_query(
                controller,
                flow.as_ptr(),
                &counter_action,
                (&mut count as *mut dpdk::rte_flow_query_count).cast(),
                &mut detail,
            )
        };
        output.counter.error = result;
        if result != 0 {
            return Err(flow_fault(
                result,
                MWAN_NATIVE_OP_QUERY,
                MWAN_NATIVE_OBJECT_RECEIVER,
                slot as u32,
                0,
                &detail,
            ));
        }
        // SAFETY: The successful query initialized the SDK count record.
        let (packets_valid, bytes_valid) = unsafe {
            (
                dpdk::mwn_flow_count_hits_valid(&count),
                dpdk::mwn_flow_count_bytes_valid(&count),
            )
        };
        if !packets_valid || !bytes_valid {
            output.counter.error = -libc::EAGAIN;
            return Err(vxlan_fault(
                libc::EAGAIN,
                MWAN_NATIVE_OP_QUERY,
                slot as u32,
                "VXLAN hardware counter sample is not valid",
            ));
        }
        // SAFETY: The query set both validity bits in this live count record.
        output.counter.packets = unsafe { dpdk::mwn_flow_count_hits(&count) };
        // SAFETY: The query set both validity bits in this live count record.
        output.counter.bytes = unsafe { dpdk::mwn_flow_count_bytes(&count) };
        output.counter.packets_valid = 1;
        output.counter.bytes_valid = 1;
        output.counter.observed_monotonic_ns = monotonic_ns();
        Ok(())
    }
}

impl OwnerState {
    fn build_vxlan_flow(
        &self,
        slot: usize,
        plan: Plan,
        validate: bool,
    ) -> NativeResult<Option<NonNull<dpdk::rte_flow>>> {
        let plan = plan.record();
        let receiver = self.config.receivers[slot];
        let controller = self.controller_port(receiver.controller as usize)?;
        let source = dpdk::rte_flow_item_port_id {
            id: u32::from(self.receiver_port(slot)?),
        };
        let source_mask = dpdk::rte_flow_item_port_id { id: u32::MAX };
        let mut attributes = dpdk::rte_flow_attr::default();
        let mut ethernet = dpdk::rte_flow_item_eth::default();
        let mut ethernet_mask = dpdk::rte_flow_item_eth::default();
        let mut ipv6 = dpdk::rte_flow_item_ipv6::default();
        let mut ipv6_mask = dpdk::rte_flow_item_ipv6::default();
        let mut tcp = dpdk::rte_flow_item_tcp::default();
        let mut tcp_mask = dpdk::rte_flow_item_tcp::default();
        let mut outer_ethernet = dpdk::rte_flow_item_eth::default();
        let mut outer_ipv4 = dpdk::rte_flow_item_ipv4::default();
        let mut outer_udp = dpdk::rte_flow_item_udp::default();
        let mut outer_vxlan = dpdk::rte_flow_item_vxlan::default();
        let vni = plan.vni.to_be_bytes();
        // SAFETY: Every helper copies scalar fields into initialized aligned
        // SDK records. Address slices have the exact lengths in the SDK API.
        unsafe {
            dpdk::mwn_flow_attr_set_transfer(&mut attributes, true);
            dpdk::mwn_flow_eth_set(
                &mut ethernet,
                plan.inner_source_mac.as_ptr(),
                plan.inner_destination_mac.as_ptr(),
                0x86dd_u16.to_be(),
            );
            dpdk::mwn_flow_eth_set(
                &mut ethernet_mask,
                [u8::MAX; 6].as_ptr(),
                [u8::MAX; 6].as_ptr(),
                u16::MAX,
            );
            dpdk::mwn_flow_ipv6_set(
                &mut ipv6,
                plan.inner.source.as_ptr(),
                plan.inner.destination.as_ptr(),
                MWAN_NATIVE_PROTOCOL_TCP as u8,
            );
            dpdk::mwn_flow_ipv6_set(
                &mut ipv6_mask,
                [u8::MAX; 16].as_ptr(),
                [u8::MAX; 16].as_ptr(),
                u8::MAX,
            );
            dpdk::mwn_flow_tcp_set(
                &mut tcp,
                plan.inner.source_port.to_be(),
                plan.inner.destination_port.to_be(),
            );
            dpdk::mwn_flow_tcp_set(&mut tcp_mask, u16::MAX, u16::MAX);
            dpdk::mwn_flow_eth_set(
                &mut outer_ethernet,
                plan.outer_source_mac.as_ptr(),
                plan.outer_destination_mac.as_ptr(),
                0x0800_u16.to_be(),
            );
            dpdk::mwn_flow_ipv4_set(
                &mut outer_ipv4,
                u32::from_ne_bytes(plan.outer_source),
                u32::from_ne_bytes(plan.outer_destination),
                MWAN_NATIVE_PROTOCOL_UDP as u8,
            );
            dpdk::mwn_flow_ipv4_envelope_set(&mut outer_ipv4, plan.outer_ttl, plan.outer_tos);
            dpdk::mwn_flow_udp_set(
                &mut outer_udp,
                plan.outer_source_port.to_be(),
                plan.outer_destination_port.to_be(),
            );
            dpdk::mwn_flow_vxlan_set(&mut outer_vxlan, vni[1..].as_ptr());
        }
        let mut pattern = [
            item(dpdk::RTE_FLOW_ITEM_TYPE_PORT_ID, &source, &source_mask),
            item(dpdk::RTE_FLOW_ITEM_TYPE_ETH, &ethernet, &ethernet_mask),
            item(dpdk::RTE_FLOW_ITEM_TYPE_IPV6, &ipv6, &ipv6_mask),
            item(dpdk::RTE_FLOW_ITEM_TYPE_TCP, &tcp, &tcp_mask),
            dpdk::rte_flow_item::default(),
        ];
        pattern[4].type_ = dpdk::RTE_FLOW_ITEM_TYPE_END;
        let definition = |kind, pointer| dpdk::rte_flow_item {
            type_: kind,
            spec: pointer,
            last: ptr::null(),
            mask: ptr::null(),
        };
        let mut envelope = [
            definition(
                dpdk::RTE_FLOW_ITEM_TYPE_ETH,
                (&outer_ethernet as *const dpdk::rte_flow_item_eth).cast(),
            ),
            definition(
                dpdk::RTE_FLOW_ITEM_TYPE_IPV4,
                (&outer_ipv4 as *const dpdk::rte_flow_item_ipv4).cast(),
            ),
            definition(
                dpdk::RTE_FLOW_ITEM_TYPE_UDP,
                (&outer_udp as *const dpdk::rte_flow_item_udp).cast(),
            ),
            definition(
                dpdk::RTE_FLOW_ITEM_TYPE_VXLAN,
                (&outer_vxlan as *const dpdk::rte_flow_item_vxlan).cast(),
            ),
            dpdk::rte_flow_item::default(),
        ];
        envelope[4].type_ = dpdk::RTE_FLOW_ITEM_TYPE_END;
        let encapsulation = dpdk::rte_flow_action_vxlan_encap {
            definition: envelope.as_mut_ptr(),
        };
        let count = dpdk::rte_flow_action_count::default();
        let mut destination = dpdk::rte_flow_action_port_id::default();
        // SAFETY: The initialized output record receives the owned controller ID.
        unsafe { dpdk::mwn_flow_port_id_set(&mut destination, u32::from(controller)) };
        let mut actions = [
            action(dpdk::RTE_FLOW_ACTION_TYPE_COUNT, &count),
            action(dpdk::RTE_FLOW_ACTION_TYPE_VXLAN_ENCAP, &encapsulation),
            action(dpdk::RTE_FLOW_ACTION_TYPE_PORT_ID, &destination),
            dpdk::rte_flow_action::default(),
        ];
        actions[3].type_ = dpdk::RTE_FLOW_ACTION_TYPE_END;
        let mut detail = dpdk::rte_flow_error::default();
        if validate {
            // SAFETY: Every item, mask, action and envelope payload remains
            // live and immovable through this synchronous SDK validation.
            let result = unsafe {
                dpdk::rte_flow_validate(
                    controller,
                    &attributes,
                    pattern.as_ptr(),
                    actions.as_ptr(),
                    &mut detail,
                )
            };
            if result != 0 {
                return Err(flow_fault(
                    result,
                    MWAN_NATIVE_OP_VALIDATE,
                    MWAN_NATIVE_OBJECT_RECEIVER,
                    slot as u32,
                    0,
                    &detail,
                ));
            }
            return Ok(None);
        }
        // SAFETY: The synchronous SDK call copies the complete terminated
        // pattern and action records into a flow owned by this controller.
        let flow = unsafe {
            dpdk::rte_flow_create(
                controller,
                &attributes,
                pattern.as_ptr(),
                actions.as_ptr(),
                &mut detail,
            )
        };
        NonNull::new(flow).map(Some).ok_or_else(|| {
            flow_fault(
                sdk_errno(),
                MWAN_NATIVE_OP_CREATE,
                MWAN_NATIVE_OBJECT_RECEIVER,
                slot as u32,
                0,
                &detail,
            )
        })
    }
}

/// # Safety
/// The caller supplies a live ABI2 owner on its exclusive owner thread and
/// separate, aligned, complete endpoint, result and error records.
#[unsafe(no_mangle)]
pub unsafe extern "C" fn mwan_native_vxlan_context_open(
    owner: *mut MwanNative,
    endpoint: *const MwanNativeVxlanEndpoint,
    result: *mut MwanNativeVxlanResult,
    error: *mut MwanNativeError,
) -> i32 {
    boundary(owner, error, MWAN_NATIVE_OP_CONFIG, || {
        if endpoint.is_null() || result.is_null() {
            return Err(vxlan_fault(
                libc::EINVAL,
                MWAN_NATIVE_OP_CONFIG,
                0,
                "VXLAN endpoint and result records are required",
            ));
        }
        // SAFETY: The caller supplies the complete readable endpoint record.
        let endpoint = unsafe { endpoint.read() };
        // SAFETY: The caller supplies this library's live exclusive owner.
        let state = unsafe { state_for(owner) }?;
        let output = state.open_vxlan_context(endpoint)?;
        // SAFETY: The separate output remains writable after context creation.
        unsafe { result.write(output) };
        Ok(())
    })
}

/// # Safety
/// The caller supplies a live exclusive owner on its owner thread and separate
/// aligned, complete plan, result and error records. The library copies the plan.
#[unsafe(no_mangle)]
pub unsafe extern "C" fn mwan_native_vxlan_install(
    owner: *mut MwanNative,
    context: MwanNativeHandle,
    plan: *const MwanNativeVxlanPlan,
    result: *mut MwanNativeVxlanResult,
    error: *mut MwanNativeError,
) -> i32 {
    boundary(owner, error, MWAN_NATIVE_OP_CREATE, || {
        if plan.is_null() || result.is_null() {
            return Err(vxlan_fault(
                libc::EINVAL,
                MWAN_NATIVE_OP_CREATE,
                context.slot,
                "VXLAN plan and result records are required",
            ));
        }
        // SAFETY: The caller supplies this library's live exclusive owner.
        let state = unsafe { state_for(owner) }?;
        let slot = state.vxlan_slot(context)?;
        // SAFETY: The input is a complete readable record, copied before mutation.
        let input = unsafe { plan.read() };
        let outcome = state.install_vxlan(context, input);
        // SAFETY: The separate output remains writable even after partial failure.
        unsafe { result.write(state.vxlan_result(slot)) };
        outcome.map(|_| ())
    })
}

/// # Safety
/// The caller supplies a live exclusive owner on its owner thread and separate
/// aligned, complete writable statistics and error records.
#[unsafe(no_mangle)]
pub unsafe extern "C" fn mwan_native_vxlan_query(
    owner: *mut MwanNative,
    rule: MwanNativeVxlanResult,
    stats: *mut MwanNativeVxlanStats,
    error: *mut MwanNativeError,
) -> i32 {
    boundary(owner, error, MWAN_NATIVE_OP_QUERY, || {
        if stats.is_null() {
            return Err(vxlan_fault(
                libc::EINVAL,
                MWAN_NATIVE_OP_QUERY,
                rule.context.slot,
                "VXLAN statistics are required",
            ));
        }
        // SAFETY: The caller supplies this library's live exclusive owner.
        let state = unsafe { state_for(owner) }?;
        let slot = state.vxlan_rule_slot(rule)?;
        let mut output = MwanNativeVxlanStats::default();
        let outcome = state.query_vxlan(slot, &mut output);
        // SAFETY: The separate statistics remain writable after a query failure.
        unsafe { stats.write(output) };
        outcome
    })
}

/// # Safety
/// The caller supplies a live exclusive owner on its owner thread and separate
/// aligned, complete writable result and error records.
#[unsafe(no_mangle)]
pub unsafe extern "C" fn mwan_native_vxlan_remove(
    owner: *mut MwanNative,
    rule: MwanNativeVxlanResult,
    result: *mut MwanNativeVxlanResult,
    error: *mut MwanNativeError,
) -> i32 {
    boundary(owner, error, MWAN_NATIVE_OP_DESTROY, || {
        if result.is_null() {
            return Err(vxlan_fault(
                libc::EINVAL,
                MWAN_NATIVE_OP_DESTROY,
                rule.context.slot,
                "VXLAN removal result is required",
            ));
        }
        // SAFETY: The caller supplies this library's live exclusive owner.
        let state = unsafe { state_for(owner) }?;
        let slot = state.vxlan_rule_slot(rule)?;
        let outcome = state.remove_vxlan(slot);
        // SAFETY: The output records retained ownership after a cleanup failure.
        unsafe { result.write(state.vxlan_result(slot)) };
        outcome
    })
}

/// # Safety
/// The caller supplies a live exclusive owner on its owner thread and separate
/// aligned, complete writable result and error records.
#[unsafe(no_mangle)]
pub unsafe extern "C" fn mwan_native_vxlan_context_close(
    owner: *mut MwanNative,
    context: MwanNativeHandle,
    result: *mut MwanNativeVxlanResult,
    error: *mut MwanNativeError,
) -> i32 {
    boundary(owner, error, MWAN_NATIVE_OP_DESTROY, || {
        if result.is_null() {
            return Err(vxlan_fault(
                libc::EINVAL,
                MWAN_NATIVE_OP_DESTROY,
                context.slot,
                "VXLAN context close result is required",
            ));
        }
        // SAFETY: Cleanup must remain available after an owner terminal fault.
        let state = unsafe { owner_state(owner) }?;
        let slot = state.vxlan_slot(context)?;
        let outcome = state.remove_vxlan(slot);
        if outcome.is_ok() {
            state.vxlan[slot].endpoint = None;
            state.vxlan[slot].interface = None;
            state.vxlan[slot].plan = None;
        }
        // SAFETY: The output remains writable after successful or failed cleanup.
        unsafe { result.write(state.vxlan_result(slot)) };
        outcome
    })
}
