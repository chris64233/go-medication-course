# go-medication-course

本项目用于 GSB 评测，基础仓库只保留可运行的 Go 模块骨架。

## 功能

`medication` 包提供用药疗程与服药记录服务（内存实现，goroutine 安全）：

- 创建疗程：记录患者、药品、剂量、每日频次、开始与计划结束时间，拒绝缺失或明显不合理的剂量/时间输入。
- 服药登记：按计划时间确认服药（`ConfirmDose`）、登记漏服（`MarkMissed`）、补记（`BackfillDose`，必须提供实际发生时间且不能晚于当前时间）。
- 幂等：同一疗程同一计划时间重复提交返回已有记录，不重复计数。
- 生命周期：暂停/恢复/完成/取消；取消后保留已记录的服药事实，取消之后的时间点不能再登记服药。
- 查询：按患者查疗程、按疗程查记录（按时间稳定排序）、计划时间点生成与依从性汇总（应服/已服/漏服/补记/待登记）。
- 错误：通过 `ErrInvalidInput`、`ErrInvalidState`、`ErrNotFound` 配合 `errors.Is/As` 判断，错误文案面向调用方，不暴露内部异常。

## 运行测试

```bash
go test ./...
```

查看覆盖率：

```bash
go test ./... -coverprofile=coverage.out && go tool cover -func=coverage.out
```
