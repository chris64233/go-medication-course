# go-medication-course

一个用 Go 实现的用药疗程（medication course）领域库，支持疗程创建、按处方
展开计划服药时间点、服药登记（含补记）、暂停/恢复、完成/取消，以及处方剂量
与频次的**版本化调整**。

核心原则：**处方调整只影响尚未发生的计划时间点，过去已经登记的服药事实是
不可变记录，永远不会被重算或覆盖。**

## 模型

- `Course`：疗程聚合根，包含状态、起始时间、处方版本序列、暂停窗口和服药事实。
- `PrescriptionVersion`：一条不可变处方版本，记录版本号、剂量 `Dose`、频次
  `Frequency`、生效时间、调整人、原因、提交时间和外部请求号。版本 1 为开方
  版本，从疗程开始时间生效；后续版本号严格递增。
- `Intake`：已确认的服药事实，冻结登记时的 `ScheduledAt`、`TakenAt`、处方
  版本和剂量；`Backfill=true` 表示补记。
- `PlannedPoint`：计划查询展开的单个时间点，明确标注该时间点采用的版本和
  剂量，并关联已登记的 `Intake`（保留原版本、原剂量）。

## 计划展开规则

- 每个版本在自己的 `[生效时间, 下一版本生效时间)` 区段内，以**自身生效时间
  为锚点**、按该版本频次展开。因此调整后未到时间的计划可以重排，而已生成的
  服药记录仍保留原版本和原剂量。
- 边界时间点归属新版本：恰好等于某版本生效时间的计划点按新版本裁决，用旧
  版本登记该时间点返回版本冲突。
- 落在暂停窗口 `[From, To)` 内的时间点不出现在计划中；当前仍暂停时开放窗口
  覆盖 `From` 之后的所有时间。
- 已登记事实若因调整不再落在当前计划序列上，仍会作为历史点追加返回，历史
  计划不会被删除。

使用 `ExpandPlan(course, start, end)` 按时间展开；开放结束时间的疗程必须
显式给出查询结束时间。

## 处方调整

`Service.AdjustPrescription` 提交新版本，规则如下：

- 保存调整人、原因、提交时间、生效时间；版本号自动递增。
- 生效时间必须**严格晚于**所有已确认服药事实（`ScheduledAt`/`TakenAt` 的
  最大值），也必须晚于当前版本生效时间；否则返回 `effective_time_conflict`。
- 已 `completed` 或 `cancelled` 的疗程不能再创建调整，返回
  `status_conflict`。
- 可携带 `ExpectedVersion` 做乐观并发控制；与最新版本不符返回
  `version_conflict`。

### 幂等

调整请求必须带外部请求号 `RequestID`：

- 同号且剂量、频次、生效时间完全相同：返回原调整，不产生新版本。
- 同号但剂量、频次或生效时间任一不同：返回 `idempotency_conflict`，计划与
  服药记录均保持不变（所有校验先于写入，失败不留部分状态）。

`Service.AdjustmentHistory` 返回包含初始版本在内的完整调整历史。

## 服药登记与并发裁决

- 登记必须指定 `ExpectedVersion`，且必须等于 `ScheduledAt` 当时生效的处方
  版本；携带旧版本登记未来服药返回 `version_conflict`。
- `ScheduledAt` 必须是当前计划中的合法时间点，且不能落在暂停窗口内。
- `TakenAt` 晚于 `ScheduledAt` 自动标记为补记（`Backfill=true`）；补记只按
  计划时间当时的版本和剂量入账，不能伪装成新版本下的正常服药，也不能用新
  版本号补记旧时间点。
- 同一时间点重复登记返回 `duplicate_conflict`。
- 只有 `active` 疗程允许登记；暂停中、已完成、已取消均返回
  `status_conflict`。
- 服药登记、处方调整、暂停/恢复、完成/取消在同一 `Service` 上串行裁决，
  状态事件同样支持 `ExpectedVersion`，保证以同一处方版本和疗程状态裁决。

## 错误处理

所有业务拒绝都返回 `*ConflictError`，可用 `errors.As` / `IsConflict(err, kind)`
判定，`ConflictKind` 取值：`version_conflict`、`status_conflict`、
`effective_time_conflict`、`idempotency_conflict`、`duplicate_conflict`、
`invalid_request`。

## 使用示例

```go
svc := gomedicationcourse.NewService(nil)
start := time.Date(2026, 1, 1, 8, 0, 0, 0, time.UTC)

svc.CreateCourse(gomedicationcourse.CreateCourseRequest{
    ID:        "course-1",
    Dose:      gomedicationcourse.Dose{Amount: 100, Unit: "mg"},
    Frequency: gomedicationcourse.Frequency{Every: 8 * time.Hour},
    StartAt:   start,
    Operator:  "dr-a",
})

svc.RegisterIntake(gomedicationcourse.RegisterIntakeRequest{
    CourseID: "course-1", ScheduledAt: start, ExpectedVersion: 1,
})

svc.AdjustPrescription(gomedicationcourse.AdjustPrescriptionRequest{
    CourseID:    "course-1",
    Dose:        gomedicationcourse.Dose{Amount: 200, Unit: "mg"},
    Frequency:   gomedicationcourse.Frequency{Every: 12 * time.Hour},
    EffectiveAt: start.Add(24 * time.Hour),
    Operator:    "dr-b",
    Reason:      "dose escalation",
    RequestID:   "ext-req-001",
})

c, _ := svc.GetCourse("course-1")
plan, _ := gomedicationcourse.ExpandPlan(c, start, start.Add(72*time.Hour))
for _, p := range plan {
    // p.Version 显示该时间点采用的处方版本；p.Taken 保留原始服药事实。
    _ = p
}
```

## 测试

`go test ./... -race`，覆盖：

- 跨版本计划展开与已登记事实版本/剂量冻结；
- 边界生效时间的版本归属；
- 生效时间不得早于（含等于）已确认服药事实；
- 补记标记、补记版本限制、非计划点与重复登记；
- 并发登记与调整的统一裁决、旧版本状态事件冲突；
- 幂等重放返回原调整、同号异内容冲突且无部分变更；
- 暂停/恢复窗口、完成/取消后禁止调整与登记；调整历史版本递增。
