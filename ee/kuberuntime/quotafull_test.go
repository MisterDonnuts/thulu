// Copyright (c) 2026 Mister.D LLC. Licensed under the Thulu Enterprise License, see ee/LICENSE.
// SPDX-License-Identifier: LicenseRef-Thulu-Enterprise

package kuberuntime

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	k8stesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/cache"

	"github.com/MisterDonnuts/thulu/internal/apperr"
)

func quotaForbidden(detail string) error {
	return apierrors.NewForbidden(schema.GroupResource{Resource: "pods"}, "agent-x", errors.New("exceeded quota: "+detail))
}

func TestQuotaRefusal(t *testing.T) {
	cases := []struct {
		name    string
		err     error
		ok      bool
		full    bool
		tooBig  bool
		mention []string
	}{
		{
			name:    "full: a refusal at the CPU limit",
			err:     quotaForbidden("octopus-tenant-quota, requested: limits.cpu=3250m, used: limits.cpu=3250m, limited: limits.cpu=6"),
			ok:      true,
			full:    true,
			mention: []string{"octopus-tenant-quota", "3.25 of 6 CPU"},
		},
		{
			name: "full: several resources",
			err: quotaForbidden("octopus-tenant-quota, requested: requests.cpu=1,requests.memory=1776Mi, " +
				"used: requests.cpu=5500m,requests.memory=15Gi, limited: requests.cpu=6,requests.memory=16Gi"),
			ok:      true,
			full:    true,
			mention: []string{"5.5 of 6 CPU", "15 of 16 GiB", "1.7 GiB"},
		},
		{
			name:    "too small: the pod alone overflows",
			err:     quotaForbidden("octopus-tenant-quota, requested: requests.cpu=8, used: requests.cpu=0, limited: requests.cpu=6"),
			ok:      true,
			tooBig:  true,
			mention: []string{"0 of 6 CPU", "8 more"},
		},
		{
			name: "unreadable numbers: still the quota's, wait",
			err:  quotaForbidden("octopus-tenant-quota, something new the API says"),
			ok:   true,
			full: true,
		},
		{
			name: "race on the quota object",
			err: apierrors.NewConflict(schema.GroupResource{Resource: "resourcequotas"}, QuotaName,
				errors.New("the object has been modified")),
			ok:   true,
			full: true,
		},
		{
			name: "forbidden for another reason",
			err:  apierrors.NewForbidden(schema.GroupResource{Resource: "pods"}, "agent-x", errors.New("violates PodSecurity")),
		},
		{
			name: "conflict on something else",
			err:  apierrors.NewConflict(schema.GroupResource{Resource: "pods"}, "agent-x", errors.New("exists")),
		},
		{name: "plain error", err: errors.New("boom")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := QuotaRefusal(c.err)
			if (out != nil) != c.ok {
				t.Fatalf("refusal = %v, want one: %v", out, c.ok)
			}
			if out == nil {
				return
			}
			if IsQuotaFull(out) != c.full {
				t.Errorf("IsQuotaFull = %v, want %v: %v", IsQuotaFull(out), c.full, out)
			}
			if errors.Is(out, ErrQuotaTooSmall) != c.tooBig {
				t.Errorf("too small = %v, want %v: %v", errors.Is(out, ErrQuotaTooSmall), c.tooBig, out)
			}
			wantKind := apperr.KindUnavailable
			if c.tooBig {
				wantKind = apperr.KindPrecondition
			}
			if apperr.KindOf(out) != wantKind {
				t.Errorf("kind = %v, want %v", apperr.KindOf(out), wantKind)
			}
			text := apperr.Text(out)
			for _, m := range c.mention {
				if !strings.Contains(text, m) {
					t.Errorf("message must name %q: %s", m, text)
				}
			}
		})
	}
}

func TestQuotaRefusalNil(t *testing.T) {
	if out := QuotaRefusal(nil); out != nil {
		t.Errorf("nil = %v", out)
	}
}

func TestParseQuotaRefusal(t *testing.T) {
	r, ok := parseQuotaRefusal(`pods "agent-x" is forbidden: exceeded quota: q1, requested: limits.cpu=3250m,pods=1, used: limits.cpu=3250m,pods=2, limited: limits.cpu=6,pods=4`)
	if !ok {
		t.Fatal("must parse")
	}
	if r.quota != "q1" {
		t.Errorf("quota = %q", r.quota)
	}
	want := resource.MustParse("3250m")
	if got := r.requested["limits.cpu"]; got.Cmp(want) != 0 {
		t.Errorf("requested cpu = %s", got.String())
	}
	if got := r.limited["pods"]; got.Value() != 4 {
		t.Errorf("limited pods = %s", got.String())
	}
	for _, bad := range []string{
		"exceeded quota: q1",
		"exceeded quota: q1, requested: cpu=1, used: cpu=1",
		"exceeded quota: q1, requested: cpu, used: cpu=1, limited: cpu=2",
		"exceeded quota: q1, requested: cpu=lots, used: cpu=1, limited: cpu=2",
		"exceeded quota: q1, requested: , used: cpu=1, limited: cpu=2",
		"no quota here",
	} {
		if _, ok := parseQuotaRefusal(bad); ok {
			t.Errorf("must not parse %q", bad)
		}
	}
}

func TestLaunchPodQuotaFull(t *testing.T) {
	rt, client := newFakeRuntime(t, Config{})
	client.PrependReactor("create", "pods", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, quotaForbidden("octopus-tenant-quota, requested: requests.cpu=1, used: requests.cpu=6, limited: requests.cpu=6")
	})
	ctx := context.Background()
	spec := baseSpec()
	_, err := rt.LaunchPod(ctx, spec)
	if !IsQuotaFull(err) {
		t.Fatalf("LaunchPod must report the quota refusal, got %v", err)
	}
	secrets, lerr := client.CoreV1().Secrets(rt.NamespaceFor(spec.WorkspaceID)).List(ctx, metav1.ListOptions{})
	if lerr != nil {
		t.Fatal(lerr)
	}
	if len(secrets.Items) != 0 {
		t.Errorf("a refused pod must not leave its env secret behind: %d left", len(secrets.Items))
	}
}

func defaultQuota() TenantQuota {
	iso := TenantIsolation{}
	iso.applyDefaults()
	return iso.Quota
}

func TestTenantQuotaHard(t *testing.T) {
	q := defaultQuota()
	h := q.hard()
	if _, ok := h[corev1.ResourceLimitsCPU]; ok {
		t.Error("CPU limits must not be summed by default")
	}
	for res, want := range map[corev1.ResourceName]string{
		corev1.ResourcePods:           defaultQuotaPods,
		corev1.ResourceRequestsCPU:    defaultQuotaCPURequests,
		corev1.ResourceRequestsMemory: defaultQuotaMemoryRequests,
		corev1.ResourceLimitsMemory:   defaultQuotaMemory,
	} {
		got := h[res]
		if got.Cmp(resource.MustParse(want)) != 0 {
			t.Errorf("%s = %s, want %s", res, got.String(), want)
		}
	}

	q.CPU, q.Memory = "12", QuotaOff
	h = q.hard()
	if got := h[corev1.ResourceLimitsCPU]; got.String() != "12" {
		t.Errorf("a CPU ceiling that is set must be in the quota, got %s", got.String())
	}
	if _, ok := h[corev1.ResourceLimitsMemory]; ok {
		t.Error("limits.memory off must leave the line out")
	}
}

func TestTenantQuotaValidate(t *testing.T) {
	agent := corev1.ResourceRequirements{
		Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("250m"), corev1.ResourceMemory: resource.MustParse("512Mi")},
		Limits:   corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2"), corev1.ResourceMemory: resource.MustParse("6Gi")},
	}
	base := defaultQuota
	if err := base().validate(agent); err != nil {
		t.Fatalf("defaults must fit the default agent: %v", err)
	}
	bad := map[string]func(*TenantQuota){
		"bad cpu requests":      func(q *TenantQuota) { q.CPURequests = "lots" },
		"bad memory requests":   func(q *TenantQuota) { q.MemoryRequests = "lots" },
		"bad cpu ceiling":       func(q *TenantQuota) { q.CPU = "lots" },
		"requests under agent":  func(q *TenantQuota) { q.CPURequests = "100m" },
		"memory under agent":    func(q *TenantQuota) { q.MemoryRequests = "256Mi" },
		"ceiling under agent":   func(q *TenantQuota) { q.CPU = "1" },
		"memory ceiling agent":  func(q *TenantQuota) { q.Memory = "4Gi" },
		"limit max under agent": func(q *TenantQuota) { q.MaxCPU = "1" },
	}
	for name, mut := range bad {
		q := base()
		mut(&q)
		if err := q.validate(agent); err == nil {
			t.Errorf("%s: validate must refuse", name)
		}
	}
	q := base()
	q.CPU, q.Memory = " off ", "OFF"
	if err := q.validate(agent); err != nil {
		t.Errorf("off ceilings must validate: %v", err)
	}
}

func TestQuotaFromEnvRequests(t *testing.T) {
	env := map[string]string{
		EnvRuntimeClass:        "kata-clh",
		EnvQuotaCPURequests:    "6",
		EnvQuotaMemoryRequests: "16Gi",
		EnvQuotaMemory:         "56Gi",
	}
	cfg, err := ConfigFromEnv(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	q := cfg.Isolation.Quota
	if q.CPURequests != "6" || q.MemoryRequests != "16Gi" || q.Memory != "56Gi" || q.CPU != "" {
		t.Errorf("quota from env = %+v", q)
	}
}

func TestPodEndHandler(t *testing.T) {
	var got []string
	h := podEndHandler(func(ns string) { got = append(got, ns) })
	pod := func(ns string, phase corev1.PodPhase) *corev1.Pod {
		return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: ns}, Status: corev1.PodStatus{Phase: phase}}
	}
	h.OnUpdate(pod("a", corev1.PodRunning), pod("a", corev1.PodSucceeded))
	h.OnUpdate(pod("b", corev1.PodPending), pod("b", corev1.PodFailed))
	h.OnUpdate(pod("x", corev1.PodPending), pod("x", corev1.PodRunning))
	h.OnUpdate(pod("x", corev1.PodSucceeded), pod("x", corev1.PodSucceeded))
	h.OnDelete(pod("c", corev1.PodRunning))
	h.OnDelete(pod("x", corev1.PodFailed))
	h.OnDelete(cache.DeletedFinalStateUnknown{Key: "d/p", Obj: pod("d", corev1.PodRunning)})
	want := []string{"a", "b", "c", "d"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("ended = %v, want %v", got, want)
	}
}

func TestWatchPodEnds(t *testing.T) {
	rt, client := newFakeRuntime(t, Config{})
	ctx, cancel := context.WithCancel(context.Background())
	var mu sync.Mutex
	var ended []string
	done := make(chan struct{})
	go func() {
		defer close(done)
		rt.WatchPodEnds(ctx, func(ns string) {
			mu.Lock()
			ended = append(ended, ns)
			mu.Unlock()
		})
	}()
	pods := client.CoreV1().Pods("tenant-acme")
	deadline := time.Now().Add(5 * time.Second)
	for i := 0; ; i++ {
		mu.Lock()
		n := len(ended)
		mu.Unlock()
		if n > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no pod end reported")
		}
		p := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name: "agent-" + strconv.Itoa(i), Namespace: "tenant-acme",
				Labels: map[string]string{LabelManagedBy: managedByValue},
			},
			Status: corev1.PodStatus{Phase: corev1.PodRunning},
		}
		created, err := pods.Create(ctx, p, metav1.CreateOptions{})
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(20 * time.Millisecond)
		created.Status.Phase = corev1.PodSucceeded
		if _, err := pods.UpdateStatus(ctx, created, metav1.UpdateOptions{}); err != nil {
			t.Fatal(err)
		}
		time.Sleep(20 * time.Millisecond)
		if err := pods.Delete(ctx, created.Name, metav1.DeleteOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	cancel()
	<-done
	mu.Lock()
	defer mu.Unlock()
	if ended[0] != "tenant-acme" {
		t.Errorf("ended namespace = %q", ended[0])
	}
}

func TestQuotaLineReadsForOneCore(t *testing.T) {
	err := quotaLine("requests.cpu", resource.MustParse("1"), resource.MustParse("3"), resource.MustParse("4"))
	es := apperr.Localize(err, "es")
	if !strings.Contains(es, "necesita 1 más") || strings.Contains(es, "faltan") {
		t.Errorf("es = %q, want it to say the workspace needs 1 more", es)
	}
	if en := apperr.Localize(err, "en"); !strings.Contains(en, "needs 1 more") {
		t.Errorf("en = %q", en)
	}
}

func TestLaunchPodWorktreeClaimQuotaFull(t *testing.T) {
	rt, client := newFakeRuntime(t, Config{})
	client.PrependReactor("create", "persistentvolumeclaims", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, quotaForbidden("octopus-tenant-quota, requested: requests.storage=10Gi, used: requests.storage=80Gi, limited: requests.storage=80Gi")
	})
	ctx := context.Background()
	spec := baseSpec()
	spec.WorktreePVC = "octopus-wt-" + spec.TaskID
	_, err := rt.LaunchPod(ctx, spec)
	if !IsQuotaFull(err) {
		t.Fatalf("LaunchPod must report the claim's quota refusal, got %v", err)
	}
	pods, lerr := client.CoreV1().Pods(rt.NamespaceFor(spec.WorkspaceID)).List(ctx, metav1.ListOptions{})
	if lerr != nil {
		t.Fatal(lerr)
	}
	if len(pods.Items) != 0 {
		t.Errorf("no pod may wait on a refused claim: %d created", len(pods.Items))
	}
}
