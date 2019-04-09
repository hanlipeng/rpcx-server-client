package config

import "flag"

var (
	Addr = flag.String("addr", "localhost:8080", "server address")
	ConsulAddr = flag.String("consulAddr", "10.0.0.5:8500", "consul address")
	BasePath   = flag.String("basepath", "/server", "prefix path")
)
