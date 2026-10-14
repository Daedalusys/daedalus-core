package main

// container_set_test.go —— container.set 适配器正向行为测试。
//
// podman 一律经包级注入缝 podmanExecutable 指向 t.TempDir() 下的 shell 夹具
// (模式镜像 task_set_test.go:installFakeSystemctl): 每次调用把 "$*" 追加进
// capture 文件(一行一次调用),"$2"=="inspect" 且 "$3"=="--type" 时按夹具回显
// JSON,其他子命令返回 0。绝不触碰宿主真实 podman —— 测试确定性 + 隔离性双重保证。

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/Daedalusys/daedalus-core/internal/tx"
)

// installFakePodman 安装假 podman 并注入包级变量(测试结束自动还原)。
// inspectJSON 是 `inspect --type container --format json <name>` 的固定回显;
// 其他子命令(start/stop/rm)返回 exitCode。
func installFakePodman(t *testing.T, inspectJSON string, exitCode int) string {
	t.Helper()
	dir := t.TempDir()
	capture := filepath.Join(dir, "argv.txt")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + strconv.Quote(capture) + "\n"
	script += "if [ \"$1\" = inspect ] && [ \"$2\" = --type ] && [ \"$3\" = container ]; then\n"
	script += "  printf '%s\\n' " + strconv.Quote(inspectJSON) + "\n"
	script += "  exit " + strconv.Itoa(exitCode) + "\n"
	script += "fi\n"
	script += "case \"$1\" in\n"
	script += "  start) exit " + strconv.Itoa(exitCode) + " ;;\n"
	script += "  stop)  exit " + strconv.Itoa(exitCode) + " ;;\n"
	script += "  rm)    exit " + strconv.Itoa(exitCode) + " ;;\n"
	script += "esac\nexit 0\n"
	path := filepath.Join(dir, "podman")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("写入假 podman 失败: %v", err)
	}
	orig := podmanExecutable
	podmanExecutable = path
	t.Cleanup(func() { podmanExecutable = orig })
	return capture
}

// withNonRootEuid 临时把 geteuid 翻为 1000(模拟普通用户),测试结束自动还原。
// euid 守门是 Propose/Apply/Rollback 三段共用前置,unit / guard 测试都需要
// 走在 user-domain 才不会被 rootful 守门错拒。
func withNonRootEuid(t *testing.T) {
	t.Helper()
	orig := geteuid
	geteuid = func() int { return 1000 }
	t.Cleanup(func() { geteuid = orig })
}

func TestContainerSet_Propose_PresentHappyPath(t *testing.T) {
	withNonRootEuid(t)

	fixtureJSON := `[{"Id":"abc123","State":{"Running":true},"Image":"nginx:latest","Status":"Up 5 minutes"}]`
	installFakePodman(t, fixtureJSON, 0)

	a := containerSetAdapter{}
	before, after, err := a.Propose(context.Background(),
		json.RawMessage(`{"name":"web","desired_state":"present"}`))
	if err != nil {
		t.Fatalf("propose 意外失败: %v", err)
	}

	var b containerSetBefore
	if err := json.Unmarshal(before, &b); err != nil {
		t.Fatalf("before_state 非法 JSON(%v): %s", err, before)
	}
	if b.ContainerName != "web" {
		t.Errorf("container_name got %q want %q", b.ContainerName, "web")
	}
	if b.ContainerID != "abc123" {
		t.Errorf("container_id got %q want %q", b.ContainerID, "abc123")
	}
	if b.StateRunning == nil || !*b.StateRunning {
		t.Errorf("state_running 应为 true, got %v", b.StateRunning)
	}
	if b.Image != "nginx:latest" {
		t.Errorf("image got %q want %q", b.Image, "nginx:latest")
	}
	if b.Status != "Up 5 minutes" {
		t.Errorf("status got %q want %q", b.Status, "Up 5 minutes")
	}

	var af containerSetAfter
	if err := json.Unmarshal(after, &af); err != nil {
		t.Fatalf("after_state 非法 JSON(%v): %s", err, after)
	}
	if af.Container != "web" {
		t.Errorf("container got %q want %q", af.Container, "web")
	}
	if af.DesiredState != "present" {
		t.Errorf("desired_state got %q want %q", af.DesiredState, "present")
	}
	if af.Verb != "" {
		t.Errorf("present verb got %q want empty", af.Verb)
	}
}

func TestContainerSet_Propose_RejectsEuidZero(t *testing.T) {
	orig := geteuid
	geteuid = func() int { return 0 }
	t.Cleanup(func() { geteuid = orig })

	a := containerSetAdapter{}
	_, _, err := a.Propose(context.Background(),
		json.RawMessage(`{"name":"web","desired_state":"present"}`))
	if err == nil {
		t.Fatal("euid==0 必须拒绝")
	}
	want := "container.set requires rootless (euid=0 indicates rootful path; use quadlet + service.set for rootful containers)"
	if !strings.Contains(err.Error(), want) {
		t.Errorf("拒串漂移: got %q want substring %q", err.Error(), want)
	}
}

func TestContainerSet_Propose_RejectsUnknownDesiredState(t *testing.T) {
	withNonRootEuid(t)
	installFakePodman(t, "[]", 0)

	a := containerSetAdapter{}
	_, _, err := a.Propose(context.Background(),
		json.RawMessage(`{"name":"web","desired_state":"running"}`))
	if err == nil {
		t.Fatal("表外 desired_state 必须拒绝")
	}
	msg := err.Error()
	if !strings.Contains(msg, `unknown desired_state "running"`) {
		t.Errorf("未点名表外值: %s", msg)
	}
	if !strings.Contains(msg, "present|absent|started|stopped") {
		t.Errorf("未点名 v1 词表: %s", msg)
	}
}

func TestContainerSet_Propose_RejectsInvalidName(t *testing.T) {
	withNonRootEuid(t)
	installFakePodman(t, "[]", 0)

	for _, name := range []string{
		"a b",    // 空格
		"a/b",    // 路径分隔
		"-x",     // 前导中划线(argv 注入)
		"a\x00b", // 空字节(经 json.Marshal 转 \u0000)
	} {
		t.Run(name, func(t *testing.T) {
			a := containerSetAdapter{}
			args, err := json.Marshal(containerSetArgs{Name: name, DesiredState: "present"})
			if err != nil {
				t.Fatal(err)
			}
			_, _, err = a.Propose(context.Background(), args)
			if err == nil {
				t.Fatalf("%q 应被名称门拒", name)
			}
			if !strings.Contains(err.Error(), "invalid container name:") {
				t.Errorf("%q 未点名 invalid container name: got %q", name, err.Error())
			}
		})
	}
}

// notFoundInspect 制造容器不存在的 fixture: podman inspect 返回空数组 + rc=0.
const notFoundInspect = `[]`

func TestContainerSet_Propose_RejectsPresentWhenAbsent(t *testing.T) {
	withNonRootEuid(t)
	installFakePodman(t, notFoundInspect, 0)

	a := containerSetAdapter{}
	_, _, err := a.Propose(context.Background(),
		json.RawMessage(`{"name":"web","desired_state":"present"}`))
	if err == nil {
		t.Fatal("present + 容器不存在 必须拒绝")
	}
	want := `container "web" not found; create out of v1 scope`
	if !strings.Contains(err.Error(), want) {
		t.Errorf("拒串漂移: got %q want substring %q", err.Error(), want)
	}
}

func TestContainerSet_Propose_RejectsStartedWhenAbsent(t *testing.T) {
	withNonRootEuid(t)
	installFakePodman(t, notFoundInspect, 0)

	a := containerSetAdapter{}
	_, _, err := a.Propose(context.Background(),
		json.RawMessage(`{"name":"web","desired_state":"started"}`))
	if err == nil {
		t.Fatal("started + 容器不存在 必须拒绝")
	}
	if !strings.Contains(err.Error(), `container "web" not found`) {
		t.Errorf("拒串漂移: %s", err.Error())
	}
}

func TestContainerSet_Propose_RejectsStoppedWhenAbsent(t *testing.T) {
	withNonRootEuid(t)
	installFakePodman(t, notFoundInspect, 0)

	a := containerSetAdapter{}
	_, _, err := a.Propose(context.Background(),
		json.RawMessage(`{"name":"web","desired_state":"stopped"}`))
	if err == nil {
		t.Fatal("stopped + 容器不存在 必须拒绝")
	}
	if !strings.Contains(err.Error(), `container "web" not found`) {
		t.Errorf("拒串漂移: %s", err.Error())
	}
}

func TestContainerSet_Propose_AbsentIdempotentWhenAbsent(t *testing.T) {
	withNonRootEuid(t)
	installFakePodman(t, notFoundInspect, 0)

	a := containerSetAdapter{}
	before, after, err := a.Propose(context.Background(),
		json.RawMessage(`{"name":"web","desired_state":"absent"}`))
	if err != nil {
		t.Fatalf("absent + 容器不存在 应 idempotent 放行, 意外失败: %v", err)
	}
	var b containerSetBefore
	if err := json.Unmarshal(before, &b); err != nil {
		t.Fatal(err)
	}
	if b.ContainerName != "web" {
		t.Errorf("container_name got %q want %q", b.ContainerName, "web")
	}
	if b.ContainerID != "" {
		t.Errorf("idempotent 路径下 container_id 应缺席, got %q", b.ContainerID)
	}
	if b.StateRunning != nil {
		t.Errorf("idempotent 路径下 state_running 应缺席, got %v", *b.StateRunning)
	}
	if b.Image != "" {
		t.Errorf("idempotent 路径下 image 应缺席, got %q", b.Image)
	}
	var af containerSetAfter
	if err := json.Unmarshal(after, &af); err != nil {
		t.Fatal(err)
	}
	if af.Verb != "rm" {
		t.Errorf("absent verb got %q want rm", af.Verb)
	}
}

func TestContainerSet_Propose_RejectsMissingDesiredState(t *testing.T) {
	withNonRootEuid(t)
	a := containerSetAdapter{}
	_, _, err := a.Propose(context.Background(),
		json.RawMessage(`{"name":"web"}`))
	if err == nil {
		t.Fatal("缺 desired_state 必须拒绝")
	}
	if !strings.Contains(err.Error(), `缺 "desired_state"`) {
		t.Errorf("未点名 missing field: %s", err.Error())
	}
}

func TestContainerSet_Propose_RejectsTrailingJSON(t *testing.T) {
	withNonRootEuid(t)
	a := containerSetAdapter{}
	_, _, err := a.Propose(context.Background(),
		json.RawMessage(`{"name":"web","desired_state":"present"}x`))
	if err == nil {
		t.Fatal("尾随 JSON 必须拒绝")
	}
	if !strings.Contains(err.Error(), "尾随数据") {
		t.Errorf("未点名 trailing data: %s", err.Error())
	}
}

// ===== Apply =====

// mustBuildContainerSetBefore 构造 before_state JSON(Apply/Rollback 测试共用).
func mustBuildContainerSetBefore(t *testing.T, name, id string, running bool, image, status string) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(containerSetBefore{
		ContainerName: name,
		ContainerID:   id,
		StateRunning:  &running,
		Image:         image,
		Status:        status,
	})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// readPodmanCalls 读 capture 为"每次调用一行 argv"的切片;不存在 → nil.
func readPodmanCalls(t *testing.T, capture string) []string {
	t.Helper()
	data, err := os.ReadFile(capture)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatalf("读 capture 失败: %v", err)
	}
	s := strings.TrimSuffix(string(data), "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

func TestContainerSet_Apply_Present_Idempotent(t *testing.T) {
	withNonRootEuid(t)
	fixtureJSON := `[{"Id":"abc123","State":{"Running":true},"Image":"nginx:latest","Status":"Up"}]`
	capture := installFakePodman(t, fixtureJSON, 0)

	a := containerSetAdapter{}
	res := a.Apply(context.Background(), tx.Step{
		Args:        json.RawMessage(`{"name":"web","desired_state":"present"}`),
		BeforeState: mustBuildContainerSetBefore(t, "web", "abc123", true, "nginx:latest", "Up"),
	})
	if res.Returncode != 0 {
		t.Fatalf("apply 意外失败: %+v", res)
	}
	for _, call := range readPodmanCalls(t, capture) {
		for _, forbid := range []string{" start ", " stop ", " rm "} {
			if strings.Contains(" "+call+" ", forbid) {
				t.Errorf("present 不应发动词, got %q", call)
			}
		}
	}
}

func TestContainerSet_Apply_Absent_RmForce(t *testing.T) {
	withNonRootEuid(t)
	capture := installFakePodman(t, "[]", 0)

	a := containerSetAdapter{}
	res := a.Apply(context.Background(), tx.Step{
		Args:        json.RawMessage(`{"name":"web","desired_state":"absent"}`),
		BeforeState: mustBuildContainerSetBefore(t, "web", "abc123", true, "nginx:latest", "Up"),
	})
	if res.Returncode != 0 {
		t.Fatalf("apply 意外失败: %+v", res)
	}
	want := []string{"rm --force web"}
	if got := readPodmanCalls(t, capture); !reflect.DeepEqual(got, want) {
		t.Errorf("podman 调用序列 got %v want %v", got, want)
	}
}

func TestContainerSet_Apply_Started_FromStopped(t *testing.T) {
	withNonRootEuid(t)
	capture := installFakePodman(t, "[]", 0)

	a := containerSetAdapter{}
	res := a.Apply(context.Background(), tx.Step{
		Args:        json.RawMessage(`{"name":"web","desired_state":"started"}`),
		BeforeState: mustBuildContainerSetBefore(t, "web", "abc123", false, "nginx:latest", "Exited"),
	})
	if res.Returncode != 0 {
		t.Fatalf("apply 意外失败: %+v", res)
	}
	want := []string{"start web"}
	if got := readPodmanCalls(t, capture); !reflect.DeepEqual(got, want) {
		t.Errorf("podman 调用序列 got %v want %v", got, want)
	}
}

func TestContainerSet_Apply_Started_NoOpWhenAlreadyRunning(t *testing.T) {
	withNonRootEuid(t)
	capture := installFakePodman(t, "[]", 0)

	a := containerSetAdapter{}
	res := a.Apply(context.Background(), tx.Step{
		Args:        json.RawMessage(`{"name":"web","desired_state":"started"}`),
		BeforeState: mustBuildContainerSetBefore(t, "web", "abc123", true, "nginx:latest", "Up"),
	})
	if res.Returncode != 0 {
		t.Fatalf("apply 意外失败: %+v", res)
	}
	if calls := readPodmanCalls(t, capture); calls != nil {
		t.Errorf("已 running 不应发动词, got %v", calls)
	}
}

func TestContainerSet_Apply_Stopped_FromRunning(t *testing.T) {
	withNonRootEuid(t)
	capture := installFakePodman(t, "[]", 0)

	a := containerSetAdapter{}
	res := a.Apply(context.Background(), tx.Step{
		Args:        json.RawMessage(`{"name":"web","desired_state":"stopped"}`),
		BeforeState: mustBuildContainerSetBefore(t, "web", "abc123", true, "nginx:latest", "Up"),
	})
	if res.Returncode != 0 {
		t.Fatalf("apply 意外失败: %+v", res)
	}
	want := []string{"stop web"}
	if got := readPodmanCalls(t, capture); !reflect.DeepEqual(got, want) {
		t.Errorf("podman 调用序列 got %v want %v", got, want)
	}
}

func TestContainerSet_Apply_Stopped_NoOpWhenAlreadyStopped(t *testing.T) {
	withNonRootEuid(t)
	capture := installFakePodman(t, "[]", 0)

	a := containerSetAdapter{}
	res := a.Apply(context.Background(), tx.Step{
		Args:        json.RawMessage(`{"name":"web","desired_state":"stopped"}`),
		BeforeState: mustBuildContainerSetBefore(t, "web", "abc123", false, "nginx:latest", "Exited"),
	})
	if res.Returncode != 0 {
		t.Fatalf("apply 意外失败: %+v", res)
	}
	if calls := readPodmanCalls(t, capture); calls != nil {
		t.Errorf("已 stopped 不应发动词, got %v", calls)
	}
}

// ===== Rollback =====

// absentBefore 构造 absent + 容器不曾存在的快照(ContainerID 空字符串).
func absentBefore(t *testing.T, name string) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(containerSetBefore{ContainerName: name})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestContainerSet_Rollback_Absent_NoOpWithNote(t *testing.T) {
	withNonRootEuid(t)
	capture := installFakePodman(t, "[]", 0)

	a := containerSetAdapter{}
	res := a.Rollback(context.Background(), tx.Step{
		Args:        json.RawMessage(`{"name":"web","desired_state":"absent"}`),
		BeforeState: absentBefore(t, "web"),
	})
	if res.Returncode != 0 {
		t.Fatalf("absent rollback 应返 rc=0, got %d: %s", res.Returncode, res.Error)
	}
	if !strings.Contains(res.Stdout, "recreation requires run-spec not in v1 scope") {
		t.Errorf("Stdout 应点名不可逆提示, got %q", res.Stdout)
	}
	if !strings.Contains(res.Stdout, `"web"`) {
		t.Errorf("Stdout 应含容器名 %q, got %q", "web", res.Stdout)
	}
	if calls := readPodmanCalls(t, capture); calls != nil {
		t.Errorf("absent rollback 不应调 podman, got %v", calls)
	}
}

func TestContainerSet_Rollback_RestoresStoppedFromStarted(t *testing.T) {
	// before 记录了"stopped"现况; Apply 发了 `start web` 到达 running;
	// Rollback 应还原到 stopped,调 `stop web`(幂等)。
	withNonRootEuid(t)
	capture := installFakePodman(t, "[]", 0)

	a := containerSetAdapter{}
	res := a.Rollback(context.Background(), tx.Step{
		Args:        json.RawMessage(`{"name":"web","desired_state":"started"}`),
		BeforeState: mustBuildContainerSetBefore(t, "web", "abc123", false, "nginx:latest", "Exited"),
	})
	if res.Returncode != 0 {
		t.Fatalf("rollback 意外失败: %+v", res)
	}
	want := []string{"stop web"}
	if got := readPodmanCalls(t, capture); !reflect.DeepEqual(got, want) {
		t.Errorf("podman 调用序列 got %v want %v", got, want)
	}
}

func TestContainerSet_Rollback_RestoresRunningFromStopped(t *testing.T) {
	// before 记录了"running"现况; Apply 发了 `stop web`;
	// Rollback 应还原到 running,调 `start web`.
	withNonRootEuid(t)
	capture := installFakePodman(t, "[]", 0)

	a := containerSetAdapter{}
	res := a.Rollback(context.Background(), tx.Step{
		Args:        json.RawMessage(`{"name":"web","desired_state":"stopped"}`),
		BeforeState: mustBuildContainerSetBefore(t, "web", "abc123", true, "nginx:latest", "Up"),
	})
	if res.Returncode != 0 {
		t.Fatalf("rollback 意外失败: %+v", res)
	}
	want := []string{"start web"}
	if got := readPodmanCalls(t, capture); !reflect.DeepEqual(got, want) {
		t.Errorf("podman 调用序列 got %v want %v", got, want)
	}
}

func TestContainerSet_Rollback_PresentFromPresent(t *testing.T) {
	// before 记录了"running"(present assert 场景下 BeforeState 也填); Rollback
	// 按 running=true 映射到 `start`(幂等).
	withNonRootEuid(t)
	capture := installFakePodman(t, "[]", 0)

	a := containerSetAdapter{}
	res := a.Rollback(context.Background(), tx.Step{
		Args:        json.RawMessage(`{"name":"web","desired_state":"present"}`),
		BeforeState: mustBuildContainerSetBefore(t, "web", "abc123", true, "nginx:latest", "Up"),
	})
	if res.Returncode != 0 {
		t.Fatalf("rollback 意外失败: %+v", res)
	}
	want := []string{"start web"}
	if got := readPodmanCalls(t, capture); !reflect.DeepEqual(got, want) {
		t.Errorf("podman 调用序列 got %v want %v", got, want)
	}
}
