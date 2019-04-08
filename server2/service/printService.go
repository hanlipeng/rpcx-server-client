package service

import "context"

type PrintService interface {
	Print(ctx context.Context,args *PrintArgs,reply *PrintReply) error
}
type PrintArgs struct {
	PrintContext string
}
type PrintReply struct {
	PrintReply bool
}