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

	"github.com/fsnotify/fsnotify"
	"google.golang.org/grpc"

	log "github.com/sirupsen/logrus"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	v2 "github.com/envoyproxy/go-control-plane/envoy/api/v2"
	auth "github.com/envoyproxy/go-control-plane/envoy/api/v2/auth"
	bootstrap "github.com/envoyproxy/go-control-plane/envoy/config/bootstrap/v2"
	discovery "github.com/envoyproxy/go-control-plane/envoy/service/discovery/v2"
	cache "github.com/envoyproxy/go-control-plane/pkg/cache"
	xds "github.com/envoyproxy/go-control-plane/pkg/server"

	"github.com/golang/protobuf/jsonpb"
	"github.com/golang/protobuf/proto"
	"github.com/golang/protobuf/ptypes"
	"github.com/golang/protobuf/ptypes/any"

	// envoy internal typed objects
	_ "github.com/envoyproxy/go-control-plane/envoy/config/accesslog/v2"
	_ "github.com/envoyproxy/go-control-plane/envoy/config/filter/http/lua/v2"
	_ "github.com/envoyproxy/go-control-plane/envoy/config/filter/http/rbac/v2"
)

var (
	ads                 bool
	debug               bool
	metricsServerAddr   string
	nodeID              string
	port                uint
	watchedDirs         string

	Version = "-no-version-" // Version is inserted at build using --ldflags -X
)

var (
	snapshotCreateFailures = promauto.NewCounter(prometheus.CounterOpts{
		Name: "ecp_snapshot_create_failures_total",
		Help: "Total number of failed snapshot creations.",
	})
	snapshotTransparentVersion = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "ecp_snapshot_transparent_version",
		Help: "Configuration snapshot version, taken from data configs.",
	})
)

func init() {
	flag.BoolVar(&ads, "ads", false, "Use ads mode (default: false, means we are using xds).")
	flag.BoolVar(&debug, "debug", false, "Use debug logging (default: false).")
	flag.StringVar(&nodeID, "nodeID", "test-id", "Node ID (default: test-id).")
	flag.UintVar(&port, "port", 18000, "Management server port (default: 1800).")
	flag.StringVar(&watchedDirs, "watch", "", "Dirs to watch for changes (default: '').")
	flag.StringVar(&metricsServerAddr, "metricsServerAddr", ":2112", "Address and port for metrics endpoint (default: ':2112').")
}

// This feels kinda dumb.
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

// RunManagementServer starts an xDS server at the given port.
func runManagementServer(ctx context.Context, srv xds.Server, port uint) {
	// gRPC golang library sets a very small upper bound for the number gRPC/h2
	// streams over a single TCP connection. If a proxy multiplexes requests over
	// a single connection to the management server, then it might lead to
	// availability problems.
	const grpcMaxConcurrentStreams = 1000000
	var grpcOptions []grpc.ServerOption
	grpcOptions = append(grpcOptions, grpc.MaxConcurrentStreams(grpcMaxConcurrentStreams))
	grpcServer := grpc.NewServer(grpcOptions...)

	lis, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		log.WithError(err).Fatal("failed to listen")
	}

	// register services
	discovery.RegisterAggregatedDiscoveryServiceServer(grpcServer, srv)
	v2.RegisterEndpointDiscoveryServiceServer(grpcServer, srv)
	v2.RegisterClusterDiscoveryServiceServer(grpcServer, srv)
	v2.RegisterRouteDiscoveryServiceServer(grpcServer, srv)
	v2.RegisterListenerDiscoveryServiceServer(grpcServer, srv)
	discovery.RegisterSecretDiscoveryServiceServer(grpcServer, srv)
	discovery.RegisterRuntimeDiscoveryServiceServer(grpcServer, srv)

	log.WithFields(log.Fields{"port": port}).Info("Listening")
	go func() {
		go func() {
			err := grpcServer.Serve(lis)

			if err != nil {
				log.WithFields(log.Fields{"error": err}).Error("Management server exited.")
			}
		}()

		<-ctx.Done()
		grpcServer.GracefulStop()
	}()

}

// Start /metrics endpoint
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
var decoders = map[string]func(string, proto.Message) error{
	".json": jsonpb.UnmarshalString,
	".pb":   proto.UnmarshalText,
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
	defer fd.Close()
	if err != nil {
		log.Errorf("Error opening file %s: %v", name, err)
		return -1, err
	}

	scanner := bufio.NewScanner(fd)
	if !scanner.Scan() {
		log.Errorf("Scanner error for file: %v", name)
		return -1, err
	}
	contentInt, err := strconv.Atoi(scanner.Text())
	if err != nil {
		log.Errorf("Error converting snapshot version to Int.", err)
		return -1, err
	}

	log.Debugf("Got snapshot version %s", contentInt)
	return contentInt, nil
}

// Decoding our config into proto.Message objects
func DecodeConfigFile(name string) (proto.Message, error) {
	localAny := &any.Any{}

	// Reading config file with data
	contents, err := ioutil.ReadFile(name)
	if err != nil {
		log.Errorf("Error reading file: %s ", name)
		return nil, err
	}

	// Decoding data given.
	ext := filepath.Ext(name)
	decoder := decoders[ext]
	err = decoder(string(contents), localAny)
	if err != nil {
		log.Errorf("Decoder error for file: %s ", name)
		return nil, err
	}

	// Casting protobuf and custom envoy api objects stuff
	var m ptypes.DynamicAny
	err = ptypes.UnmarshalAny(localAny, &m)
	if err != nil {
		log.Errorf("Error UnmarshalAny in file: %s", name)
		return nil, err
	}

	var v = m.Message.(Validatable)

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
	str, err := (&jsonpb.Marshaler{}).MarshalToString(from)
	if err != nil {
		panic(err)
	}
	err = jsonpb.UnmarshalString(str, to)
	if err != nil {
		panic(err)
	}
}

func createResourcesFromConfig(config cache.SnapshotCache, snapshotVersion *int, dirs []string) {
	clusters := []cache.Resource{}  // v2.Cluster
	endpoints := []cache.Resource{} // v2.ClusterLoadAssignment
	routes := []cache.Resource{}    // v2.RouteConfiguration
	listeners := []cache.Resource{} // v2.Listener
	secrets := []cache.Resource{} // auth.Secret
	runtimes := []cache.Resource{} // discovery.Runtime

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
		var outObjects *[]cache.Resource

		// Objects' type selector
		switch decodedObjects.(type) {
		case *v2.Cluster:
			outObjects = &clusters
		case *v2.ClusterLoadAssignment:
			outObjects = &endpoints
		case *v2.RouteConfiguration:
			outObjects = &routes
		case *v2.Listener:
			outObjects = &listeners
		case *auth.Secret:
			outObjects = &secrets
		case *discovery.Runtime:
			outObjects = &runtimes
		case *bootstrap.Bootstrap: // static configuration
			bootstrapResource := decodedObjects.(*bootstrap.Bootstrap)
			staticResource := bootstrapResource.StaticResources
			for _, listener := range staticResource.Listeners {
				listeners = append(listeners, cloneResource(listener).(cache.Resource))
			}
			for _, cluster := range staticResource.Clusters {
				clusters = append(clusters, cloneResource(cluster).(cache.Resource))
			}
			continue
		default:
			log.Warnf("Unrecognized resource %s: %v", name, err)
			continue
		}

		// to do: config_dump
		*outObjects = append(*outObjects, decodedObjects.(cache.Resource))
	}

	version := fmt.Sprintf("v%d", *snapshotVersion)
	snapshot := cache.NewSnapshot(version, endpoints, clusters, routes, listeners, runtimes)

	if secrets != nil {
		snapshot.Resources[cache.Secret] = cache.NewResources(version, secrets)
	} else {
		log.Debugf("No secrets given. %+v", secrets)
	}

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
		log.Errorf("Snapshot inconsistency: %+v\n", snapshot)
	} else { // this looks like if-spaghetti
		if err := config.SetSnapshot(nodeID, snapshot); err != nil {
			log.Fatalf("Fatal error while trying to set snapshot. Error: %q. Snapshot: %+v\n", err, snapshot)
		} else {
			f := float64(*snapshotVersion)
			snapshotTransparentVersion.Set(f)
			log.Infof("Pushing new snapshot %+v", *snapshotVersion)
		}
	}

}

// OnStreamOpen is called once an xDS stream is open with a stream ID and the type URL (or "" for ADS).
func (logger logger) OnStreamOpen(_ context.Context, id int64, stype string) error {
	if debug {
		log.Debugf("Stream[%d] is open for '%s' (can be empty for ADS).\n", id, stype)
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
func (logger logger) OnStreamRequest(id int64, req *v2.DiscoveryRequest) error {
	log.Infof("Stream[%d] request: %v", id, req)
	return nil
}

// OnStreamResponse is called immediately prior to sending a response on a stream.
func (logger logger) OnStreamResponse(id int64, req *v2.DiscoveryRequest, res *v2.DiscoveryResponse) {
	if debug {
		log.Debugf("Stream[%d] response: %v -> %v", id, req, res)
	} else {
		log.Infof("Stream[%d] response for request: %v", id, req)
	}
}

// OnFetchRequest is called for each Fetch request
func (logger logger) OnFetchRequest(_ context.Context, req *v2.DiscoveryRequest) error {
	log.Infof("Fetch request: %v", req)
	return nil
}

// OnFetchResponse is called immediately prior to sending a response.
func (logger logger) OnFetchResponse(req *v2.DiscoveryRequest, res *v2.DiscoveryResponse) {
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

	log.Infof("Envoy control plane (version: %s) is starting...", Version)

	// Get dirs with data configuration. They are not watched, unless specified in 'watcherDirs' arg.
	// If not specified, looking in current directory.
	dataConfigs := flag.Args()
	if len(dataConfigs) == 0 {
		dataConfigs = []string{"."}
	}

	// Data files watcher stuff
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
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
	srv := xds.NewServer(ctx, config, logger{})
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
	createResourcesFromConfig(config, &snapshotVersion, dataConfigs)

OUTER:
	for {
		select {
		case sig := <-ch:
			switch sig {
			case syscall.SIGHUP: // reload objects from data files on SIGHUP
				log.Infof("Got SIGHUP. Dirs: %+v",dataConfigs)
				createResourcesFromConfig(config, &snapshotVersion, dataConfigs)
			case os.Interrupt, syscall.SIGTERM: // exit on SIGTERM
				break OUTER
			}
		case event := <-watcher.Events:
			log.Infof("Got watcher event: %s: %s", event.Op, event.Name)
			createResourcesFromConfig(config, &snapshotVersion, dataConfigs)
		case err := <-watcher.Errors:
			log.WithError(err).Warn("Watcher error.")
		}
	}

	log.Info("Envoy control plane exited.")

}
