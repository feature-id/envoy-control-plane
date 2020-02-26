# envoy-control-plane changelog

Added SDS support / secrets management.
Differentiated watched files from configuration data, so you can re-read configs atomically now, touching some trigger.
Added prometheus metrics support (:2112/metrics)
Code refactoring, to make it more understandable.
Integrated with newer go-control-plane and envoy (checked out at 11.10.2019 right after envoy 1.12.2 release). 