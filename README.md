# rpcx-server-client

分别开三个Terminal
在根目录下命令行运行  
go run -tags consul server2/main.go   
go run -tags consul server1/main.go   
go run -tags consul client1/main.go   
在client1,server2看到无报错输出则启动成功