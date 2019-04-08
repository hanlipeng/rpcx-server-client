package service

import (
	"context"
	"fmt"
)

type PrintServiceImpl struct {

}

func (*PrintServiceImpl) Print(ctx context.Context, args *PrintArgs, reply *PrintReply) error {

	fmt.Println(args.PrintContext)
	reply.PrintReply = true
	return nil
}

