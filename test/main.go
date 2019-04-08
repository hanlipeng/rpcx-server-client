package main

import (
	"fmt"
	"reflect"
	"runtime"
)

type t interface {
	test()
}

func getMethodName(t func()){
	of := reflect.ValueOf(t)
	pointer := of.Pointer()
	pc := runtime.FuncForPC(pointer)
	name := pc.Name()
	fmt.Println(name)
}

func main() {
	i := tstruct{}
	getMethodName(i.test)
}

type tstruct struct {
}

func (*tstruct) test(){}

func test(){}