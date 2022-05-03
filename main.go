package main

/************************************************
 * Envoy control plane server
 * **********************************************
 * Based on Ambassador's Ambex ADS and massively
 * modified to extend its functionality.
 * **********************************************
 */

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/ioutil"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/fsnotify/fsnotify"
	"google.golang.org/grpc"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"

	log "github.com/sirupsen/logrus"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	clusterservice "github.com/envoyproxy/go-control-plane/envoy/service/cluster/v3"
	discoverygrpc "github.com/envoyproxy/go-control-plane/envoy/service/discovery/v3"
	endpointservice "github.com/envoyproxy/go-control-plane/envoy/service/endpoint/v3"
	listenerservice "github.com/envoyproxy/go-control-plane/envoy/service/listener/v3"
	routeservice "github.com/envoyproxy/go-control-plane/envoy/service/route/v3"
	runtimeservice "github.com/envoyproxy/go-control-plane/envoy/service/runtime/v3"
	secretservice "github.com/envoyproxy/go-control-plane/envoy/service/secret/v3"
	"github.com/envoyproxy/go-control-plane/pkg/server/v3"

	bootstrap "github.com/envoyproxy/go-control-plane/envoy/config/bootstrap/v3"
	cluster "github.com/envoyproxy/go-control-plane/envoy/config/cluster/v3"
	endpoint "github.com/envoyproxy/go-control-plane/envoy/config/endpoint/v3"
	listener "github.com/envoyproxy/go-control-plane/envoy/config/listener/v3"
	route "github.com/envoyproxy/go-control-plane/envoy/config/route/v3"
	secret "github.com/envoyproxy/go-control-plane/envoy/extensions/transport_sockets/tls/v3"
	runtime "github.com/envoyproxy/go-control-plane/envoy/service/runtime/v3"

	"github.com/envoyproxy/go-control-plane/pkg/cache/types"
	"github.com/envoyproxy/go-control-plane/pkg/cache/v3"
	"github.com/envoyproxy/go-control-plane/pkg/resource/v3"
	_ "github.com/envoyproxy/go-control-plane/pkg/wellknown"

	// envoy internal typed objects
	_ "github.com/envoyproxy/go-control-plane/envoy/config/accesslog/v3"
	_ "github.com/envoyproxy/go-control-plane/envoy/config/overload/v3"
	_ "github.com/envoyproxy/go-control-plane/envoy/extensions/access_loggers/file/v3"
	_ "github.com/envoyproxy/go-control-plane/envoy/extensions/compression/gzip/compressor/v3"
	_ "github.com/envoyproxy/go-control-plane/envoy/extensions/compression/gzip/decompressor/v3"
	_ "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/http/adaptive_concurrency/v3"
	_ "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/http/buffer/v3"
	_ "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/http/compressor/v3"
	_ "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/http/cors/v3"
	_ "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/http/csrf/v3"
	_ "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/http/decompressor/v3"
	_ "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/http/ext_authz/v3"
	_ "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/http/fault/v3"
	_ "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/http/gzip/v3"
	_ "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/http/header_to_metadata/v3"
	_ "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/http/health_check/v3"
	_ "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/http/ip_tagging/v3"
	_ "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/http/lua/v3"
	_ "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/http/original_src/v3"
	_ "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/http/ratelimit/v3"
	_ "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/http/rbac/v3"
	_ "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/http/router/v3"
	_ "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/listener/http_inspector/v3"
	_ "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/listener/original_dst/v3"
	_ "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/listener/original_src/v3"
	_ "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/listener/proxy_protocol/v3"
	_ "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/listener/tls_inspector/v3"
	_ "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/network/direct_response/v3"
	_ "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/network/ext_authz/v3"
	_ "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/network/http_connection_manager/v3"
	_ "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/network/rbac/v3"
	_ "github.com/envoyproxy/go-control-plane/envoy/extensions/retry/priority/previous_priorities/v3"
	_ "github.com/envoyproxy/go-control-plane/envoy/extensions/transport_sockets/tls/v3"
	_ "github.com/envoyproxy/go-control-plane/envoy/extensions/upstreams/http/v3"
)

var (
	ads                 bool
	debug               bool
	metricsServerAddr   string
	nodeID              string
	port                uint
	watchedDirs         string
)

var (
	snapshotCreateFailures = promauto.NewCounter(prometheus.CounterOpts{
		Name: "ecp_snapshot_create_failures_total",
		Help: "Total number of failed snapshot creations.",
	})
	snapshotTransparentVersion = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "ecp_snapshot_transparent_version",
		Help: "Version of configuration snapshot, taken from data storage.",
	})
)

func init() {
	flag.BoolVar(&ads, "ads", false, "Enable ads mode (default: false, means we are using xds)")
	flag.BoolVar(&debug, "debug", false, "Use debug logging (default: false)")
	flag.StringVar(&nodeID, "nodeID", "test-id", "Node ID")
	flag.UintVar(&port, "port", 18000, "Management server port.")
	flag.StringVar(&watchedDirs, "watch", "", "Dirs to watch for changes (default: current directory)")
	flag.StringVar(&metricsServerAddr, "metricsServerAddr", ":2112",
		"Address:port for prometheus metrics endpoint")
}

// This feels kinda dumb. ¯\_(ツ)_/¯
type logger struct{}

func (logger logger) Infof(format string, args ...interface{}) {
	log.Infof(format, args...)
}
func (logger logger) Warnf(format string, args ...interface{}) {
	log.Warnf(format, args...)
}
func (logger logger) Errorf(format string, args ...interface{}) {
	log.Errorf(format, args...)
}
func (logger logger) Debugf(format string, args ...interface{}) {
	log.Debugf(format, args...)
}
// end of logger stuff

const (
	// gRPC golang library sets a very small upper bound for the number gRPC/h2
	// streams over a single TCP connection. If a proxy multiplexes requests over
	// a single connection to the management server, then it might lead to
	// availability problems.
	grpcMaxConcurrentStreams = 1000000
	// Keepalive timeouts based on connection_keepalive parameter
	// https://www.envoyproxy.io/docs/envoy/latest/configuration/overview/examples#dynamic
	grpcKeepaliveTime        = 30 * time.Second
	grpcKeepaliveTimeout     = 5 * time.Second
	grpcKeepaliveMinTime     = 30 * time.Second
)

func registerServer(grpcServer *grpc.Server, server server.Server) {
	discoverygrpc.RegisterAggregatedDiscoveryServiceServer(grpcServer, server)
	endpointservice.RegisterEndpointDiscoveryServiceServer(grpcServer, server)
	clusterservice.RegisterClusterDiscoveryServiceServer(grpcServer, server)
	routeservice.RegisterRouteDiscoveryServiceServer(grpcServer, server)
	listenerservice.RegisterListenerDiscoveryServiceServer(grpcServer, server)
	secretservice.RegisterSecretDiscoveryServiceServer(grpcServer, server)
	runtimeservice.RegisterRuntimeDiscoveryServiceServer(grpcServer, server)
}

// runManagementServer starts an xDS server at the given port.
func runManagementServer(ctx context.Context, srv server.Server, port uint) {

	var grpcOptions []grpc.ServerOption
	grpcOptions = append(grpcOptions,
		grpc.MaxConcurrentStreams(grpcMaxConcurrentStreams),
		grpc.KeepaliveParams(keepalive.ServerParameters{
			Time:    grpcKeepaliveTime,
			Timeout: grpcKeepaliveTimeout,
		}),
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{
			MinTime:             grpcKeepaliveMinTime,
			PermitWithoutStream: true,
		}),
	)
	grpcOptions = append(grpcOptions, grpc.MaxConcurrentStreams(grpcMaxConcurrentStreams))
	grpcServer := grpc.NewServer(grpcOptions...)

	lis, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		log.WithError(err).Fatal("failed to listen")
	}

	registerServer(grpcServer, srv)

	log.WithFields(log.Fields{"port": port}).Info("Listening at")

	go func() {
		go func() {
			if err = grpcServer.Serve(lis); err != nil {
				log.WithFields(log.Fields{"error": err}).Error("Management server exited.")
			}
		}()
		<-ctx.Done()
		grpcServer.GracefulStop()
	}()

}

// runMetricsServer starts /metrics endpoint at metricsServerAddr
func runMetricsServer (ctx context.Context, metricsServerAddr string) {
	go func() {
		go func() {
			// Handle function add handler to DefaultServeMux.
			http.Handle("/metrics", promhttp.Handler())
			// ListenAndServe starts an HTTP server with a given address and handler.
			// The handler is usually nil, which means to use DefaultServeMux.
			err := http.ListenAndServe(metricsServerAddr, nil)
			if err != nil {
				log.WithFields(log.Fields{"error": err}).Error("Metrics server exited.")
			}
		}()
		<-ctx.Done()
	}()
}

// Decoders for unmarshalling config, that ambex was fed with.
// They can be in JSON or protobuf format.
var decoders = map[string]func([]byte, proto.Message) error{
	".json": protojson.Unmarshal,
	//".pb":   proto.Unmarshal,
}


// Check if we have supported decoder for our data files (by their extension, lol)
func isDecodable(name string) bool {
	// Filter out dot-files
	if strings.HasPrefix(name, ".") {
		return false
	}
	// We can decode only files with extensions, listed in decoders struct (above)
	ext := filepath.Ext(name)
	_, ok := decoders[ext] // this line sets 'ok' value, based on 'decoders' struct match
	return ok
}

// Not sure if there is a better way to do this, but we cast to this
// so we can call the generated Validate method (c) ambex devs
type Validatable interface {
	proto.Message
	Validate() error
}

func getConfigVersion(name string) (int, error) {
	fd, err := os.Open(name)
	if err != nil {
		log.Errorf("Error opening file %s: %v", name, err)
		return -1, err
	}
	defer fd.Close()

	scanner := bufio.NewScanner(fd)
	if !scanner.Scan() {
		log.Errorf("Scanner error for file: %v", name)
		return -1, err
	}
	contentInt, err := strconv.Atoi(scanner.Text())
	if err != nil {
		log.Errorf("Error converting snapshot version to Int: %v", err)
		return -1, err
	}

	log.Debugf("Got snapshot version %d", contentInt)
	return contentInt, nil
}

// Decoding our config into proto.Message objects
func DecodeConfigFile(name string) (proto.Message, error) {
	var decodedContents anypb.Any

	// Reading config file with data
	contents, err := ioutil.ReadFile(name)
	if err != nil {
		log.Errorf("Error reading file: %s ", name)
		return nil, err
	}

	// Decoding data given.
	ext := filepath.Ext(name)
	decoder := decoders[ext]
	err = decoder(contents, &decodedContents)
	if err != nil {
		log.Errorf("Decoder error for file: %s ", name)
		return nil, err
	}

	// Casting protobuf and custom envoy api objects stuff
	m, err := decodedContents.UnmarshalNew()
	if err != nil {
		log.Errorf("Error UnmarshalNew in file %s: %v", name, err)
		return nil, err
	}

	var v, ok = m.(Validatable)
	if !ok {
		var castingErrorText = "error casting message to Validatable() interface"
		log.Errorf("%v", castingErrorText)
		return nil, errors.New(castingErrorText)
	}

	err = v.Validate()
	if err != nil {
		log.Errorf("Error validating file: %s ", name)
		return nil, err
	}
	log.Infof("Loaded file %s", name)
	return v, nil
}

func cloneResource(src proto.Message) proto.Message {
	in := reflect.ValueOf(src)
	if in.IsNil() {
		return src
	}
	out := reflect.New(in.Type().Elem())
	dst := out.Interface().(proto.Message)
	mergeResource(dst, src)
	return dst
}
func mergeResource(to, from proto.Message) {
	fromInBytes, err := protojson.Marshal(from)
	if err != nil {
		panic(err)
	}
	err = protojson.Unmarshal(fromInBytes, to)
	if err != nil {
		panic(err)
	}
}

func createResourcesFromConfig(config cache.SnapshotCache, snapshotVersion *int, dirs []string) {
	var clusters []types.Resource
	var endpoints []types.Resource
	var routes []types.Resource
	var listeners []types.Resource
	var secrets []types.Resource
	var runtimes []types.Resource
	var scopedroutes []types.Resource
	var extensionconfigs []types.Resource

	// Configs on disk to get our resources from.
	var filenames []string

	for _, dir := range dirs {
		files, err := ioutil.ReadDir(dir)
		if err != nil {
			log.WithError(err).Warnf("Error listing %v", dir)
			continue
		}
		for _, file := range files {
			name := file.Name()
			if isDecodable(name) {
				filenames = append(filenames, filepath.Join(dir, name))
			}
			if name == "snapshot_version" {
				*snapshotVersion, err = getConfigVersion(filepath.Join(dir, name))
				if err != nil {
					log.WithError(err).Warnf("Error getting snapshot version from file %v", name)
				}
			}
		}
	}

	for _, name := range filenames {
		decodedObjects, err := DecodeConfigFile(name)
		if err != nil {
			log.Warnf("Error decoding data in %s: %v", name, err)
			continue
		}

		// Array of the resulting objects:
		var outObjects *[]types.Resource

		// Objects' type selector
		switch decodedObjects.(type) {
		case *cluster.Cluster:
			outObjects = &clusters
		case *endpoint.ClusterLoadAssignment:
			outObjects = &endpoints
		case *route.RouteConfiguration:
			outObjects = &routes
		case *listener.Listener:
			outObjects = &listeners
		case *secret.Secret:
			outObjects = &secrets
		case *runtime.Runtime:
			outObjects = &runtimes
		case *bootstrap.Bootstrap: // static configuration
			bootstrapResource := decodedObjects.(*bootstrap.Bootstrap)
			staticResource := bootstrapResource.StaticResources
			for _, listenerResource := range staticResource.Listeners {
				// This cloning and clusters' one below are very similar to proto.Clone(),
				// but we don't know for sure why authors use their own implementation.
				listeners = append(listeners, cloneResource(listenerResource).(types.Resource))
			}
			for _, clusterResource := range staticResource.Clusters {
				clusters = append(clusters, cloneResource(clusterResource).(types.Resource))
			}
			continue
		default:
			log.Warnf("Unrecognized resource: %s", name)
			continue
		}

		// I have no idea why does ambex need this list, maybe for config dump?
		// (however, it is not implemented anywhere around)
		// todo: make viewable config dump
		*outObjects = append(*outObjects, decodedObjects.(types.Resource))
	}

	version := fmt.Sprintf("v%d", *snapshotVersion)
	snapshot, _ := cache.NewSnapshot(version, map[resource.Type][]types.Resource{
		resource.EndpointType:        endpoints,
		resource.ClusterType:         clusters,
		resource.RouteType:           routes,
		resource.ScopedRouteType:     scopedroutes,
		resource.ListenerType:        listeners,
		resource.RuntimeType:         runtimes,
		resource.SecretType:          secrets,
		resource.ExtensionConfigType: extensionconfigs,
	})

	// Consistent check verifies that the dependent resources are exactly listed in the
	// snapshot:
	// - all EDS resources are listed by name in CDS resources
	// - all RDS resources are listed by name in LDS resources
	//
	// Note that clusters and listeners are requested without name references, so
	// Envoy will accept the snapshot list of clusters as-is even if it does not match
	// all references found in xDS.
	err := snapshot.Consistent()
	if err != nil {
		snapshotCreateFailures.Inc()
		log.Errorf("Snapshot inconsistency. Error: %+v\n", err)
		if debug {
			// Trying to print pretty snapshot json
			prettySnapshot, errMarshalIndent := json.MarshalIndent(snapshot, "", "\t")
			if errMarshalIndent != nil {
				log.Debugf("Error making pretty snapshot: %+v\n", errMarshalIndent)
				log.Debugf("Failed snapshot: %+v\n", snapshot)
			} else {
				log.Debugf("Failed snapshot: %+v\n", prettySnapshot)
			}
		}
	} else {
		if err := config.SetSnapshot(context.Background(), nodeID, snapshot); err != nil {
			log.Fatalf("Fatal error while trying to set snapshot. Error: %q. Snapshot: %+v\n", err, snapshot)
		} else {
			f := float64(*snapshotVersion)
			snapshotTransparentVersion.Set(f)
			log.Infof("Pushing new snapshot %+v", *snapshotVersion)
		}
	}

}

// OnStreamOpen is called once an xDS stream is open with a stream ID and the type URL (or "" for ADS).
func (logger logger) OnStreamOpen(_ context.Context, id int64, typ string) error {
	if debug {
		log.Debugf("Stream[%d] is open for '%s' (can be empty for ADS).\n", id, typ)
	}
	return nil
}

// OnStreamClosed is called immediately prior to closing an xDS stream with a stream ID.
func (logger logger) OnStreamClosed(id int64) {
	if debug {
		log.Debugf("Stream[%d] is closed.\n", id)
	}
}

// OnStreamRequest is called once a request is received on a stream.
func (logger logger) OnStreamRequest(id int64, req *discoverygrpc.DiscoveryRequest) error {
	log.Infof("Stream[%d] request: %v", id, req)
	return nil
}

// OnStreamResponse is called immediately prior to sending a response on a stream.
func (logger logger) OnStreamResponse(id int64, req *discoverygrpc.DiscoveryRequest,
	res *discoverygrpc.DiscoveryResponse) {
	if debug {
		log.Debugf("Stream[%d] response: %v -> %v", id, req, res)
	} else {
		log.Infof("Stream[%d] response for request: %v", id, req)
	}
}

// OnFetchRequest is called for each Fetch request
func (logger logger) OnFetchRequest(_ context.Context, req *discoverygrpc.DiscoveryRequest) error {
	log.Infof("Fetch request: %v", req)
	return nil
}

// OnFetchResponse is called immediately prior to sending a response.
func (logger logger) OnFetchResponse(req *discoverygrpc.DiscoveryRequest, res *discoverygrpc.DiscoveryResponse) {
	if debug {
		log.Debugf("Fetch response: %v -> %v", req, res)
	} else {
		log.Infof("Fetch response for request: %v", req)
	}
}

func main() {
	// Parsing binary arguments
	flag.Parse()

	if debug {
		log.SetLevel(log.DebugLevel)
	}

	log.Info("Envoy control plane is starting...")

	// Get dirs with configuration for ambex. They are not watched, unless specified in 'watcherDirs' arg.
	// If not specified, looking in current directory.
	ambexDataConfigs := flag.Args()
	if len(ambexDataConfigs) == 0 {
		ambexDataConfigs = []string{"."}
	}

	// Data files watcher stuff
	watcher, err := fsnotify.NewWatcher(); if err != nil {
		log.WithError(err).Fatal()
	}
	defer watcher.Close()

	if watchedDirs != "" {
		watchedDirsArray := []string{watchedDirs}
		for _, watchedDir := range watchedDirsArray {
			err = watcher.Add(watchedDir)
			if err != nil {
				log.WithError(err).Fatal()
			}
		}
	}
	// end of watcher stuff

	ch := make(chan os.Signal)
	signal.Notify(ch, syscall.SIGHUP, os.Interrupt, syscall.SIGTERM)

	// Running in background context with delayed cancel function
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Create a cache for new configuration snapshot (config).
	// ADS flag (when set to true) forces a delay in responding to streaming requests until all
	// resources are explicitly named in the request. This avoids the problem of a
	// partial request over a single stream for a subset of resources which would
	// require generating a fresh version for acknowledgement. ADS flag requires
	// snapshot consistency. For non-ADS case (and fetch), mutliple partial
	// requests are sent across multiple streams and re-using the snapshot version
	// is OK.
	config := cache.NewSnapshotCache(ads, cache.IDHash{}, logger{})

	// It seems, that we can create snaps for different node names
	if _, err := config.GetSnapshot(nodeID); err == nil {
		log.Errorf("Unexpected snapshot found for key %q.", nodeID)
	}

	// Create and run our management server
	srv := server.NewServer(ctx, config, nil)
	runManagementServer(ctx, srv, port)
	runMetricsServer(ctx, metricsServerAddr)

	pid := os.Getpid()
	file := "ecp.pid"
	if err := ioutil.WriteFile(file, []byte(fmt.Sprintf("%v", pid)), 0644); err != nil {
		log.Warnf("Cannot write PID-file %s for pid %s.", file, pid)
	} else {
		log.WithFields(log.Fields{"pid": pid, "file": file}).Info("Wrote PID")
	}

	snapshotVersion := 0

	// Read our data files and convert it to objects
	createResourcesFromConfig(config, &snapshotVersion, ambexDataConfigs)

OUTER:
	for {
		select {
		case sig := <-ch:
			switch sig {
			case syscall.SIGHUP: // reload objects from data files on SIGHUP
				log.Infof("Got SIGHUP. Dirs: %+v",ambexDataConfigs)
				createResourcesFromConfig(config, &snapshotVersion, ambexDataConfigs)
			case os.Interrupt, syscall.SIGTERM: // exit on SIGTERM
				break OUTER
			}
		case event := <-watcher.Events:
			log.Infof("Got watcher event: %s: %s", event.Op, event.Name)
			createResourcesFromConfig(config, &snapshotVersion, ambexDataConfigs)
		case err := <-watcher.Errors:
			log.WithError(err).Warn("Watcher error.")
		}
	}

	log.Info("Management server exited.")

}
