use std::collections::{BTreeMap, BTreeSet};
use std::env;
use std::error::Error;
use std::fs;
use std::io;
use std::path::{Path, PathBuf};
use std::process::Command;
use std::sync::{Arc, Mutex};

use bindgen::callbacks::{DeriveInfo, ItemInfo, ItemKind, ParseCallbacks};

#[derive(Debug, Clone, Default)]
struct TypeNames {
    aliases: Arc<Mutex<BTreeMap<String, String>>>,
    default_derives: Vec<String>,
}

impl ParseCallbacks for TypeNames {
    fn item_name(&self, item: ItemInfo<'_>) -> Option<String> {
        if !matches!(item.kind, ItemKind::Type) {
            return None;
        }
        let name = rust_type_name(item.name);
        if item.name.starts_with("rte_") && name != item.name {
            self.aliases
                .lock()
                .expect("bindgen type callback owns the alias map")
                .insert(item.name.to_owned(), name.clone());
        }
        Some(name)
    }

    fn add_derives(&self, item: &DeriveInfo<'_>) -> Vec<String> {
        if self.default_derives.contains(&rust_type_name(item.name)) {
            return vec!["Default".to_owned()];
        }
        Vec::new()
    }
}

fn rust_type_name(name: &str) -> String {
    let leading = name.bytes().take_while(|byte| *byte == b'_').count();
    let mut result = String::new();
    if leading != 0 {
        result.push_str(&format!("Internal{leading}"));
    }
    for word in name.split('_').filter(|word| !word.is_empty()) {
        let mut letters = word.chars();
        if let Some(first) = letters.next() {
            result.extend(first.to_uppercase());
            result.extend(letters);
        }
    }
    result
}

fn main() -> Result<(), Box<dyn Error>> {
    println!("cargo:rerun-if-changed=include/mwan_native.h");
    println!("cargo:rerun-if-changed=src/dpdk_shim.h");
    println!("cargo:rerun-if-changed=src/dpdk_shim.c");
    let output = PathBuf::from(env::var("OUT_DIR")?);
    bindgen::Builder::default()
        .header("include/mwan_native.h")
        .allowlist_type("mwan_native.*")
        .allowlist_var("MWAN_NATIVE_.*")
        .blocklist_function(".*")
        .prepend_enum_name(false)
        .derive_default(true)
        .no_default("mwan_native_config|MwanNativeConfig")
        .no_default("mwan_native_error|MwanNativeError")
        .no_default("mwan_native_build_info|MwanNativeBuildInfo")
        .no_default("mwan_native_frame|MwanNativeFrame")
        .derive_eq(true)
        .derive_partialeq(true)
        .derive_hash(true)
        .layout_tests(true)
        .generate_comments(false)
        .parse_callbacks(Box::new(TypeNames::default()))
        .generate()?
        .write_to_file(output.join("abi_bindings.rs"))?;
    if env::var_os("CARGO_FEATURE_DPDK").is_some() {
        build_dpdk(&output)?;
    }
    Ok(())
}

fn required_hash(name: &str) -> Result<String, Box<dyn Error>> {
    println!("cargo:rerun-if-env-changed={name}");
    let value = env::var(name).map_err(|error| {
        io::Error::new(
            io::ErrorKind::InvalidInput,
            format!("Set {name} to the verified supplied SDK hash: {error}"),
        )
    })?;
    if value.len() != 64 || !value.bytes().all(|byte| byte.is_ascii_hexdigit()) {
        return Err(io::Error::new(
            io::ErrorKind::InvalidInput,
            format!("{name} must contain exactly 64 hexadecimal digits"),
        )
        .into());
    }
    Ok(value)
}

fn build_dpdk(output: &Path) -> Result<(), Box<dyn Error>> {
    if env::var("CARGO_CFG_TARGET_OS")? != "linux" {
        return Err(io::Error::new(
            io::ErrorKind::Unsupported,
            "The dpdk feature requires a Linux target and the supplied DPDK 26.03 SDK",
        )
        .into());
    }
    for name in ["MWAN_DPDK_SOURCE_SHA256", "MWAN_DPDK_PATCH_SHA256"] {
        let value = required_hash(name)?;
        println!("cargo:rustc-env={name}={value}");
    }
    let sdk = pkg_config::Config::new()
        .exactly_version("26.03.0")
        .statik(true)
        .cargo_metadata(false)
        .probe("libdpdk")?;
    let opaque_types = [
        "rte_device",
        "rte_driver",
        "rte_eth_conf",
        "rte_eth_dev_info",
        "rte_eth_link",
        "rte_eth_rxconf",
        "rte_eth_txconf",
        "rte_flow_action_port_id",
        "rte_flow_attr",
        "rte_flow_group_attr",
        "rte_flow_item_eth",
        "rte_flow_item_ipv4",
        "rte_flow_item_ipv6",
        "rte_flow_item_tcp",
        "rte_flow_item_udp",
        "rte_flow_item_vxlan",
        "rte_flow_query_count",
        "rte_mbuf",
        "rte_mempool",
    ];
    let names = TypeNames {
        default_derives: opaque_types
            .iter()
            .copied()
            .filter(|name| {
                !matches!(
                    *name,
                    "rte_device" | "rte_driver" | "rte_mbuf" | "rte_mempool"
                )
            })
            .map(rust_type_name)
            .collect(),
        ..TypeNames::default()
    };
    let mut builder = bindgen::Builder::default()
        .header("src/dpdk_shim.h")
        .allowlist_recursively(false)
        .allowlist_function("mwn_.*")
        .allowlist_var("RTE_FLOW_(ITEM|ACTION)_TYPE_.*")
        .allowlist_var("RTE_PMD_BNXT_REP_ROLE_.*")
        .prepend_enum_name(false)
        .derive_default(true)
        .derive_debug(false)
        .layout_tests(true)
        .generate_comments(false)
        .clang_arg("-DALLOW_EXPERIMENTAL_API")
        .parse_callbacks(Box::new(names.clone()));
    for name in opaque_types {
        builder = builder
            .allowlist_type(name)
            .opaque_type(format!("{name}|{}", rust_type_name(name)))
            .blocklist_type(format!(
                "{name}__bindgen_ty_.*|{}BindgenTy.*",
                rust_type_name(name)
            ))
            .no_default(name)
            .no_default(rust_type_name(name));
    }
    for name in [
        "rte_flow_action",
        "rte_flow_action_vxlan_encap",
        "rte_flow_error",
        "rte_flow_item",
    ] {
        builder = builder.no_default(name).no_default(rust_type_name(name));
    }
    for name in [
        "rte_eth_conf",
        "rte_eth_dev_info",
        "rte_eth_link",
        "rte_ether_addr",
        "rte_be16_t",
        "rte_be32_t",
        "rte_flow",
        "rte_flow_action",
        "rte_flow_action_count",
        "rte_flow_action_vxlan_encap",
        "rte_flow_action_port_id",
        "rte_flow_action_set_ipv4",
        "rte_flow_action_set_ipv6",
        "rte_flow_action_set_tp",
        "rte_flow_action_type",
        "rte_flow_attr",
        "rte_flow_error",
        "rte_flow_error_type",
        "rte_flow_group_attr",
        "rte_flow_item",
        "rte_flow_item_eth",
        "rte_flow_item_ipv4",
        "rte_flow_item_ipv6",
        "rte_flow_item_tcp",
        "rte_flow_item_udp",
        "rte_flow_item_vxlan",
        "rte_flow_item_port_id",
        "rte_flow_item_type",
        "rte_flow_query_count",
        "rte_mbuf",
        "rte_mempool",
        "rte_ipv6_addr",
        "rte_pmd_bnxt_rep_role",
    ] {
        builder = builder.allowlist_type(name);
    }
    for name in [
        "rte_eal_cleanup",
        "rte_eal_init",
        "rte_eth_dev_adjust_nb_rx_tx_desc",
        "rte_eth_dev_close",
        "rte_eth_dev_configure",
        "rte_eth_dev_get_port_by_name",
        "rte_eth_dev_info_get",
        "rte_eth_dev_is_valid_port",
        "rte_eth_dev_socket_id",
        "rte_eth_dev_start",
        "rte_eth_dev_stop",
        "rte_eth_link_get_nowait",
        "rte_eth_macaddr_get",
        "rte_eth_rx_queue_setup",
        "rte_eth_tx_queue_setup",
        "rte_flow_create",
        "rte_flow_destroy",
        "rte_flow_group_set_miss_actions",
        "rte_flow_query",
        "rte_flow_validate",
        "rte_mempool_free",
        "rte_pktmbuf_pool_create",
        "rte_pmd_bnxt_rep_role_set",
        "rte_rand",
    ] {
        builder = builder.allowlist_function(name);
    }
    let flags = sdk_compiler_flags()?;
    builder = builder.clang_args(&flags);
    let mut compiler = cc::Build::new();
    compiler
        .file("src/dpdk_shim.c")
        .define("ALLOW_EXPERIMENTAL_API", None)
        .warnings(true)
        .extra_warnings(true)
        .flags(&flags)
        .flag_if_supported("-Werror");
    let generated = builder.generate()?.to_string();
    let mut aliases = String::new();
    for (original, renamed) in names
        .aliases
        .lock()
        .map_err(|error| io::Error::other(format!("bindgen alias map is unavailable: {error}")))?
        .iter()
    {
        if emitted_type(&generated, renamed) {
            aliases.push_str(&format!("pub use self::{renamed} as {original};\n"));
        }
    }
    fs::write(output.join("dpdk_bindings.rs"), generated + &aliases)?;
    compiler.compile("mwan_dpdk_shim");
    emit_sdk_linking(&sdk)?;
    write_rust_linking(output)?;
    Ok(())
}

fn emitted_type(source: &str, name: &str) -> bool {
    source.lines().any(|line| {
        ["pub struct ", "pub union ", "pub enum ", "pub type "]
            .iter()
            .filter_map(|prefix| line.trim_start().strip_prefix(prefix))
            .filter_map(|declaration| declaration.strip_prefix(name))
            .any(|suffix| {
                suffix
                    .chars()
                    .next()
                    .is_none_or(|value| !value.is_alphanumeric() && value != '_')
            })
    })
}

fn sdk_compiler_flags() -> Result<Vec<String>, Box<dyn Error>> {
    let program = env::var_os("PKG_CONFIG").unwrap_or_else(|| "pkg-config".into());
    let result = Command::new(program)
        .args(["--static", "--cflags", "libdpdk", "libdpdk = 26.03.0"])
        .output()?;
    if !result.status.success() {
        return Err(io::Error::other(format!(
            "pkg-config could not read the supplied SDK compiler flags: {}",
            String::from_utf8_lossy(&result.stderr)
        ))
        .into());
    }
    shlex::split(&String::from_utf8(result.stdout)?)
        .ok_or_else(|| io::Error::other("pkg-config returned malformed SDK compiler flags").into())
}

fn emit_sdk_linking(sdk: &pkg_config::Library) -> Result<(), Box<dyn Error>> {
    for path in &sdk.link_paths {
        println!("cargo:rustc-link-search=native={}", path.display());
    }
    let mut linked_dpdk = BTreeSet::new();
    for library in &sdk.libs {
        let archive_name = library
            .strip_prefix(":lib")
            .and_then(|name| name.strip_suffix(".a"));
        let name = archive_name.unwrap_or(library);
        if name.starts_with("rte_") {
            // pkg-config can list one archive under both naming forms. Rust
            // rejects repeated modifier directives for the normalized library.
            if !linked_dpdk.insert(name) {
                continue;
            }
            // Meson preserves PMD constructors when it combines the final archive.
            println!("cargo:rustc-link-lib=static:-bundle,+whole-archive={name}");
        } else if let Some(file) = library.strip_prefix(':') {
            println!("cargo:rustc-link-arg=-l:{file}");
        } else {
            println!("cargo:rustc-link-lib={name}");
        }
    }
    for file in &sdk.link_files {
        println!("cargo:rustc-link-arg={}", file.display());
    }
    Ok(())
}

fn write_rust_linking(output: &Path) -> Result<(), Box<dyn Error>> {
    println!("cargo:rerun-if-env-changed=MWAN_NATIVE_LINK_FLAGS_FILE");
    let Some(destination) = env::var_os("MWAN_NATIVE_LINK_FLAGS_FILE") else {
        return Ok(());
    };
    let source = output.join("native_link_probe.rs");
    fs::write(&source, "")?;
    let target = env::var("TARGET")?;
    let mut command = Command::new(env::var("RUSTC")?);
    command
        .arg(&source)
        .arg("--crate-name=mwan_native_link_probe")
        .arg("--crate-type=staticlib")
        .arg("--print=native-static-libs")
        .arg("--target")
        .arg(target)
        .arg("--out-dir")
        .arg(output);
    // Cargo compiles this script for the host. This separate compiler call
    // must retain the selected target's static runtime flags.
    if let Some(flags) = env::var_os("CARGO_ENCODED_RUSTFLAGS") {
        for flag in flags
            .to_string_lossy()
            .split('\u{1f}')
            .filter(|flag| !flag.is_empty())
        {
            command.arg(flag);
        }
    }
    let result = command.output()?;
    let diagnostics = String::from_utf8(result.stderr)?;
    if !result.status.success() {
        return Err(io::Error::other(format!(
            "rustc could not report native link libraries: {diagnostics}"
        ))
        .into());
    }
    let libraries = diagnostics
        .lines()
        .find_map(|line| line.strip_prefix("note: native-static-libs: "))
        .ok_or_else(|| {
            io::Error::other(format!(
                "rustc did not report native link libraries: {diagnostics}"
            ))
        })?;
    fs::write(destination, libraries)?;
    Ok(())
}
