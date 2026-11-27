# Docker includes this target with the upstream OVS Makefile for the parser
# stage. It prepares generated sources before building the library for mwan.

.PHONY: mwan-parser
mwan-parser: $(BUILT_SOURCES)
	$(MAKE) lib/libopenvswitch.la
