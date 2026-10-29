use crate::abi::*;
use std::mem::size_of;
use std::net::{Ipv4Addr, Ipv6Addr};
use std::panic::{AssertUnwindSafe, catch_unwind};

const MINIMUM_INNER_MTU: u16 = 1280;
const MAXIMUM_UNDERLAY_MTU: u16 = 9600;
const VXLAN_OVERHEAD: u16 = 50;
const VXLAN_DESTINATION_PORT: u16 = 4789;
const MAXIMUM_VNI: u32 = 0x00ff_ffff;

#[derive(Debug)]
pub(crate) struct InvalidPlan(pub(crate) &'static str);

/// Only validation can construct this copied plan.
#[derive(Clone, Copy)]
pub(crate) struct Plan {
    record: MwanNativeVxlanPlan,
}

impl TryFrom<MwanNativeVxlanPlan> for Plan {
    type Error = InvalidPlan;

    fn try_from(record: MwanNativeVxlanPlan) -> Result<Self, Self::Error> {
        if record.version != MWAN_NATIVE_VXLAN_VERSION
            || record.record_size as usize != size_of::<MwanNativeVxlanPlan>()
            || record.reserved != 0
            || record.inner.reserved != 0
        {
            return Err(InvalidPlan(
                "VXLAN plan version, size or reserved fields are invalid",
            ));
        }
        if record.inner.family != MWAN_NATIVE_FAMILY_IPV6 as u8
            || record.inner.protocol != MWAN_NATIVE_PROTOCOL_TCP as u8
        {
            return Err(InvalidPlan(
                "VXLAN encapsulation requires an exact IPv6 TCP tuple",
            ));
        }
        for bytes in [record.inner.source, record.inner.destination] {
            let address = Ipv6Addr::from(bytes);
            if address.is_unspecified() || address.is_multicast() || address.is_loopback() {
                return Err(InvalidPlan(
                    "Inner IPv6 addresses must identify unicast endpoints",
                ));
            }
        }
        if record.inner.source_port == 0 || record.inner.destination_port == 0 {
            return Err(InvalidPlan("Inner TCP ports must be nonzero"));
        }
        for bytes in [record.outer_source, record.outer_destination] {
            let address = Ipv4Addr::from(bytes);
            if address.is_unspecified()
                || address.is_multicast()
                || address.is_broadcast()
                || address.is_loopback()
                || bytes[0] == 0
                || bytes[0] >= 240
            {
                return Err(InvalidPlan(
                    "Outer IPv4 addresses must identify unicast endpoints",
                ));
            }
        }
        if record.outer_source == record.outer_destination {
            return Err(InvalidPlan("Outer IPv4 endpoints must differ"));
        }
        for address in [
            record.inner_source_mac,
            record.inner_destination_mac,
            record.outer_source_mac,
            record.outer_destination_mac,
        ] {
            if address[0] & 1 != 0 || address == [0; 6] {
                return Err(InvalidPlan(
                    "VXLAN Ethernet addresses must be nonzero unicast addresses",
                ));
            }
        }
        if record.vni == 0 || record.vni > MAXIMUM_VNI {
            return Err(InvalidPlan("VXLAN VNI must fit the nonzero 24-bit range"));
        }
        if record.outer_source_port == 0
            || record.outer_destination_port != VXLAN_DESTINATION_PORT
            || record.outer_ttl == 0
        {
            return Err(InvalidPlan(
                "VXLAN requires a nonzero UDP source, destination 4789 and TTL",
            ));
        }
        if record.inner_mtu < MINIMUM_INNER_MTU
            || record.underlay_mtu > MAXIMUM_UNDERLAY_MTU
            || u32::from(record.inner_mtu) + u32::from(VXLAN_OVERHEAD)
                > u32::from(record.underlay_mtu)
        {
            return Err(InvalidPlan(
                "Underlay MTU must include the IPv6 MTU and 50 VXLAN bytes",
            ));
        }
        if record.cookie == 0 || record.policy_generation == 0 {
            return Err(InvalidPlan(
                "VXLAN cookie and policy generation must be nonzero",
            ));
        }
        Ok(Self { record })
    }
}

impl Plan {
    pub(crate) fn record(self) -> MwanNativeVxlanPlan {
        self.record
    }
}

/// # Safety
/// The caller supplies separate, aligned, complete readable plan and writable error
/// records for this nonreentrant call. The library copies the input record.
#[unsafe(no_mangle)]
pub unsafe extern "C" fn mwan_native_vxlan_plan_validate(
    plan: *const MwanNativeVxlanPlan,
    error: *mut MwanNativeError,
) -> i32 {
    if error.is_null() {
        return -libc::EINVAL;
    }
    let outcome = catch_unwind(AssertUnwindSafe(|| {
        if plan.is_null() {
            return Err(InvalidPlan("A complete VXLAN plan is required"));
        }
        // SAFETY: The caller supplies a complete aligned readable ABI record.
        let copied = unsafe { plan.read() };
        Plan::try_from(copied).map(Plan::record).map(|_| ())
    }));
    let mut output = MwanNativeError::default();
    let message = match outcome {
        Ok(Ok(())) => None,
        Ok(Err(InvalidPlan(message))) => {
            output.code = libc::EINVAL;
            Some(message)
        }
        Err(_) => {
            output.code = libc::EFAULT;
            Some("VXLAN plan validation panicked")
        }
    };
    if let Some(message) = message {
        output.operation = MWAN_NATIVE_OP_VALIDATE;
        for (target, byte) in output.message.iter_mut().zip(message.bytes()) {
            *target = byte as std::ffi::c_char;
        }
    }
    // SAFETY: The caller supplies the separate writable error record.
    unsafe { error.write(output) };
    -output.code
}

#[cfg(test)]
mod tests {
    use super::*;

    pub(crate) fn input_plan() -> MwanNativeVxlanPlan {
        MwanNativeVxlanPlan {
            version: MWAN_NATIVE_VXLAN_VERSION,
            record_size: size_of::<MwanNativeVxlanPlan>() as u32,
            cookie: 1,
            policy_generation: 1,
            inner: MwanNativeTuple {
                family: 6,
                protocol: 6,
                source: Ipv6Addr::new(0x3d06, 0xbad, 0xb01, 0x305, 0, 0, 0, 0x100).octets(),
                destination: Ipv6Addr::new(0xfd06, 0xfab, 0x7702, 0, 0, 0, 0, 2).octets(),
                source_port: 45001,
                destination_port: 5227,
                ..MwanNativeTuple::default()
            },
            inner_source_mac: [2, 0, 0, 0, 0, 1],
            inner_destination_mac: [2, 0, 0, 0, 0, 2],
            outer_source_mac: [2, 0, 0, 0, 0, 1],
            outer_destination_mac: [2, 0, 0, 0, 0, 3],
            outer_source: [10, 230, 0, 1],
            outer_destination: [10, 230, 0, 101],
            vni: 7702,
            outer_source_port: 49152,
            outer_destination_port: 4789,
            inner_mtu: 1450,
            underlay_mtu: 1500,
            outer_ttl: 64,
            ..MwanNativeVxlanPlan::default()
        }
    }

    fn check(plan: &MwanNativeVxlanPlan) -> (i32, MwanNativeError) {
        let mut error = MwanNativeError::default();
        // SAFETY: The complete ABI input and output use separate allocations.
        let result = unsafe { mwan_native_vxlan_plan_validate(plan, &mut error) };
        (result, error)
    }

    #[test]
    fn public_validation_accepts_the_fixed_envelope_and_ipv6_mtu_boundary() {
        let plan = input_plan();
        assert_eq!(check(&plan).0, 0);
        let minimum = MwanNativeVxlanPlan {
            inner_mtu: 1280,
            underlay_mtu: 1330,
            ..plan
        };
        assert_eq!(check(&minimum).0, 0);
        let too_small = MwanNativeVxlanPlan {
            underlay_mtu: 1329,
            ..minimum
        };
        let (result, error) = check(&too_small);
        assert_eq!(result, -libc::EINVAL);
        assert_eq!(error.operation, MWAN_NATIVE_OP_VALIDATE);
    }

    #[test]
    fn public_validation_rejects_unsupported_envelopes_and_inexact_tuples() {
        let original = input_plan();
        let mut cases = Vec::new();
        let mut plan = original;
        plan.inner.protocol = 17;
        cases.push(plan);
        plan = original;
        plan.inner.family = 4;
        cases.push(plan);
        plan = original;
        plan.inner.destination_port = 0;
        cases.push(plan);
        plan = original;
        plan.outer_destination_port = 8472;
        cases.push(plan);
        plan = original;
        plan.vni = 0x0100_0000;
        cases.push(plan);
        plan = original;
        plan.outer_destination_mac[0] = 1;
        cases.push(plan);
        plan = original;
        plan.outer_source = [127, 0, 0, 1];
        cases.push(plan);
        plan = original;
        plan.record_size -= 1;
        cases.push(plan);
        plan = original;
        plan.reserved = 1;
        cases.push(plan);
        for plan in cases {
            let (result, error) = check(&plan);
            assert_eq!(result, -libc::EINVAL);
            assert_ne!(error.message[0], 0);
        }
    }

    #[test]
    fn public_validation_rejects_null_and_clears_an_old_error_after_success() {
        let mut error = MwanNativeError::default();
        // SAFETY: Null input must fail before a read, and error remains writable.
        let result = unsafe { mwan_native_vxlan_plan_validate(std::ptr::null(), &mut error) };
        assert_eq!(result, -libc::EINVAL);
        let plan = input_plan();
        // SAFETY: Both complete ABI records remain separate and live.
        let result = unsafe { mwan_native_vxlan_plan_validate(&plan, &mut error) };
        assert_eq!(result, 0);
        assert_eq!(error, MwanNativeError::default());
    }
}
