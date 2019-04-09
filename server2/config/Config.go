package config

import "flag"

var (
	Addr       = flag.String("addr", "localhost:8081", "server address")
	ConsulAddr = flag.String("consulAddr", "10.0.0.5:8500", "consul address")
	BasePath   = flag.String("server", "/server", "prefix path")
)
