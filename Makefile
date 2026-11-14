# Convenience targets. CI runs the gates in CONTRIBUTING.md directly.

FUZZTIME ?= 60

.PHONY: fuzz
# Run every fuzz target in turn for FUZZTIME seconds each (make fuzz FUZZTIME=300).
fuzz:
	scripts/fuzz.sh $(FUZZTIME)
