project('mwan-native', 'c', version: '1.0.0', meson_version: '>=1.1.0', default_options: ['c_std=gnu11', 'warning_level=3', 'werror=true'])
if host_machine.system() != 'linux'
  error('The native worker requires Linux')
endif
dpdk = dependency('libdpdk', version: '=26.03.0', static: true)
source_hash = get_option('dpdk_source_sha256')
patch_hash = get_option('dpdk_patch_sha256')
if source_hash == '' or patch_hash == ''
  error('Both verified DPDK source hashes are required')
endif
fs = import('fs')
rust_archive = get_option('native_archive')
rust_link_flags_file = get_option('native_link_flags_file')
if rust_archive == '' or not fs.is_file(rust_archive)
  error('Build the Rust static library with make native-library or supply native_archive')
endif
if rust_link_flags_file == '' or not fs.is_file(rust_link_flags_file)
  error('Supply the rustc native link flags produced by make native-library')
endif
rust_link_flags = fs.read(rust_link_flags_file).strip().split()
includes = include_directories('include', 'src')
install_headers('include/mwan_native.h')

# One relocatable object preserves PMD registration constructors in the archive.
# cgo links that archive without accepting unrestricted linker flags.
pkgconfig = find_program('pkg-config')
static_flags = run_command(pkgconfig, '--static', '--libs', 'libdpdk', check: true).stdout().strip().split()
archive_flags = []
system_flags = []
dpdk_libraries = []
foreach flag : static_flags
  if flag.startswith('-L')
    archive_flags += flag
    system_flags += flag
  elif flag.startswith('-l:librte_') and flag.endswith('.a')
    name = flag.replace('-l:lib', '-l').replace('.a', '')
    if not dpdk_libraries.contains(name)
      dpdk_libraries += name
      archive_flags += flag
    endif
  elif flag.startswith('-lrte_')
    # Requires and Libs.private can identify the same archive twice.
    if not dpdk_libraries.contains(flag)
      dpdk_libraries += flag
      archive_flags += flag.replace('-lrte_', '-l:librte_') + '.a'
    endif
  elif flag.startswith('-Wl,')
    continue
  else
    system_flags += flag
  endif
endforeach
compiler = meson.get_compiler('c')
combined = custom_target('mwan_native_combined', input: rust_archive, output: 'mwan_native_combined.o',
  command: compiler.cmd_array() + ['-r', '-nostdlib', '-o', '@OUTPUT@', '-Wl,--whole-archive', '@INPUT@'] +
    archive_flags + ['-Wl,--no-whole-archive'])
native_library = static_library('mwan_native', objects: combined, install: true)
pc = configuration_data()
pc.set('PREFIX', get_option('prefix'))
pc.set('LIBDIR', join_paths(get_option('prefix'), get_option('libdir')))
pc.set('INCLUDEDIR', join_paths(get_option('prefix'), get_option('includedir')))
pc.set('LINK_ARGS', ' '.join(system_flags + rust_link_flags))
configure_file(input: 'mwan-native.pc.in', output: 'mwan-native.pc',
  configuration: pc, install_dir: join_paths(get_option('libdir'), 'pkgconfig'))

if get_option('hardware_test_driver')
  executable('mwan-native-hardware', 'tests/hardware.c',
    include_directories: includes, link_with: native_library,
    link_args: system_flags + rust_link_flags, install: false)
endif
