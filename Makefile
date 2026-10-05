.DEFAULT_GOAL := help

define HELP_TEXT
Run make inside a module directory. The repository root does not build anything.

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
