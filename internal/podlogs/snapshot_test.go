package podlogs

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

func TestCollectSourcesCurrentPreviousWindow(t *testing.T) {
	var mu sync.Mutex
	requests := []url.Values{}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests = append(requests, r.URL.Query())
		mu.Unlock()
		fmt.Fprintln(w, "2026-10-13T10:00:00.123456789Z selected container run")
	}))
	defer api.Close()
	client, err := kubernetes.NewForConfig(&rest.Config{Host: api.URL})
	if err != nil {
		t.Fatal(err)
	}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "db-1"}}
	since := int64(60)
	got := CollectSources(context.Background(), client, "test", []Source{NewSource(pod, "database", false), NewSource(pod, "database", true)}, 0, &since, true)
	if got.Notice != "" || len(got.Logs) != 2 || len(got.SourcePods) != 1 {
		t.Fatalf("snapshot=%+v", got)
	}
	previous := 0
	for _, entry := range got.Logs {
		if entry.Previous {
			previous++
		}
	}
	if previous != 1 {
		t.Fatalf("previous entries=%d", previous)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 2 {
		t.Fatalf("requests=%v", requests)
	}
	previous = 0
	for _, q := range requests {
		if _, present := q["tailLines"]; present {
			t.Fatalf("historical window must omit tailLines: %v", q)
		}
		if q.Get("sinceSeconds") != "60" || q.Get("limitBytes") != "65537" || q.Get("timestamps") != "true" || q.Get("container") != "database" {
			t.Fatalf("request=%v", q)
		}
		if q.Get("previous") == "true" {
			previous++
		}
	}
	if previous != 1 {
		t.Fatalf("previous requests=%d", previous)
	}
}

func TestSnapshotClipNoticeRespectsCallerWindow(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, strings.Repeat("2026-10-13T10:00:00.123456789Z historical line\n", 2000))
	}))
	defer api.Close()
	client, err := kubernetes.NewForConfig(&rest.Config{Host: api.URL})
	if err != nil {
		t.Fatal(err)
	}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "db-1"}}
	got := CollectSources(context.Background(), client, "test", []Source{NewSource(pod, "database", true)}, 0, nil, true)
	last, _ := time.Parse(time.RFC3339Nano, "2026-10-13T10:00:00.123456789Z")
	if len(got.Clipped) != 1 || got.Clipped[0].Pod != "db-1" || got.Clipped[0].Container != "database" || !got.Clipped[0].Previous || !got.Clipped[0].Last.Equal(last) {
		t.Fatalf("clip=%+v", got.Clipped)
	}
	if got.Notice != "1 source reached the 64 KiB snapshot limit." {
		t.Fatalf("notice=%q", got.Notice)
	}
	if notice := got.Summarize(func(c Clip) bool { return c.Last.Before(last) }); notice != "" {
		t.Fatalf("clip outside the window should not warn: %q", notice)
	}
	if notice := got.Summarize(func(c Clip) bool { return c.Last.Before(last.Add(time.Second)) }); notice != got.Notice {
		t.Fatalf("clip inside the window=%q", notice)
	}
	if len(got.Logs) == 0 || !strings.HasSuffix(got.Logs[len(got.Logs)-1].Content, "historical line") {
		t.Fatal("clipping left a partial line")
	}
}

func TestLogWireContracts(t *testing.T) {
	updated := false
	cases := []struct {
		name  string
		value any
		want  string
	}{
		{"pod", PodInfo{Name: "p", Containers: []string{"init", "app"}, Ready: true, Phase: "Running", NodeName: "node", HealthLevel: "healthy", Reason: "reason", Message: "message", RestartCount: 2, LastTerminationReason: "Error", CreatedAt: "time", ContainerStatuses: []ContainerInfo{{Name: "init", Init: true, Ready: false, RestartCount: 1}, {Name: "app", Ready: true, RestartCount: 1}}, StepID: "step", StepName: "run", StepPhase: "Running", RevisionIdentity: "hash", UpdatedRevision: &updated}, `{"name":"p","containers":["init","app"],"ready":true,"phase":"Running","nodeName":"node","healthLevel":"healthy","reason":"reason","message":"message","restartCount":2,"lastTerminationReason":"Error","createdAt":"time","containerStatuses":[{"name":"init","init":true,"ready":false,"restartCount":1},{"name":"app","ready":true,"restartCount":1}],"stepID":"step","stepName":"run","stepPhase":"Running","revisionIdentity":"hash","updatedRevision":false}`},
		{"current entry", Entry{Pod: "p", Container: "app", Timestamp: "time", Content: "raw", SourceLabel: "primary", Level: "info", Logger: "postgres", Message: "message", Follow: 42}, `{"pod":"p","container":"app","timestamp":"time","content":"raw","sourceLabel":"primary","level":"info","logger":"postgres","message":"message"}`},
		{"previous entry", Entry{Pod: "p", Container: "app", Previous: true}, `{"pod":"p","container":"app","timestamp":"","content":"","previous":true}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := json.Marshal(tc.value)
			if err != nil {
				t.Fatal(err)
			}
			var got, want any
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal([]byte(tc.want), &want); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("wire shape=%s; want %s", raw, tc.want)
			}
		})
	}
	empty := CollectPods(context.Background(), nil, "test", nil, "", 100, nil, true)
	raw, err := json.Marshal(empty.Logs)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "[]" {
		t.Fatalf("empty logs=%s", raw)
	}
	raw, err = json.Marshal(BuildPodInfos(nil))
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "[]" {
		t.Fatalf("empty pods=%s", raw)
	}
}

func TestSnapshotClipUnknownTimestampStillReportsIncompleteData(t *testing.T) {
	for _, body := range []string{strings.Repeat("x", 65538), strings.Repeat("not a timestamp but a complete line\n", 3000)} {
		t.Run(fmt.Sprintf("prefix_%s", body[:3]), func(t *testing.T) {
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) }))
			defer api.Close()
			client, err := kubernetes.NewForConfig(&rest.Config{Host: api.URL})
			if err != nil {
				t.Fatal(err)
			}
			got := CollectSources(context.Background(), client, "test", []Source{{Pod: "db-1", Container: "database"}}, 0, nil, true)
			if len(got.Clipped) != 1 || !got.Clipped[0].Last.IsZero() {
				t.Fatalf("unknown clip=%+v", got.Clipped)
			}
			end := time.Now()
			notice := got.Summarize(func(c Clip) bool { return c.Last.IsZero() || c.Last.Before(end) })
			if notice != "1 source reached the 64 KiB snapshot limit." {
				t.Fatalf("unknown clipping was hidden: %q", notice)
			}
		})
	}
}

func TestParseLineAcceptsAllKubernetesTimestampPrecisions(t *testing.T) {
	for _, tc := range []struct{ line, stamp, content string }{
		{"2026-10-13T10:00:00Z hi", "2026-10-13T10:00:00Z", "hi"},
		{"2026-10-13T10:00:00.123456789Z ", "2026-10-13T10:00:00.123456789Z", ""},
		{"2026-10-13T10:00:00Z ", "2026-10-13T10:00:00Z", ""},
		{"2026-10-13T10:00:00+00:00 spaced  content", "2026-10-13T10:00:00+00:00", "spaced  content"},
		{"2026-13-99T10:00:00Z invalid", "", "2026-13-99T10:00:00Z invalid"},
		{"unstructured log line", "", "unstructured log line"},
	} {
		stamp, content := ParseLine(tc.line)
		if stamp != tc.stamp || content != tc.content {
			t.Errorf("ParseLine(%q)=(%q,%q), want (%q,%q)", tc.line, stamp, content, tc.stamp, tc.content)
		}
	}
}

func TestCollectPreviousRunsReadsOnlyRestartedContainers(t *testing.T) {
	var mu sync.Mutex
	asked := []string{}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		asked = append(asked, strings.Split(r.URL.Path, "/")[6]+"/"+r.URL.Query().Get("container")+" previous="+r.URL.Query().Get("previous"))
		mu.Unlock()
		fmt.Fprintln(w, "2026-10-13T10:00:00Z before the crash")
	}))
	defer api.Close()
	client, err := kubernetes.NewForConfig(&rest.Config{Host: api.URL})
	if err != nil {
		t.Fatal(err)
	}
	pod := func(name string, statuses ...corev1.ContainerStatus) *corev1.Pod {
		p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name}}
		for _, s := range statuses {
			p.Spec.Containers = append(p.Spec.Containers, corev1.Container{Name: s.Name})
		}
		p.Status.ContainerStatuses = statuses
		return p
	}
	restarted := corev1.ContainerStatus{Name: "app", RestartCount: 2}
	terminated := corev1.ContainerStatus{Name: "sidecar", LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 1}}}
	fresh := corev1.ContainerStatus{Name: "app"}
	pods := []*corev1.Pod{
		pod("web-a", restarted, corev1.ContainerStatus{Name: "sidecar"}),
		pod("web-b", fresh, terminated),
		pod("web-c", fresh),
	}

	got, found := CollectPreviousRuns(context.Background(), client, "test", pods, "", 100, nil)
	if !found || len(got.Logs) != 2 || got.Notice != "" {
		t.Fatalf("found=%v snapshot=%+v", found, got)
	}
	for _, e := range got.Logs {
		if !e.Previous {
			t.Fatalf("entry not marked previous: %+v", e)
		}
	}
	mu.Lock()
	sort.Strings(asked)
	if want := []string{"web-a/app previous=true", "web-b/sidecar previous=true"}; !reflect.DeepEqual(asked, want) {
		t.Fatalf("asked %v, want %v", asked, want)
	}
	mu.Unlock()

	if _, found := CollectPreviousRuns(context.Background(), client, "test", pods, "app", 100, nil); !found {
		t.Fatal("container filter dropped web-a/app")
	}
	if got, found := CollectPreviousRuns(context.Background(), client, "test", pods[2:], "", 100, nil); found || len(got.Logs) != 0 {
		t.Fatalf("no restarts: found=%v logs=%d", found, len(got.Logs))
	}
}
