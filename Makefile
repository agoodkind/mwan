.DEFAULT_GOAL := help

define HELP_TEXT
This repository has no root build. Run make inside a module directory.

  gateway/   The mwan gateway program, Go module goodkind.io/mwan.

Each directory has its own Makefile and its own gates.
Example: make -C gateway check test
endef
export HELP_TEXT

.PHONY: help
help:
	@printf '%s\n' "$$HELP_TEXT"
	@exit 1

%:
	@printf '%s\n' "$$HELP_TEXT"
	@exit 1
