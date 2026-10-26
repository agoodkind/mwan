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
metadata = configuration_data()
metadata.set_quoted('MWAN_DPDK_SOURCE_SHA256', source_hash)
metadata.set_quoted('MWAN_DPDK_PATCH_SHA256', patch_hash)
configure_file(output: 'mwan_native_build.h', configuration: metadata)
includes = include_directories('include', 'src')
worker = static_library('mwan_native_core', 'src/worker.c', 'src/flow.c', 'src/packet.c',
  include_directories: includes, dependencies: dpdk,
  c_args: ['-DALLOW_EXPERIMENTAL_API'], install: false)
install_headers('include/mwan_native.h')

# One relocatable object preserves PMD registration constructors in the archive.
# cgo links that archive without accepting unrestricted linker flags.
pkgconfig = find_program('pkg-config')
static_flags = run_command(pkgconfig, '--static', '--libs', 'libdpdk', check: true).stdout().strip().split()
archive_flags = []
system_flags = []
foreach flag : static_flags
  if flag.startswith('-L')
    archive_flags += flag
    system_flags += flag
  elif flag.startswith('-l:librte_') and flag.endswith('.a')
    archive_flags += flag
  elif flag.startswith('-lrte_') or flag.startswith('-Wl,')
    continue
  else
    system_flags += flag
  endif
endforeach
compiler = meson.get_compiler('c')
combined = custom_target('mwan_native_combined', input: worker, output: 'mwan_native_combined.o',
  command: compiler.cmd_array() + ['-r', '-nostdlib', '-o', '@OUTPUT@', '-Wl,--whole-archive', '@INPUT@'] +
    archive_flags + ['-Wl,--no-whole-archive'])
static_library('mwan_native', objects: combined, install: true)
pc = configuration_data()
pc.set('PREFIX', get_option('prefix'))
pc.set('LIBDIR', join_paths(get_option('prefix'), get_option('libdir')))
pc.set('INCLUDEDIR', join_paths(get_option('prefix'), get_option('includedir')))
pc.set('LINK_ARGS', ' '.join(system_flags))
configure_file(input: 'mwan-native.pc.in', output: 'mwan-native.pc',
  configuration: pc, install_dir: join_paths(get_option('libdir'), 'pkgconfig'))

if get_option('hardware_test_driver')
  executable('mwan-native-hardware', 'tests/hardware.c',
    include_directories: includes, link_with: worker, dependencies: dpdk,
    c_args: ['-DALLOW_EXPERIMENTAL_API'], install: false)
endif
