//! Callers must preserve the SDK's pointer, buffer, and thread requirements.

include!(concat!(env!("OUT_DIR"), "/dpdk_bindings.rs"));

impl Default for RteFlowAction {
    fn default() -> Self {
        Self {
            type_: 0,
            conf: std::ptr::null(),
        }
    }
}

impl Default for RteFlowError {
    fn default() -> Self {
        Self {
            type_: 0,
            cause: std::ptr::null(),
            message: std::ptr::null(),
        }
    }
}

impl Default for RteFlowItem {
    fn default() -> Self {
        Self {
            type_: 0,
            spec: std::ptr::null(),
            last: std::ptr::null(),
            mask: std::ptr::null(),
        }
    }
}
