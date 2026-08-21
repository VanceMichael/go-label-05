# Bug Reproduction

## 包的性质

当前 test_model_fix 保存的是被测模型修复后的结果源码，不是初始含 Bug 源码。要复现原始缺陷，必须检出下面固定的 parent SHA；不要在当前修复结果源码上期待重新出现修复前失败。生成系统使用的可信验证补丁和完整验证日志仅在本地留存，不提交到结果分支。

## 问题现象

昨晚批量导入牛舍传感器数据时，有一条损坏记录让处理器直接崩溃，整个 HerdCycle 进程随即退出，接口既没有返回这条记录的失败原因，也没有说明队列里其余记录是否执行。请修复批处理的异常隔离：单条处理逻辑发生 panic 时要留下可识别的 internal 失败结果，不能拖垮服务；启用 StopOnError 后，尚未开始的记录应标为 canceled 且不得再调用处理器，结果顺序仍与输入一致。

## 含 Bug 版本

- 仓库：VanceMichael/go-label-05
- 仓库地址：https://github.com/VanceMichael/go-label-05.git
- parent SHA：06c55594c43a931eb829d928df8b82aeb637e7fe

## 复现步骤

```bash
git clone -- https://github.com/VanceMichael/go-label-05.git bug-repro
cd bug-repro
git checkout --detach 06c55594c43a931eb829d928df8b82aeb637e7fe
go test ./internal/batch -run ^TestProcessorPanicIsContainedAndStopsRemainingWork$ -count=1
```

## 双架构完整错误信息

### linux/amd64

- 容器内复现预期退出码：1
- 容器内复现实际退出码：1

stdout：

```text
$ go test ./internal/batch -run ^TestProcessorPanicIsContainedAndStopsRemainingWork$ -count=1
panic: telemetry decoder invariant violated

goroutine 19 [running]:
go-base/internal/batch.TestProcessorPanicIsContainedAndStopsRemainingWork.func1({0xc000126210?, 0x5434e0?}, 0x5800a0?)
	/app/internal/batch/service_test.go:23 +0x4b
go-base/internal/batch.itemExecutor[...].run(0x5800a0?, {0x580148?, 0xc000140190}, {0x4cea00, {{0x54cf0f, 0x63e320?}, 0xc00013a410?}})
	/app/internal/batch/execution.go:33 +0x19d
go-base/internal/batch.Run[...].func1()
	/app/internal/batch/service.go:84 +0x17e
created by go-base/internal/batch.Run[...] in goroutine 18
	/app/internal/batch/service.go:81 +0x77a
FAIL	go-base/internal/batch	0.034s
FAIL

```

stderr：

```text
(empty)
```

### linux/arm64

- 容器内复现预期退出码：1
- 容器内复现实际退出码：1

stdout：

```text
$ go test ./internal/batch -run ^TestProcessorPanicIsContainedAndStopsRemainingWork$ -count=1
panic: telemetry decoder invariant violated

goroutine 7 [running]:
go-base/internal/batch.TestProcessorPanicIsContainedAndStopsRemainingWork.func1({0x400001a250?, 0xffffacbf2a48?}, 0x1905e0?)
	/app/internal/batch/service_test.go:23 +0x90
go-base/internal/batch.itemExecutor[...].run(0x1905e0?, {0x1906c0?, 0x4000010230}, {0x145380, {{0x15cee3, 0xddb18?}, 0x400003a718?}})
	/app/internal/batch/execution.go:33 +0x124
go-base/internal/batch.Run[...].func1()
	/app/internal/batch/service.go:84 +0xf4
created by go-base/internal/batch.Run[...] in goroutine 6
	/app/internal/batch/service.go:81 +0x77c
FAIL	go-base/internal/batch	0.005s
FAIL

```

stderr：

```text
(empty)
```

## 通过条件

传感器批次的首条处理器 panic 必须被限制在该作业边界内，报告中保留包含原始 panic 信息的 internal 失败项；StopOnError 随即停止调度，后续等待项只记录 canceled 且调用次数保持为零，两个结果仍按输入顺序返回，进程不得崩溃或遗留 goroutine。TestProcessorPanicIsContainedAndStopsRemainingWork 需在 -race 下由红转绿，输入顺序回归、全仓 go test ./... 与 go build ./... 同时通过；不得吞掉失败项、继续执行等待项或改弱测试。
