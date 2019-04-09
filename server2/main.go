package main

import (
	"flag"
	"github.com/rcrowley/go-metrics"
	"github.com/smallnest/rpcx/log"
	"github.com/smallnest/rpcx/server"
	"github.com/smallnest/rpcx/serverplugin"
	"newMicro/server2/config"
	"newMicro/server2/factory"
	"time"
)



func main() {
	flag.Parse()

	s := server.NewServer()
	addRegistryPlugin(s)
	s.RegisterName("PrintService",factory.GetPrintService(), "")
	s.Serve("tcp", *config.Addr)
}
func addRegistryPlugin(s *server.Server) {

	r := &serverplugin.ConsulRegisterPlugin{
		ServiceAddress: "tcp@" + *config.Addr,
		ConsulServers:  []string{*config.ConsulAddr},
		BasePath:       *config.BasePath,
		Metrics:        metrics.NewRegistry(),
		UpdateInterval: time.Minute,
	}
	err := r.Start()
	if err != nil {
		log.Fatal(err)
	}
	s.Plugins.Add(r)
}
