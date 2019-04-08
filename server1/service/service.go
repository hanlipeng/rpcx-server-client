package service

import "context"

type DemoService interface {
TestGet(ctx context.Context,args *TestGetArgs,reply *TestReply) error
}
type TestGetArgs struct {
	X int
	Y int
}
type TestReply struct {
	Result int
}