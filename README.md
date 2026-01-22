# KV Storage Project

一个高性能、多协议兼容、从单机演进至分布式的 KV 存储系统。

## 单机 KV 全链路调用示意图

```
[ 外部世界 ]      [ 协议层 (Protocol) ]       [ 调度层 (Coord) ]      [ 核心层 (Engine/Storage) ]
    |                    |                      |                      |
 1. 客户端发送命令        |                      |                      |
    (SET k v) --------> [ RedisExporter ]       |                      |
    (字节流)             | (TCP Accept)         |                      |
                         |                      |                      |
                         | 2. ParseRequest()    |                      |
                         | (字节流 -> Request)   |                      |
                         |          |           |                      |
                         | 3. 调用 Coordinate()  |                      |
                         | (common.Request) --> [ LocalCoordinator ]   |
                         |                      | (识别 OpSet)          |
                         |                      |          |           |
                         |                      | 4. 调用 KV.Set()      |
                         |                      | (逻辑分发) --------> [ engine.KV ]
                         |                      |                      | (获取 TSO 时间戳)
                         |                      |                      |          |
                         |                      |                      | 5. 调用 Storage.Set()
                         |                      |                      | (写入物理内存)
                         |                      |                      |          ↓
                         |                      |                      | [ MemoryStorage ]
                         |                      |                      | (数据落地)
                         |                      |                      |          |
                         | 6. 返回结果           | <--------------------+----------+
                         | (common.Response) <- [ LocalCoordinator ]
                         |          |           |
                         | 7. EncodeResponse()  |
                         | (Response -> 字节流)  |
                         |          |           |
 8. 回复客户端 <--------- [ RedisExporter ]       |
    (+OK\r\n)            | (TCP Write)          |
    (字节流)             |                      |
```

- 多通信协议兼容：支持 Redis、gRPC 等多种协议的并发接入与统一调度。
- 分布式一致性解耦：支持从单机模式平滑演进至 Raft 等分布式共识协议，实现一致性策略的透明切换。
- 多存储模式支持：底层存储接口通用化，支持从高性能内存存储向磁盘持久化模式的无缝扩展。