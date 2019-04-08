package factory

import (
	"flag"
	"github.com/smallnest/rpcx/client"
	client2 "newMicro/server1/client"
	"newMicro/server1/config"
	"newMicro/server1/service"
	server2 "newMicro/server2/service"
)

var beanMap = make(map[string]interface{}, 16)

func GetDemoService() service.DemoService {
	beanFlag := "DemoService"
	initFunc := func() interface{} {
		return service.DemoServiceImpl{PrintService: GetPrintClient()}
	}
	return getBean(beanFlag, initFunc).(service.DemoService)
}

func GetPrintClient() server2.PrintService {
	beanFlag := "PrintService"
	basePath := flag.String("server2", "/server2", "prefix path")

	initFunc := func() interface{} {
		return client2.PrintServiceClient{Client: initXClient(beanFlag,basePath)}
	}
	return getBean(beanFlag, initFunc).(server2.PrintService)
}

func getBean(beanName string, initFunc func() interface{}) interface{} {
	bean := beanMap[beanName]
	if bean == nil {
		bean = initFunc()
		beanMap[beanName] = bean
	}
	return bean
}

func initXClient(serverName string,basePath *string) client.XClient {

	d := client.NewConsulDiscovery(*basePath, serverName, []string{*config.ConsulAddr}, nil)
	xclient := client.NewXClient(serverName, client.Failtry, client.RandomSelect, d, client.DefaultOption)
	return xclient
}
