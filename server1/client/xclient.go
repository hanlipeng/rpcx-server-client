package client

import (
	"context"
	"github.com/smallnest/rpcx/client"
	"newMicro/server2/service"
)

type PrintServiceClient struct {
	Client client.XClient
}

func (p PrintServiceClient) Print(ctx context.Context, args *service.PrintArgs, reply *service.PrintReply) error {
	return p.Client.Call(ctx,"Print",args,reply)
}



