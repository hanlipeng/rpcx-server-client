package service

import (
	"context"
	"fmt"
	"newMicro/server2/service"
)

type DemoServiceImpl struct {
	PrintService service.PrintService
}

func (d DemoServiceImpl) TestGet(ctx context.Context, args *TestGetArgs, reply *TestReply) error {
	i := args.X + args.Y
	reply.Result= i
	err := d.PrintService.Print(ctx, &service.PrintArgs{PrintContext: fmt.Sprintf("%v + %v = %v", args.X, args.Y, i)}, new(service.PrintReply))
	return err
}
