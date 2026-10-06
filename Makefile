.DEFAULT_GOAL := help

define HELP_TEXT
Run make in a module directory.

  gateway/ contains the goodkind.io/mwan module.
  Run make -C gateway build to compile the mwan program.
  provider/ contains the goodkind.io/mwan/provider module.
  Run make -C provider build to compile the OpenTofu provider.

Run make -C gateway help to list its targets.
endef
export HELP_TEXT

.PHONY: help
help:
	@printf '%s\n' "$$HELP_TEXT"
	@exit 1

%:
	@printf '%s\n' "$$HELP_TEXT"
	@exit 1
