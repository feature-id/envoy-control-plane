# envoy-control-plane

This is XDS/ADS server for envoy (envoyproxy.io) management, written in Go (uses envoyproxy/go-control-plane under the hood).
Based on Ambassador's Ambex ADS (datawire/ambex) and massively modified to extend its functionality.

See CHANGELOG.md for the list of modifications.

## quick start
```
$ go build .
$ ./envoy-control-plane -h
Usage of ./envoy-control-plane:
  -ads
    	Use ads mode (default: false, means we are using xds).
  -debug
    	Use debug logging (default: false).
  -metricsServerAddr string
    	Address and port for metrics endpoint (default: ':2112'). (default ":2112")
  -nodeID string
    	Node ID (default: test-id). (default "test-id")
  -port uint
    	Management server port (default: 1800). (default 18000)
  -watch string
    	Dirs to watch for changes (default: '').
```
