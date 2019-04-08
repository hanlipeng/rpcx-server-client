package main

import (
	"flag"
	"github.com/rcrowley/go-metrics"
	"github.com/smallnest/rpcx/log"
	"github.com/smallnest/rpcx/server"
	"github.com/smallnest/rpcx/serverplugin"
	"newMicro/server2/factory"
	"time"
)

var (
	addr = flag.String("addr", "localhost:8081", "server address")
	consulAddr = flag.String("consulAddr", "localhost:8500", "consul address")
	basePath   = flag.String("server2", "/server2", "prefix path")
)

func main() {
	flag.Parse()

	s := server.NewServer()
	addRegistryPlugin(s)
	s.RegisterName("PrintService",factory.GetPrintService(), "")
	s.Serve("tcp", *addr)
}
func addRegistryPlugin(s *server.Server) {

	r := &serverplugin.ConsulRegisterPlugin{
		ServiceAddress: "tcp@" + *addr,
		ConsulServers:  []string{*consulAddr},
		BasePath:       *basePath,
		Metrics:        metrics.NewRegistry(),
		UpdateInterval: time.Minute,
	}
	err := r.Start()
	if err != nil {
		log.Fatal(err)
	}
	s.Plugins.Add(r)
}
