# go-medication-course

本项目用于 GSB 评测，实现疗程管理与处方调整的版本化记录。

## 领域模型

- **疗程（Course）**：生命周期为 `active → paused → active`，终态为 `completed` / `cancelled`。
- **处方版本（PrescriptionVersion）**：创建疗程时生成版本 1；每次调整产生递增的新版本，
  记录剂量、频次（小时间隔）、调整人、原因、提交时间与生效时间。
- **服药记录（DoseRecord）**：已登记的服药事实，不可修改；永远保留计划时间点当时生效的
  版本与剂量，补记（`Backfill=true`）也不会被新版本重写。

## 核心规则

1. **调整只影响未来**：`SubmitAdjustment` 的生效时间不能早于已确认的服药事实，
   也不能早于疗程开始；版本号严格递增。
2. **计划按时间选版本**：`Plan(courseID, from, to)` 展开计划点，每个点标注当时生效的
   版本与剂量；调整后未到时间的计划自动重排，历史计划与服药记录保持不变。
3. **并发裁决**：所有写操作在同一把互斥锁下先校验后变更。`RegisterDose` 携带
   `expectedVersion`，与该时间点当前生效版本不一致时返回 `ErrVersionConflict`；
   已暂停返回 `ErrCoursePaused`，已完成/取消返回 `ErrCourseTerminated`。
4. **幂等调整**：调整请求携带外部请求号，同号同内容返回原调整，同号不同剂量/频次/
   生效时间返回 `ErrRequestConflict`；校验全部通过后才变更，失败不产生部分修改。
5. **补记限制**：只能对存在的计划点登记；重复登记返回 `ErrDoseAlreadyRecorded`；
   过去时间点的登记标记为补记，保留原版本原剂量。

## 主要 API

- `CreateCourse` / `PauseCourse` / `ResumeCourse` / `CompleteCourse` / `CancelCourse`
- `SubmitAdjustment(courseID, AdjustmentRequest)`：提交新处方版本
- `RegisterDose(courseID, scheduledAt, expectedVersion)`：登记服药事实
- `AdjustmentHistory(courseID)`：调整历史（含初始版本）
- `Plan(courseID, from, to)`：按时间展开的计划查询，逐点显示采用版本

## 测试

```sh
go test ./... -race
```

覆盖跨版本计划、边界生效时间、补记限制、并发登记与调整、重复请求幂等、
暂停/恢复与终态约束。
