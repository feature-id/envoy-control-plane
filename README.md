# envoy-control-plane

This is XDS/ADS server for envoy (envoyproxy.io) management, written in Go (uses envoyproxy/go-control-plane under the hood).

Based on Ambassador's Ambex ADS (datawire/ambex) and massively modified to extend its functionality (see CHANGELOG.md
for the list of modifications).

This control plane does not rely on kube objects, it just reads envoy configuration from json/protobuf files and watches
them (or any other file-trigger) for changes. Such configuration can be used when envoy is running in front of more than
one kubernetes cluster or if you just have some legacy VMs/baremetals as backends.

## quick start
```
$ go build .
$ ./envoy-control-plane -h
Usage of ./envoy-control-plane:
  -ads
        Use ads mode. (default: false, means we are using xds)
  -debug
        Use debug logging.
  -metricsServerAddr string
        Address and port for metrics endpoint. (default ":2112")
  -nodeID string
        Node ID. (default "test-id")
  -port uint
        Management server port. (default 18000)
  -watch string
        Dirs to watch for changes.
```
