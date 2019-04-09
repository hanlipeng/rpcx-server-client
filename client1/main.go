package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"newMicro/server1/service"
	"time"

	"github.com/smallnest/rpcx/client"
)

var (
	consulAddr = flag.String("consulAddr", "10.0.0.5:8500", "consul address")
	basePath   = flag.String("base", "/server", "prefix path")
)

func main() {
	flag.Parse()

	d := client.NewConsulDiscovery(*basePath, "DemoService", []string{*consulAddr}, nil)
	xclient := client.NewXClient("DemoService", client.Failtry, client.RandomSelect, d, client.DefaultOption)
	defer xclient.Close()

	args := &service.TestGetArgs{
		X: 1,
		Y: 2,
	}

	for {
		reply := &service.TestReply{}
		start := time.Now()
		err := xclient.Call(context.Background(), "TestGet", args, reply)
		if err != nil {
			log.Printf("ERROR failed to call: %v", err)
		}
		fmt.Println(time.Since(start).Seconds())

		log.Printf("%d + %d = %d", args.X, args.Y, reply.Result)
		time.Sleep(1e9)
	}

}