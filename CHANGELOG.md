# envoy-control-plane changelog

02.04.2021
- Switched to envoy APIv3.
- Removed custom SDS support, because it is now included in go-control-plane.
- Updated go-control-plane to v0.9.7-0.20200713194620-000e06b258c1 (works with envoy 1.15.0 release)

26.02.2020
- Added SDS support / secrets management.
- Differentiated watched files from configuration data, so you can re-read configs atomically now, touching some trigger.
- Added prometheus metrics support (:2112/metrics)
- Code refactoring, to make it more understandable.
- Updated go-control-plane to v0.9.2-0.20191211232018-b5a1f4df970c (works with envoy 1.12.2 release). 
