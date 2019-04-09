package main

import (
	"errors"
	"flag"
	gateway "github.com/rpcx-ecosystem/rpcx-gateway"
	"github.com/smallnest/rpcx/client"
	"log"
	"strings"
)

var (
	addr       = flag.String("addr", ":9981", "http server address")
	st         = flag.String("st", "http1", "server type: http1 or h2c")
	registry   = flag.String("consulAddr", "consul://10.0.0.5:8500", "consul address")
	basePath   = flag.String("basepath", "/server", "basepath for zookeeper, etcd and consul")
	failmode   = flag.Int("failmode", int(client.Failover), "failMode, Failover in default")
	selectMode = flag.Int("selectmode", int(client.RoundRobin), "selectMode, RoundRobin in default")
)

func main() {
	flag.Parse()

	d, err := createServiceDiscovery(*registry)
	if err != nil {
		log.Fatal(err)
	}
	gw := gateway.NewGateway(*addr, gateway.ServerType(*st), d, client.FailMode(*failmode), client.SelectMode(*selectMode), client.DefaultOption)

	gw.Serve()
}

func createServiceDiscovery(regAddr string) (client.ServiceDiscovery, error) {
	i := strings.Index(regAddr, "://")
	if i < 0 {
		return nil, errors.New("wrong format registry address. The right fotmat is [registry_type://address]")
	}

	regAddr = regAddr[i+3:]
	return client.NewConsulDiscoveryTemplate(*basePath, []string{regAddr}, nil), nil
}
