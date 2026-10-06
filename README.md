# go-medication-course

本项目用于 GSB 评测，基础仓库只保留可运行的 Go 模块骨架。

本轮在疗程、计划服药、暂停/恢复/完成/取消能力之上，增加**处方调整的版本化记录**。

## 核心规则

- 处方以版本序列保存：创建疗程即生成版本 1，每次成功调整产生严格递增的新版本。每个版本记录调整人（`Author`）、原因（`Reason`）、提交时间（`SubmittedAt`）、生效时间（`EffectiveAt`）以及外部请求号（`ExternalRequestID`）。
- 调整只影响生效时间之后尚未发生的计划点：`Plan` 按每个计划时间点选择当时生效的版本与剂量。已登记的服药事实（`DoseRecord`）永久保留登记当时的版本号与剂量，不会因后续调整被重算或覆盖。
- 生效时间不得早于已经确认的服药事实的最晚计划时间（相等允许，属于边界），也不得早于疗程开始时间，否则分别返回 `ErrEffectiveBeforeConfirmed` / `ErrInvalidEffectiveAt`。
- 服药登记必须携带 `expectedVersion`：与该计划时间点当前生效版本不一致时返回 `ErrVersionConflict`（例如调整后仍携带旧版本登记未来服药）。对过去时间点的登记属于补记（`Backfill=true`），仍按该时间点当时生效的版本记录，不能伪装成新版本的正常服药；非计划时间点返回 `ErrNoPlannedDose`，同一计划点重复登记返回 `ErrDoseAlreadyRecorded`。
- 暂停期间不能登记或调整（`ErrCoursePaused`）；已完成/已取消的疗程不能再创建调整或登记（`ErrCourseTerminated`）。
- 调整请求按外部请求号幂等：同号同内容返回原调整；同号但剂量、频次或生效时间不同返回 `ErrRequestConflict`。所有校验先于任何变更，失败时计划与服药记录不发生部分改变。
- 所有读写在同一把互斥锁下裁决，保证服药登记、处方调整、暂停/恢复与完成/取消并发时以同一处方版本和疗程状态为准，版本号连续无丢号。

## 主要 API

- `NewService()` / `SetClock`：创建内存服务，测试可注入固定时钟。
- `CreateCourse(patient, startAt, doseMg, intervalHours, author, reason)`：创建疗程与初始处方版本（版本 1，自 `startAt` 生效）。
- `SubmitAdjustment(courseID, AdjustmentRequest)`：提交新版本，支持幂等。
- `RegisterDose(courseID, scheduledAt, expectedVersion)`：登记服药事实，返回不可变的 `DoseRecord`（含 `Version`、`DoseMg`、`Backfill`）。
- `PauseCourse` / `ResumeCourse` / `CompleteCourse` / `CancelCourse`：生命周期流转。
- `AdjustmentHistory(courseID)`：按版本号升序返回全部处方版本。
- `Plan(courseID, from, to)`：按时间展开 `[from, to)` 内的计划点，每个 `PlannedPoint` 明确标注 `ScheduledAt`、`Version`、`DoseMg`、`Status`（`pending`/`taken`/`missed`）及关联的服药记录。

## 测试

`go test ./... -race` 覆盖跨版本计划展开、边界生效时间、补记限制、并发登记/调整/完成裁决以及外部请求号幂等等场景。
