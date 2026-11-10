// Copyright (c) 2026 Mister.D LLC. Licensed under the Thulu Enterprise License, see ee/LICENSE.
// SPDX-License-Identifier: LicenseRef-Thulu-Enterprise

package kuberuntime

import (
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/kubeapi"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/MisterDonnuts/thulu/internal/apperr"
)

var ErrQuotaFull = kubeapi.ErrQuotaFull

var ErrQuotaTooSmall error = apperr.Precondition("kuberuntime.errors.quota_too_small")

// kube-apiserver reports a quota refusal as 403 "exceeded quota: <name>, requested: <list>, used:
// <list>, limited: <list>", the only place the numbers appear.
// Two creates racing for the same quota get 409 Conflict on the ResourceQuota object; that one also
// clears on retry.
func QuotaRefusal(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	if apierrors.IsConflict(err) && strings.Contains(msg, "resourcequotas") {
		return quotaFull(QuotaName, raceErr)
	}
	if !apierrors.IsForbidden(err) || !strings.Contains(msg, refusalPrefix) {
		return nil
	}
	r, parsed := parseQuotaRefusal(msg)
	if !parsed {
		return quotaFull(QuotaName, msg)
	}
	if lines := r.shortfall(true); len(lines.Errs) > 0 {
		return apperr.Wrap(ErrQuotaTooSmall, "kuberuntime.errors.quota_too_small_detail", "quota", r.quota, "detail", lines)
	}
	return quotaFull(r.quota, r.shortfall(false))
}

func quotaFull(quota string, detail any) error {
	return apperr.Wrap(ErrQuotaFull, "kuberuntime.errors.quota_full_detail", "quota", quota, "detail", detail)
}

const refusalPrefix = "exceeded quota: " //i18n:ignore kube-apiserver's refusal format, read back, not a message

var raceErr = apperr.New("kuberuntime.errors.quota_race")

func IsQuotaFull(err error) bool { return kubeapi.IsQuotaFull(err) }

type quotaRefusal struct {
	quota                    string
	requested, used, limited map[string]resource.Quantity
}

func parseQuotaRefusal(msg string) (quotaRefusal, bool) {
	_, rest, ok := strings.Cut(msg, refusalPrefix)
	if !ok {
		return quotaRefusal{}, false
	}
	name, rest, ok := strings.Cut(rest, ", requested: ")
	if !ok {
		return quotaRefusal{}, false
	}
	req, rest, ok := strings.Cut(rest, ", used: ")
	if !ok {
		return quotaRefusal{}, false
	}
	used, lim, ok := strings.Cut(rest, ", limited: ")
	if !ok {
		return quotaRefusal{}, false
	}
	r := quotaRefusal{quota: strings.TrimSpace(name)}
	if r.requested, ok = parseQuotaList(req); !ok {
		return quotaRefusal{}, false
	}
	if r.used, ok = parseQuotaList(used); !ok {
		return quotaRefusal{}, false
	}
	if r.limited, ok = parseQuotaList(lim); !ok || len(r.requested) == 0 {
		return quotaRefusal{}, false
	}
	return r, true
}

func parseQuotaList(s string) (map[string]resource.Quantity, bool) {
	out := map[string]resource.Quantity{}
	for _, part := range strings.Split(strings.TrimSpace(s), ",") {
		if part == "" {
			continue
		}
		k, v, ok := strings.Cut(part, "=")
		if !ok {
			return nil, false
		}
		q, err := resource.ParseQuantity(strings.TrimSpace(v))
		if err != nil {
			return nil, false
		}
		out[strings.TrimSpace(k)] = q
	}
	return out, true
}

func (r quotaRefusal) shortfall(never bool) apperr.List {
	keys := make([]string, 0, len(r.requested))
	for k := range r.requested {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := apperr.List{Sep: "; "}
	for _, k := range keys {
		asked, total := r.requested[k], r.limited[k]
		if never && asked.Cmp(total) <= 0 {
			continue
		}
		out.Errs = append(out.Errs, quotaLine(k, asked, r.used[k], total))
	}
	return out
}

func quotaLine(resourceName string, asked, used, total resource.Quantity) error {
	switch strings.TrimPrefix(strings.TrimPrefix(resourceName, "requests."), "limits.") {
	case "cpu":
		return apperr.New("kuberuntime.errors.quota_line_cpu",
			"requested", cores(asked), "used", cores(used), "hard", cores(total))
	case "memory":
		return apperr.New("kuberuntime.errors.quota_line_memory",
			"requested", gib(asked), "used", gib(used), "hard", gib(total))
	case "pods", "count/pods":
		return apperr.New("kuberuntime.errors.quota_line_pods", "used", used.Value(), "hard", total.Value())
	}
	return apperr.New("kuberuntime.errors.quota_line",
		"resource", resourceName, "requested", asked.String(), "used", used.String(), "hard", total.String())
}

func cores(q resource.Quantity) string {
	return strconv.FormatFloat(math.Round(float64(q.MilliValue())/10)/100, 'f', -1, 64)
}

func gib(q resource.Quantity) string {
	return strconv.FormatFloat(math.Round(q.AsApproximateFloat64()/(1<<30)*10)/10, 'f', -1, 64)
}
