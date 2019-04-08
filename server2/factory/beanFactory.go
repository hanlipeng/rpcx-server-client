package factory

import "newMicro/server2/service"

func GetPrintService()service.PrintService{
	return new(service.PrintServiceImpl)
}
