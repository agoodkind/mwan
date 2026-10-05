.DEFAULT_GOAL := help

define HELP_TEXT
Run make in a module directory.

  gateway/ contains the goodkind.io/mwan module.
  make -C gateway build compiles the mwan program.

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
