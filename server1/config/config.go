package config

import "flag"

var (
	Addr = flag.String("addr", "localhost:8080", "server address")
	ConsulAddr = flag.String("consulAddr", "localhost:8500", "consul address")
	BasePath   = flag.String("server1", "/server1", "prefix path")
)
